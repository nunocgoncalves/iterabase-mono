// Package kind owns fresh local Kind cluster lifecycle and kubeconfig isolation.
package kind

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
)

var prefixPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// Manager creates unique clusters through a testable process seam.
type Manager struct {
	Executor process.Executor
	TempRoot string
	Now      func() time.Time
	Random   io.Reader
}

// Cluster is an isolated Kind handle. Delete is idempotent.
type Cluster struct {
	Name       string
	Kubeconfig string

	executor  process.Executor
	tempDir   string
	owned     bool
	mu        sync.Mutex
	deleted   bool
	deleteErr error
}

// Create provisions a fresh cluster whose name and kubeconfig cannot collide
// with a stale local or CI run.
func (manager Manager) Create(ctx context.Context, prefix string) (*Cluster, error) {
	if manager.Executor == nil {
		return nil, fmt.Errorf("kind manager has no process executor")
	}
	name, err := manager.uniqueName(prefix)
	if err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp(manager.TempRoot, "iterabase-e2e-kind-")
	if err != nil {
		return nil, err
	}
	kubeconfig, err := filepath.Abs(filepath.Join(tempDir, "kubeconfig.yaml"))
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}
	_, err = manager.Executor.Run(ctx, process.Command{
		Name: "kind", Args: []string{"create", "cluster", "--name", name, "--kubeconfig", kubeconfig, "--wait", "120s"},
		Timeout: 5 * time.Minute, OutputName: "kind-create-" + name + ".log",
	})
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_, deleteErr := manager.Executor.Run(cleanupCtx, process.Command{
			Name: "kind", Args: []string{"delete", "cluster", "--name", name},
			Timeout: 5 * time.Minute, OutputName: "kind-delete-after-create-failure-" + name + ".log",
		})
		_ = os.RemoveAll(tempDir)
		if deleteErr != nil {
			return nil, fmt.Errorf("create kind cluster %s: %w (best-effort delete also failed: %v)", name, err, deleteErr)
		}
		return nil, fmt.Errorf("create kind cluster %s: %w", name, err)
	}
	return &Cluster{Name: name, Kubeconfig: kubeconfig, executor: manager.Executor, tempDir: tempDir, owned: true}, nil
}

// Use wraps an exact existing kubeconfig without taking lifecycle ownership.
func Use(name, kubeconfig string, executor process.Executor) (*Cluster, error) {
	absolute, err := filepath.Abs(kubeconfig)
	if err != nil {
		return nil, err
	}
	if name == "" || executor == nil {
		return nil, fmt.Errorf("existing cluster requires a name and executor")
	}
	return &Cluster{Name: name, Kubeconfig: absolute, executor: executor}, nil
}

// LoadImage loads an explicitly named local image into every Kind node.
func (cluster *Cluster) LoadImage(ctx context.Context, image string) error {
	if image == "" {
		return fmt.Errorf("kind image identity is empty")
	}
	_, err := cluster.executor.Run(ctx, process.Command{
		Name: "kind", Args: []string{"load", "docker-image", "--name", cluster.Name, image},
		Timeout: 10 * time.Minute, OutputName: "kind-load-" + safeFileName(image) + ".log",
	})
	return err
}

// ImportImageArchive restores one composer-resolved archive into the runner
// daemon after cluster creation and then transports that exact reference into
// every Kind node. Callers cannot rely on images loaded before Kind existed.
func (cluster *Cluster) ImportImageArchive(ctx context.Context, archive, image string) error {
	if image == "" {
		return fmt.Errorf("kind image identity is empty")
	}
	info, err := os.Stat(archive)
	if err != nil {
		return fmt.Errorf("resolved image archive %q: %w", archive, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("resolved image archive %q is not a regular file", archive)
	}
	if _, err := cluster.executor.Run(ctx, process.Command{
		Name: "docker", Args: []string{"load", "-i", archive},
		Timeout: 10 * time.Minute, OutputName: "docker-load-" + safeFileName(image) + ".log",
	}); err != nil {
		return fmt.Errorf("restore resolved image %s: %w", image, err)
	}
	if err := cluster.LoadImage(ctx, image); err != nil {
		return fmt.Errorf("import resolved image %s into Kind: %w", image, err)
	}
	return nil
}

// Delete tears down owned infrastructure and removes its temporary kubeconfig.
func (cluster *Cluster) Delete(ctx context.Context) error {
	cluster.mu.Lock()
	defer cluster.mu.Unlock()
	if cluster.deleted {
		return cluster.deleteErr
	}
	cluster.deleted = true
	if cluster.owned {
		_, cluster.deleteErr = cluster.executor.Run(ctx, process.Command{
			Name: "kind", Args: []string{"delete", "cluster", "--name", cluster.Name},
			Timeout: 5 * time.Minute, OutputName: "kind-delete-" + cluster.Name + ".log",
		})
	}
	if cluster.tempDir != "" {
		if err := os.RemoveAll(cluster.tempDir); cluster.deleteErr == nil && err != nil {
			cluster.deleteErr = err
		}
	}
	return cluster.deleteErr
}

func (manager Manager) uniqueName(prefix string) (string, error) {
	if !prefixPattern.MatchString(prefix) {
		return "", fmt.Errorf("kind cluster prefix %q must be lowercase DNS-safe", prefix)
	}
	now := manager.Now
	if now == nil {
		now = time.Now
	}
	random := manager.Random
	if random == nil {
		random = rand.Reader
	}
	var entropy [4]byte
	if _, err := io.ReadFull(random, entropy[:]); err != nil {
		return "", err
	}
	suffix := fmt.Sprintf("-%x-%s", now().UnixNano(), hex.EncodeToString(entropy[:]))
	maxPrefix := 63 - len(suffix)
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "-")
	}
	return prefix + suffix, nil
}

func safeFileName(value string) string {
	value = strings.NewReplacer("/", "-", ":", "-", "@", "-").Replace(value)
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}
