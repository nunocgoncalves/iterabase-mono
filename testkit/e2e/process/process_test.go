package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

func TestRunnerBoundsAndRedactsProcessOutput(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runner := Runner{Redactor: redact.New("process-secret"), OutputDir: outputDir}
	result, err := runner.Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", "printf 'token=process-secret\\n'; exit 7"},
		Timeout: time.Second, OutputName: "process.log",
	})
	if err == nil || result.ExitCode != 7 {
		t.Fatalf("process result = %+v, error = %v", result, err)
	}
	if strings.Contains(result.Output, "process-secret") {
		t.Fatalf("process output was not redacted: %s", result.Output)
	}
	data, readErr := os.ReadFile(filepath.Join(outputDir, "process.log"))
	if readErr != nil || strings.Contains(string(data), "process-secret") {
		t.Fatalf("persisted process evidence = %q, error = %v", data, readErr)
	}
}

func TestRunnerTerminatesAtTimeout(t *testing.T) {
	t.Parallel()
	started := time.Now()
	_, err := (Runner{}).Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", "sleep 2"}, Timeout: 20 * time.Millisecond,
	})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("timed process error = %v", err)
	}
}

func TestRunnerRequiresTimeout(t *testing.T) {
	t.Parallel()
	_, err := (Runner{}).Run(context.Background(), Command{Name: "true"})
	if err == nil {
		t.Fatal("unbounded process unexpectedly accepted")
	}
}
