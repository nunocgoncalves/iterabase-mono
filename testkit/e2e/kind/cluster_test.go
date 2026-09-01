package kind

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
)

type fakeExecutor struct {
	mu        sync.Mutex
	commands  []process.Command
	failNext  bool
	outputFor func(process.Command) string
}

func (executor *fakeExecutor) Run(_ context.Context, command process.Command) (process.Result, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.commands = append(executor.commands, command)
	if executor.failNext {
		executor.failNext = false
		return process.Result{}, errors.New("forced create failure")
	}
	result := process.Result{}
	if executor.outputFor != nil {
		result.Output = executor.outputFor(command)
	}
	return result, nil
}

func TestCreateFailureStillAttemptsClusterDeletion(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{failNext: true}
	manager := Manager{
		Executor: executor, TempRoot: t.TempDir(),
		Now: func() time.Time { return time.Unix(0, 42) }, Random: bytes.NewReader([]byte("abcd")),
	}
	if _, err := manager.Create(context.Background(), "failed-e2e"); err == nil {
		t.Fatal("forced create failure unexpectedly passed")
	}
	if len(executor.commands) != 2 || executor.commands[1].Args[0] != "delete" {
		t.Fatalf("create failure did not attempt delete: %+v", executor.commands)
	}
}

func TestDownloadedRuntimeArtifactRequiresPostCreateClusterImport(t *testing.T) {
	t.Parallel()
	configDigest := "sha256:" + strings.Repeat("a", 64)
	runtimeDigest := "sha256:" + strings.Repeat("b", 64)
	executor := &fakeExecutor{outputFor: func(command process.Command) string {
		if command.Name == "kind" && slices.Equal(command.Args, []string{"get", "nodes", "--name", "charts"}) {
			return "charts-control-plane\n"
		}
		if command.Name == "docker" && len(command.Args) > 2 && command.Args[0] == "exec" && command.Args[2] == "crictl" {
			return fmt.Sprintf(`{"status":{"id":%q,"repoTags":["docker.io/iterabase-e2e/control-plane:exact-head"]},"info":{"imageSpec":{"config":{"Labels":{"org.opencontainers.image.revision":"exact-head"}}}}}`, configDigest)
		}
		if command.Name == "docker" && len(command.Args) > 2 && command.Args[0] == "exec" && command.Args[2] == "ctr" {
			return "REF TYPE DIGEST SIZE PLATFORMS LABELS\n" +
				"docker.io/iterabase-e2e/control-plane:exact-head application/vnd.oci.image.manifest.v1+json " + runtimeDigest + " 1B linux/amd64 -\n"
		}
		return ""
	}}
	cluster, err := Use("charts", filepath.Join(t.TempDir(), "kubeconfig"), executor)
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(t.TempDir(), "artifacts", "e2e-runtime-control-plane-image")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifactDir, "control-plane-image.tar")
	if err := os.WriteFile(archive, []byte("exact downloaded archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	identity, err := cluster.ImportImageArchive(context.Background(), archive, "iterabase-e2e/control-plane:exact-head", configDigest)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ConfigDigest != configDigest || identity.RuntimeDigest != runtimeDigest || identity.Labels["org.opencontainers.image.revision"] != "exact-head" {
		t.Fatalf("imported identity = %+v", identity)
	}
	if len(executor.commands) != 5 {
		t.Fatalf("import commands = %d, want docker restore + Kind transport + config/manifest identity inspection", len(executor.commands))
	}
	if got := executor.commands[0]; got.Name != "docker" || !slices.Equal(got.Args, []string{"load", "-i", archive}) {
		t.Fatalf("first import command = %+v, want exact downloaded archive restore", got)
	}
	if got := executor.commands[1]; got.Name != "kind" || !slices.Equal(got.Args, []string{"load", "docker-image", "--name", "charts", "iterabase-e2e/control-plane:exact-head"}) {
		t.Fatalf("second import command = %+v, want post-create Kind transport", got)
	}
	if got := executor.commands[3]; got.Name != "docker" || !slices.Equal(got.Args, []string{"exec", "charts-control-plane", "crictl", "inspecti", "iterabase-e2e/control-plane:exact-head"}) {
		t.Fatalf("config inspection command = %+v, want exact node config identity", got)
	}
	if got := executor.commands[4]; got.Name != "docker" || !slices.Equal(got.Args, []string{"exec", "charts-control-plane", "ctr", "-n", "k8s.io", "images", "list"}) {
		t.Fatalf("manifest inspection command = %+v, want exact imported runtime identity", got)
	}
}

func TestMissingDownloadedRuntimeArtifactCannotReachClusterImport(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{}
	cluster, err := Use("charts", filepath.Join(t.TempDir(), "kubeconfig"), executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cluster.ImportImageArchive(context.Background(), filepath.Join(t.TempDir(), "missing.tar"), "iterabase-e2e/control-plane:exact-head", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("missing downloaded runtime artifact unexpectedly reached install transport")
	}
	if len(executor.commands) != 0 {
		t.Fatalf("missing archive executed import commands: %+v", executor.commands)
	}
}

func TestClusterLifecycleUsesUniqueNamesAndIsolatedKubeconfigs(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{}
	manager := Manager{
		Executor: executor,
		TempRoot: t.TempDir(),
		Now:      func() time.Time { return time.Unix(0, 42) },
		Random:   bytes.NewReader([]byte("abcdefgh")),
	}
	first, err := manager.Create(context.Background(), "charts-e2e")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), "charts-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name || first.Kubeconfig == second.Kubeconfig {
		t.Fatalf("clusters collided: first=%+v second=%+v", first, second)
	}
	if err := first.LoadImage(context.Background(), "control-plane:test"); err != nil {
		t.Fatal(err)
	}
	if err := first.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(executor.commands); got != 4 {
		t.Fatalf("commands = %d, want two creates + load + one delete", got)
	}
	if executor.commands[0].Args[0] != "create" || executor.commands[3].Args[0] != "delete" {
		t.Fatalf("unexpected lifecycle commands: %+v", executor.commands)
	}
}
