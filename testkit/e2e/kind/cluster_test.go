package kind

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
)

type fakeExecutor struct {
	mu       sync.Mutex
	commands []process.Command
	failNext bool
}

func (executor *fakeExecutor) Run(_ context.Context, command process.Command) (process.Result, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.commands = append(executor.commands, command)
	if executor.failNext {
		executor.failNext = false
		return process.Result{}, errors.New("forced create failure")
	}
	return process.Result{}, nil
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
	executor := &fakeExecutor{}
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

	if err := cluster.ImportImageArchive(context.Background(), archive, "iterabase-e2e/control-plane:exact-head"); err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 2 {
		t.Fatalf("import commands = %d, want docker restore + Kind transport", len(executor.commands))
	}
	if got := executor.commands[0]; got.Name != "docker" || !slices.Equal(got.Args, []string{"load", "-i", archive}) {
		t.Fatalf("first import command = %+v, want exact downloaded archive restore", got)
	}
	if got := executor.commands[1]; got.Name != "kind" || !slices.Equal(got.Args, []string{"load", "docker-image", "--name", "charts", "iterabase-e2e/control-plane:exact-head"}) {
		t.Fatalf("second import command = %+v, want post-create Kind transport", got)
	}
}

func TestMissingDownloadedRuntimeArtifactCannotReachClusterImport(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{}
	cluster, err := Use("charts", filepath.Join(t.TempDir(), "kubeconfig"), executor)
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster.ImportImageArchive(context.Background(), filepath.Join(t.TempDir(), "missing.tar"), "iterabase-e2e/control-plane:exact-head"); err == nil {
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
