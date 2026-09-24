package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

const (
	pinnedImageCacheRootEnv       = "FORGE_E2E_IMAGE_CACHE_ROOT"
	pinnedImageCacheGenerationEnv = "FORGE_E2E_IMAGE_CACHE_GENERATION"
)

var pinnedImageCacheGenerationPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var pinnedImageCacheDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type pinnedImageCacheImage struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	Archive   string `json:"archive"`
}

type pinnedImageCacheManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	Capacity      string                  `json:"capacity"`
	Generation    string                  `json:"generation"`
	CacheRoot     string                  `json:"cache_root"`
	Images        []pinnedImageCacheImage `json:"images"`
}

func parsePinnedImageCacheManifest(capacity string, data []byte) (pinnedImageCacheManifest, error) {
	var manifest pinnedImageCacheManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("decode pinned image cache manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return manifest, fmt.Errorf("unsupported pinned image cache schema %d", manifest.SchemaVersion)
	}
	if manifest.Capacity != capacity {
		return manifest, fmt.Errorf("pinned image cache capacity %q does not match %q", manifest.Capacity, capacity)
	}
	if !pinnedImageCacheGenerationPattern.MatchString(manifest.Generation) {
		return manifest, fmt.Errorf("invalid pinned image cache generation %q", manifest.Generation)
	}
	if len(manifest.Images) == 0 {
		return manifest, fmt.Errorf("pinned image cache manifest has no images")
	}
	archives := make(map[string]bool, len(manifest.Images))
	for _, image := range manifest.Images {
		if image.Reference == "" || !pinnedImageCacheDigestPattern.MatchString(image.Digest) {
			return manifest, fmt.Errorf("pinned image %q has an incomplete digest identity", image.Reference)
		}
		if image.Archive == "" || path.Base(image.Archive) != image.Archive || !strings.HasSuffix(image.Archive, ".tar") {
			return manifest, fmt.Errorf("pinned image %q has an unsafe archive name %q", image.Reference, image.Archive)
		}
		if archives[image.Archive] {
			return manifest, fmt.Errorf("pinned image archive %q is duplicated", image.Archive)
		}
		archives[image.Archive] = true
	}
	return manifest, nil
}

// preparePinnedImageCache imports the seeded pinned-image generation on the
// fixture and proves every image is present under its exact reference before any
// Helm apply runs, so the applies never depend on a public registry. Missing or
// mismatched cache state fails the scenario instead of silently falling back.
func preparePinnedImageCache(t *testing.T, ip, keyPath, capacity string) {
	t.Helper()
	root := os.Getenv(pinnedImageCacheRootEnv)
	generation := os.Getenv(pinnedImageCacheGenerationEnv)
	if root == "" || generation == "" {
		t.Fatalf("pinned image cache identity is not configured (%s, %s); seed the fixture with the Fixture image cache workflow",
			pinnedImageCacheRootEnv, pinnedImageCacheGenerationEnv)
	}
	if !pinnedImageCacheGenerationPattern.MatchString(generation) {
		t.Fatalf("invalid pinned image cache generation %q", generation)
	}
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("ssh dial %s for pinned image cache: %v", ip, err)
	}
	defer client.Close()

	manifestPath := path.Join(root, capacity, generation, "generation.json")
	output, err := sshOutput(client, "sudo cat "+candidateShellQuote(manifestPath))
	if err != nil {
		t.Fatalf("read pinned image cache manifest %s: %v\n%s; seed the fixture with the Fixture image cache workflow", manifestPath, err, output)
	}
	manifest, err := parsePinnedImageCacheManifest(capacity, []byte(output))
	if err != nil {
		t.Fatalf("pinned image cache manifest %s: %v", manifestPath, err)
	}
	if manifest.Generation != generation || manifest.CacheRoot != root {
		t.Fatalf("pinned image cache identity mismatch: host generation=%s root=%s, expected generation=%s root=%s",
			manifest.Generation, manifest.CacheRoot, generation, root)
	}

	imported := 0
	for _, image := range manifest.Images {
		if _, err := sshOutput(client, "sudo k3s crictl inspecti "+candidateShellQuote(image.Reference)); err == nil {
			continue
		}
		archive := path.Join(root, capacity, generation, "images", image.Archive)
		if output, err := sshOutput(client, "sudo k3s ctr images import "+candidateShellQuote(archive)); err != nil {
			t.Fatalf("import pinned image %s from %s: %v\n%s", image.Reference, archive, err, output)
		}
		if output, err := sshOutput(client, "sudo k3s crictl inspecti "+candidateShellQuote(image.Reference)); err != nil {
			t.Fatalf("pinned image %s is absent after import from %s: %v\n%s", image.Reference, archive, err, output)
		}
		imported++
	}
	t.Logf("pinned image cache generation %s ready: %d images (%d imported this run)", generation, len(manifest.Images), imported)
}
