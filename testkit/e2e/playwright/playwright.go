// Package playwright provides the Go orchestration seam for locked browser tests.
package playwright

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/artifacts"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

// Invocation describes one locked Playwright run. Browser assertions remain in
// TypeScript; Go owns process bounds and artifact policy.
type Invocation struct {
	Directory   string
	Args        []string
	Timeout     time.Duration
	Artifacts   []artifacts.Entry
	ArtifactDir string
}

// Runner executes npm ci followed by the already-locked Playwright binary.
type Runner struct {
	Executor process.Executor
	Redactor *redact.Redactor
}

// Run invokes exactly once with retries forced off and collects declared
// artifacts whether the browser assertion passes or fails.
func (runner Runner) Run(ctx context.Context, invocation Invocation) error {
	if runner.Executor == nil || invocation.Directory == "" || invocation.Timeout <= 0 {
		return fmt.Errorf("playwright invocation requires executor, directory, and positive timeout")
	}
	if !filepath.IsAbs(invocation.Directory) {
		return fmt.Errorf("playwright package directory must be absolute")
	}
	if _, err := os.Stat(filepath.Join(invocation.Directory, "package-lock.json")); err != nil {
		return fmt.Errorf("playwright package must have package-lock.json: %w", err)
	}
	_, installErr := runner.Executor.Run(ctx, process.Command{
		Name: "npm", Args: []string{"ci"}, Dir: invocation.Directory,
		Timeout: invocation.Timeout, OutputName: "playwright-npm-ci.log",
	})
	var testErr error
	if installErr == nil {
		args := []string{"--no-install", "playwright", "test", "--retries=0"}
		args = append(args, invocation.Args...)
		_, testErr = runner.Executor.Run(ctx, process.Command{
			Name: "npx", Args: args, Dir: invocation.Directory,
			Timeout: invocation.Timeout, OutputName: "playwright-test.log",
		})
	}
	artifactErr := artifacts.Collect(invocation.Artifacts, invocation.ArtifactDir, runner.Redactor)
	return errors.Join(installErr, testErr, artifactErr)
}
