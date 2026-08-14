package playwright

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/artifacts"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
)

type recordingExecutor struct{ commands []process.Command }

func (executor *recordingExecutor) Run(_ context.Context, command process.Command) (process.Result, error) {
	executor.commands = append(executor.commands, command)
	return process.Result{}, nil
}

func TestRunnerUsesLockedDependenciesDisablesRetriesAndCollectsArtifacts(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(directory, "report.txt")
	if err := os.WriteFile(report, []byte("synthetic report"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &recordingExecutor{}
	runner := Runner{Executor: executor}
	err := runner.Run(context.Background(), Invocation{
		Directory: directory, Timeout: time.Minute, Args: []string{"tests/journey.spec.ts"},
		Artifacts:   []artifacts.Entry{{Name: "report.txt", Source: report, Kind: artifacts.Text}},
		ArtifactDir: filepath.Join(directory, "collected"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 2 || executor.commands[0].Name != "npm" || executor.commands[1].Name != "npx" {
		t.Fatalf("commands = %+v", executor.commands)
	}
	if !slices.Contains(executor.commands[1].Args, "--no-install") || !slices.Contains(executor.commands[1].Args, "--retries=0") {
		t.Fatalf("Playwright command is not locked/retry-free: %v", executor.commands[1].Args)
	}
	if _, err := os.Stat(filepath.Join(directory, "collected", "report.txt")); err != nil {
		t.Fatalf("browser artifact not collected: %v", err)
	}
}
