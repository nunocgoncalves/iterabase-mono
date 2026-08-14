package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

func TestCollectRedactsTextAndRejectsUndeclaredOpaqueArtifacts(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "browser.log")
	if err := os.WriteFile(source, []byte("token: browser-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := Collect([]Entry{{Name: "browser.log", Source: source, Kind: Text}}, destination, redact.New("browser-secret")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "browser.log"))
	if err != nil || strings.Contains(string(data), "browser-secret") {
		t.Fatalf("collected text = %q, error = %v", data, err)
	}
	if err := Collect([]Entry{{Name: "trace.zip", Source: source, Kind: Kind("opaque")}}, t.TempDir(), nil); err == nil {
		t.Fatal("undeclared opaque artifact unexpectedly accepted")
	}
}

func TestCollectRejectsParentDirectoryArtifactName(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "evidence")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "escaped.log"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "collected")
	if err := Collect([]Entry{{Name: "..", Source: source, Kind: Text}}, destination, nil); err == nil {
		t.Fatal("parent-directory artifact name unexpectedly accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.log")); !os.IsNotExist(err) {
		t.Fatalf("artifact escaped destination: %v", err)
	}
}

func TestCollectAllowsExplicitSafeSyntheticOpaqueArtifact(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "screenshot.png")
	want := []byte{0, 1, 2, 3}
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := Collect([]Entry{{Name: "screenshot.png", Source: source, Kind: SafeSyntheticOpaque}}, destination, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "screenshot.png"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("opaque bytes = %v, error = %v", got, err)
	}
}
