package e2e

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	candidateOverlayRepository = "https://github.com/nunocgoncalves/iterabase-overlay.git"
	candidateOverlayRef        = "e2e"
)

type candidateOverlayPlan struct {
	repository string
	ref        string
	flux       bool
	values     string
}

func candidateOverlayPlanForEnvironment(t *testing.T) candidateOverlayPlan {
	t.Helper()
	return candidateOverlayPlan{
		repository: candidateOverlayRepository,
		ref:        candidateOverlayRef,
		flux:       true,
		values:     candidateOverlayValues(t),
	}
}

func candidateOverlaySetupScript(values, prefix string) string {
	valuesPath := prefix + "-values.yaml"
	filterPath := prefix + "-smudge"
	attributesPath := prefix + "-attributes"
	encoded := base64.StdEncoding.EncodeToString([]byte(values))
	return fmt.Sprintf(`set -eu
if ! command -v git >/dev/null 2>&1; then
  sudo apt-get update -qq
  sudo apt-get install -y git
fi
printf '%%s' %s | base64 --decode > %s
cat > %s <<'FILTER'
#!/bin/sh
cat
printf '\n'
cat %s
FILTER
chmod 700 %s
printf 'values.client.yaml filter=iterabase-release-candidates\n' > %s
git config --global core.attributesFile %s
git config --global filter.iterabase-release-candidates.clean cat
git config --global filter.iterabase-release-candidates.smudge %s
git config --global filter.iterabase-release-candidates.required true
`, candidateShellQuote(encoded), candidateShellQuote(valuesPath), candidateShellQuote(filterPath),
		candidateShellQuote(valuesPath), candidateShellQuote(filterPath), candidateShellQuote(attributesPath),
		candidateShellQuote(attributesPath), candidateShellQuote(filterPath))
}

// prepareCandidateOverlay keeps the public overlay commit as both Forge's
// resolved source and Flux's exact artifact identity. For selected images, a
// host-local Git smudge filter appends run-addressed values only to Forge's
// checkout. The public source commit therefore still matches the Flux artifact
// while Helm sees the candidate overrides on its first install.
func prepareCandidateOverlay(t *testing.T, runID, ip, keyPath string) candidateOverlayPlan {
	t.Helper()
	plan := candidateOverlayPlanForEnvironment(t)
	if plan.values == "" {
		return plan
	}

	prefix := "/tmp/iterabase-release-overlay-" + runID
	valuesPath := prefix + "-values.yaml"
	filterPath := prefix + "-smudge"
	attributesPath := prefix + "-attributes"
	script := candidateOverlaySetupScript(plan.values, prefix)

	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial candidate host to prepare overlay values: %v", err)
	}
	if output, err := sshOutput(client, script); err != nil {
		client.Close()
		t.Fatalf("prepare exact-candidate overlay values: %v\n%s", err, output)
	}
	client.Close()
	t.Cleanup(func() {
		cleanupClient, err := sshDial(ip, keyPath)
		if err != nil {
			t.Logf("remove candidate overlay values: dial host: %v", err)
			return
		}
		defer cleanupClient.Close()
		cleanup := fmt.Sprintf(
			"git config --global --unset-all core.attributesFile || true; "+
				"git config --global --remove-section filter.iterabase-release-candidates || true; "+
				"rm -f %s %s %s",
			candidateShellQuote(valuesPath), candidateShellQuote(filterPath), candidateShellQuote(attributesPath),
		)
		if output, err := sshOutput(cleanupClient, cleanup); err != nil {
			t.Logf("remove candidate overlay values: %v\n%s", err, output)
		}
	})
	t.Log("prepared exact-source overlay checkout with selected immutable image identities")
	return plan
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

	// These real-machine scenarios prove Forge/bootstrap and exact candidate
	// installation. Portable dispatch behavior belongs to control-plane E2E and
	// requires a customer-specific default model, so keep it out of this fixture.
	releaseCandidate := os.Getenv("FORGE_E2E_RELEASE_CANDIDATE") == "true"

	var values strings.Builder
	if releaseCandidate || controlPlane != "" || toolRunner != "" {
		values.WriteString("\n# Exact run-addressed release candidates.\ncontrol-plane:\n")
		if releaseCandidate {
			values.WriteString("  dispatch:\n    enabled: false\n")
		}
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
	t.Setenv("FORGE_E2E_RELEASE_CANDIDATE", "true")
	t.Setenv("FORGE_E2E_CHART_REPOSITORY", "oci://ghcr.io/example/candidates/platform")
	t.Setenv("CONTROL_PLANE_IMAGE_REPO", "ghcr.io/example/control-plane")
	t.Setenv("CONTROL_PLANE_IMAGE_TAG", "candidate-run@"+digest)
	t.Setenv(controlPlaneDigestEnv, digest)
	t.Setenv("TOOL_RUNNER_IMAGE_REPO", "")
	t.Setenv("TOOL_RUNNER_IMAGE_TAG", "")
	t.Setenv(toolRunnerDigestEnv, "")
	t.Setenv("INFERENCE_GATEWAY_IMAGE_REPO", "")
	t.Setenv("INFERENCE_GATEWAY_IMAGE_TAG", "")
	t.Setenv(inferenceGatewayDigestEnv, "")

	plan := candidateOverlayPlanForEnvironment(t)
	if plan.repository != candidateOverlayRepository || plan.ref != candidateOverlayRef || !plan.flux {
		t.Fatalf("candidate plan must retain exact public Flux source: %+v", plan)
	}
	for expected := range map[string]struct{}{
		"control-plane:": {},
		"dispatch:":      {},
		"enabled: false": {},
		"repository: \"ghcr.io/example/control-plane\"": {},
		"tag: \"candidate-run@" + digest + "\"":         {},
	} {
		if !strings.Contains(plan.values, expected) {
			t.Fatalf("candidate values missing %q:\n%s", expected, plan.values)
		}
	}
}

