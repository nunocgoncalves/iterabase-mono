// Package artifacts enforces fail-closed collection of component-declared evidence.
package artifacts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

// Kind controls how artifact bytes are handled.
type Kind string

const (
	maxArtifactBytes = 64 << 20

	// Text is always passed through the shared redactor.
	Text Kind = "text"
	// SafeSyntheticOpaque is copied byte-for-byte only after an owner explicitly
	// declares that the fixture contains synthetic, non-secret data.
	SafeSyntheticOpaque Kind = "safe-synthetic-opaque"
)

// Entry declares one file or directory produced by a component stage.
type Entry struct {
	Name   string
	Source string
	Kind   Kind
}

// Collect copies declared evidence beneath destination. Symlinks and undeclared
// opaque bytes fail closed.
func Collect(entries []Entry, destination string, redactor *redact.Redactor) error {
	if destination == "" {
		return fmt.Errorf("artifact destination is empty")
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		clean := filepath.Clean(entry.Name)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid artifact name %q", entry.Name)
		}
		if _, exists := seen[clean]; exists {
			return fmt.Errorf("duplicate artifact name %q", entry.Name)
		}
		seen[clean] = struct{}{}
		if entry.Kind != Text && entry.Kind != SafeSyntheticOpaque {
			return fmt.Errorf("artifact %q is opaque without an explicit safe-synthetic declaration", entry.Name)
		}
		if err := collectEntry(entry, filepath.Join(destination, clean), redactor); err != nil {
			return fmt.Errorf("collect artifact %q: %w", entry.Name, err)
		}
	}
	return nil
}

func collectEntry(entry Entry, destination string, redactor *redact.Redactor) error {
	info, err := os.Lstat(entry.Source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink source is not allowed")
	}
	if !info.IsDir() {
		return copyFile(entry.Source, destination, entry.Kind, redactor)
	}
	return filepath.WalkDir(entry.Source, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %s is not allowed", path)
		}
		relative, err := filepath.Rel(entry.Source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if item.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target, entry.Kind, redactor)
	})
}

func copyFile(source, destination string, kind Kind, redactor *redact.Redactor) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.Size() > maxArtifactBytes {
		return fmt.Errorf("artifact is %d bytes; maximum is %d", info.Size(), maxArtifactBytes)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if kind == Text {
		data = redactor.Bytes(data)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o600)
}
