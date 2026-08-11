package e2e

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
)

const (
	candidateOverlayRepository = "https://github.com/nunocgoncalves/iterabase-overlay.git"
	candidateOverlayRef        = "e2e"
)

// prepareCandidateOverlay keeps real-machine release validation on Forge's
// normal overlay path while replacing selected images before the candidate
// chart is first installed. The temporary host-local repository is based on
// the public E2E overlay and exists only for the lifetime of the droplet.
func prepareCandidateOverlay(t *testing.T, runID, ip, keyPath string) (string, string) {
	t.Helper()
	values := candidateOverlayValues(t)
	if values == "" {
		return candidateOverlayRepository, candidateOverlayRef
	}

	directory := "/tmp/iterabase-release-overlay-" + runID
	encoded := base64.StdEncoding.EncodeToString([]byte(values))
	script := fmt.Sprintf(`set -eu
dir=%s
rm -rf "$dir"
git clone --quiet --branch %s --single-branch %s "$dir"
printf '%%s' %s | base64 --decode >> "$dir/values.client.yaml"
cd "$dir"
git add values.client.yaml
git -c user.email=release@iterabase.local -c user.name='Iterabase release validation' commit -qm 'inject exact release candidates'
`, candidateShellQuote(directory), candidateShellQuote(candidateOverlayRef),
		candidateShellQuote(candidateOverlayRepository), candidateShellQuote(encoded))

	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial candidate host to prepare overlay: %v", err)
	}
	defer client.Close()
	if output, err := sshOutput(client, script); err != nil {
		t.Fatalf("prepare exact-candidate overlay: %v\n%s", err, output)
	}
	t.Log("prepared ephemeral overlay with selected immutable image identities")
	return "file://" + directory, candidateOverlayRef
}

func candidateOverlayValues(t *testing.T) string {
	t.Helper()
	imageValues := func(repositoryEnv, tagEnv, digestEnv, prefix string) string {
		digest := os.Getenv(digestEnv)
		if digest == "" {
			return ""
		}
		if !strings.HasPrefix(digest, "sha256:") {
			t.Fatalf("%s=%q is not a canonical sha256 digest", digestEnv, digest)
		}
		repository := os.Getenv(repositoryEnv)
		tag := os.Getenv(tagEnv)
		if repository == "" || tag == "" || !strings.HasSuffix(tag, "@"+digest) {
			t.Fatalf("%s requires repository and immutable tag ending in @%s", digestEnv, digest)
		}
		return fmt.Sprintf("%srepository: %q\n%stag: %q\n", prefix, repository, prefix, tag)
	}

	controlPlane := imageValues(
		"CONTROL_PLANE_IMAGE_REPO", "CONTROL_PLANE_IMAGE_TAG", controlPlaneDigestEnv, "    ",
	)
	toolRunner := imageValues(
		"TOOL_RUNNER_IMAGE_REPO", "TOOL_RUNNER_IMAGE_TAG", toolRunnerDigestEnv, "      ",
	)
	inference := imageValues(
		"INFERENCE_GATEWAY_IMAGE_REPO", "INFERENCE_GATEWAY_IMAGE_TAG", inferenceGatewayDigestEnv, "    ",
	)

	var values strings.Builder
	if controlPlane != "" || toolRunner != "" {
		values.WriteString("\n# Exact run-addressed release candidates.\ncontrol-plane:\n")
		if controlPlane != "" {
			values.WriteString("  image:\n")
			values.WriteString(controlPlane)
		}
		if toolRunner != "" {
			values.WriteString("  toolRunner:\n    image:\n")
			values.WriteString(toolRunner)
		}
	}
	if inference != "" {
		values.WriteString("inference-gateway:\n  image:\n")
		values.WriteString(inference)
	}
	return values.String()
}

func TestCandidateOverlayValues(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	t.Setenv("CONTROL_PLANE_IMAGE_REPO", "ghcr.io/example/control-plane")
	t.Setenv("CONTROL_PLANE_IMAGE_TAG", "candidate-run@"+digest)
	t.Setenv(controlPlaneDigestEnv, digest)
	t.Setenv("TOOL_RUNNER_IMAGE_REPO", "")
	t.Setenv("TOOL_RUNNER_IMAGE_TAG", "")
	t.Setenv(toolRunnerDigestEnv, "")
	t.Setenv("INFERENCE_GATEWAY_IMAGE_REPO", "")
	t.Setenv("INFERENCE_GATEWAY_IMAGE_TAG", "")
	t.Setenv(inferenceGatewayDigestEnv, "")

	values := candidateOverlayValues(t)
	for expected := range map[string]struct{}{
		"control-plane:": {},
		"repository: \"ghcr.io/example/control-plane\"": {},
		"tag: \"candidate-run@" + digest + "\"":         {},
	} {
		if !strings.Contains(values, expected) {
			t.Fatalf("candidate values missing %q:\n%s", expected, values)
		}
	}
}
