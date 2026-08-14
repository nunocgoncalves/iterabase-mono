// Package catalogue discovers owner suites by compiling their TestE2E entrypoints.
package catalogue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

// Discover compiles every workspace module ending in /test/e2e in catalogue
// mode, then validates and merges the emitted registrations.
func Discover(ctx context.Context, root string) (e2e.Catalogue, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return e2e.Catalogue{}, err
	}
	workspace := filepath.Join(absoluteRoot, "go.work")
	cmd := exec.CommandContext(ctx, "go", "work", "edit", "-json")
	cmd.Dir = absoluteRoot
	cmd.Env = append(os.Environ(), "GOWORK="+workspace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return e2e.Catalogue{}, fmt.Errorf("inspect Go workspace: %w\n%s", err, output)
	}
	var work struct {
		Use []struct {
			DiskPath string
		} `json:"Use"`
	}
	if err := json.Unmarshal(output, &work); err != nil {
		return e2e.Catalogue{}, fmt.Errorf("decode Go workspace: %w", err)
	}
	var modules []string
	for _, use := range work.Use {
		module := use.DiskPath
		if !filepath.IsAbs(module) {
			module = filepath.Join(absoluteRoot, module)
		}
		module, err = filepath.Abs(module)
		if err != nil {
			return e2e.Catalogue{}, err
		}
		if strings.HasSuffix(filepath.ToSlash(module), "/test/e2e") {
			modules = append(modules, module)
		}
	}
	sort.Strings(modules)
	if len(modules) == 0 {
		return e2e.Catalogue{}, fmt.Errorf("workspace contains no owner test/e2e modules")
	}

	tempDir, err := os.MkdirTemp("", "iterabase-e2e-catalogue-")
	if err != nil {
		return e2e.Catalogue{}, err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	catalogues := make([]e2e.Catalogue, 0, len(modules))
	for index, module := range modules {
		cataloguePath := filepath.Join(tempDir, fmt.Sprintf("suite-%02d.json", index))
		var logs bytes.Buffer
		cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-run", "^TestE2E$", ".")
		cmd.Dir = module
		cmd.Env = append(os.Environ(), "GOWORK="+workspace, e2e.CatalogueOutputEnv+"="+cataloguePath)
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if err := cmd.Run(); err != nil {
			return e2e.Catalogue{}, fmt.Errorf("compile catalogue entrypoint %s: %w\n%s", module, err, logs.String())
		}
		data, err := os.ReadFile(cataloguePath)
		if err != nil {
			return e2e.Catalogue{}, fmt.Errorf("entrypoint %s emitted no catalogue: %w", module, err)
		}
		var compiled e2e.Catalogue
		if err := json.Unmarshal(data, &compiled); err != nil {
			return e2e.Catalogue{}, fmt.Errorf("decode catalogue from %s: %w", module, err)
		}
		relative, err := filepath.Rel(absoluteRoot, module)
		if err != nil {
			return e2e.Catalogue{}, err
		}
		for _, suite := range compiled.Suites {
			if filepath.Clean(suite.Suite.Entrypoint) != filepath.Clean(relative) {
				return e2e.Catalogue{}, fmt.Errorf("suite %s records entrypoint %q, compiled from %q", suite.Suite.Name, suite.Suite.Entrypoint, relative)
			}
		}
		catalogues = append(catalogues, compiled)
	}
	return e2e.MergeCatalogues(catalogues...)
}

// FindRoot walks upward to the repository go.work.
func FindRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.work")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cannot find repository go.work from %s", start)
		}
		current = parent
	}
}
