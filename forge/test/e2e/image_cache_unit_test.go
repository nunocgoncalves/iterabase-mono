package e2e

import (
	"strings"
	"testing"
)

func TestPinnedImageCacheManifestParsesTheSeededContract(t *testing.T) {
	valid := `{
		"schema_version": 1,
		"capacity": "cpu",
		"generation": "` + strings.Repeat("a", 64) + `",
		"cache_root": "/var/lib/iterabase-e2e/image-cache",
		"images": [
			{"reference": "ghcr.io/nunocgoncalves/iterabase-third-party/minio:RELEASE.2025-09-07T16-13-09Z@sha256:786c852164a4fab14fd194fdbe6b4ed6f34934fcf6e4556aa9368432081719e9", "digest": "sha256:` + strings.Repeat("b", 64) + `", "config_digest": "sha256:` + strings.Repeat("c", 64) + `", "archive": "ghcr.io_nunocgoncalves_iterabase-third-party_minio-abc.tar"}
		]
	}`
	manifest, err := parsePinnedImageCacheManifest("cpu", []byte(valid))
	if err != nil {
		t.Fatalf("valid manifest was rejected: %v", err)
	}
	if manifest.Generation != strings.Repeat("a", 64) || len(manifest.Images) != 1 {
		t.Fatalf("manifest was not parsed exactly: %+v", manifest)
	}
}

func TestPinnedImageCacheManifestRejectsInvalidContracts(t *testing.T) {
	generation := strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	cases := map[string]string{
		"unsupported schema": `{"schema_version":2,"capacity":"cpu","generation":"` + generation + `","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"` + digest + `","archive":"busybox.tar"}]}`,
		"wrong capacity":     `{"schema_version":1,"capacity":"gpu","generation":"` + generation + `","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"` + digest + `","archive":"busybox.tar"}]}`,
		"bad generation":     `{"schema_version":1,"capacity":"cpu","generation":"short","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"` + digest + `","archive":"busybox.tar"}]}`,
		"no images":          `{"schema_version":1,"capacity":"cpu","generation":"` + generation + `","cache_root":"/cache","images":[]}`,
		"bad digest":         `{"schema_version":1,"capacity":"cpu","generation":"` + generation + `","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"sha256:xyz","archive":"busybox.tar"}]}`,
		"bad config digest":  `{"schema_version":1,"capacity":"cpu","generation":"` + generation + `","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"` + digest + `","config_digest":"sha256:xyz","archive":"busybox.tar"}]}`,
		"path traversal":     `{"schema_version":1,"capacity":"cpu","generation":"` + generation + `","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"` + digest + `","archive":"../busybox.tar"}]}`,
		"duplicate archive":  `{"schema_version":1,"capacity":"cpu","generation":"` + generation + `","cache_root":"/cache","images":[{"reference":"busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0","digest":"` + digest + `","archive":"busybox.tar"},{"reference":"debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132","digest":"` + digest + `","archive":"busybox.tar"}]}`,
	}
	for name, payload := range cases {
		if _, err := parsePinnedImageCacheManifest("cpu", []byte(payload)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