func TestCandidateOverlayValuesForChartOnly(t *testing.T) {
	t.Setenv("FORGE_E2E_RELEASE_CANDIDATE", "true")
	t.Setenv("FORGE_E2E_CHART_REPOSITORY", "oci://ghcr.io/example/candidates/platform")
	t.Setenv(controlPlaneDigestEnv, "")
	t.Setenv(toolRunnerDigestEnv, "")
	t.Setenv(inferenceGatewayDigestEnv, "")

	values := candidateOverlayValues(t)
	if !strings.Contains(values, "control-plane:\n  dispatch:\n    enabled: false") {
		t.Fatalf("candidate chart fixture must disable customer-specific dispatch:\n%s", values)
	}
}

func TestCandidateOverlayCheckoutKeepsExactSourceCommit(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	t.Setenv("CONTROL_PLANE_IMAGE_REPO", "ghcr.io/example/control-plane")
	t.Setenv("CONTROL_PLANE_IMAGE_TAG", "candidate-run@"+digest)
	t.Setenv(controlPlaneDigestEnv, digest)
	t.Setenv(toolRunnerDigestEnv, "")
	t.Setenv(inferenceGatewayDigestEnv, "")
	plan := candidateOverlayPlanForEnvironment(t)
	if plan.values == "" {
		t.Fatal("non-empty candidate environment produced no overlay values")
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	source := filepath.Join(root, "source")
	checkout := filepath.Join(root, "checkout")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "crds", "client"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		"values.yaml":                    "base: true\n",
		"values.client.yaml":             "# fixture\n",
		"crds/client/kustomization.yaml": "resources: []\n",
	} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit(source, "init", "-q", "-b", candidateOverlayRef)
	runGit(source, "add", ".")
	runGit(source, "-c", "user.email=e2e@example.com", "-c", "user.name=E2E", "commit", "-qm", "fixture")
	sourceCommit := runGit(source, "rev-parse", "HEAD")

	setup := exec.Command("bash", "-c", candidateOverlaySetupScript(plan.values, filepath.Join(root, "candidate")))
	setup.Env = append(os.Environ(), "HOME="+home)
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("prepare candidate checkout filter: %v\n%s", err, out)
	}
	runGit(root, "clone", "-q", "--branch", candidateOverlayRef, source, checkout)
	if checkoutCommit := runGit(checkout, "rev-parse", "HEAD"); checkoutCommit != sourceCommit {
		t.Fatalf("candidate checkout commit = %s, want exact source %s", checkoutCommit, sourceCommit)
	}
	contents, err := os.ReadFile(filepath.Join(checkout, "values.client.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), plan.values) {
		t.Fatalf("candidate checkout did not receive candidate values:\n%s", contents)
	}
}
