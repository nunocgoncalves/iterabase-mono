package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nunocgoncalves/iterabase-mono/forge/test/e2e/internal/kindtest"
)

// runOverlayStage upgrades the composed CPU fixture from the migration source
// to the current platform through Forge's production ordering: clone the public
// fixture, install the certificate substrate, establish an exact Flux artifact,
// migrate certificate ownership, apply the platform, then reconcile its CRs.
//
// It points at ref `e2e` (a minimal-scaffold test-fixture branch): `master` holds
// the HOR-299 bare-metal prod recipe (required placeholders, not deployable bare
// on a cloud VM). The prod recipe's deployability is HOR-299's job; this test is
// forge's mechanics. See iterabase-overlay `e2e` branch.
//
// FORGE_OVERLAY_TOKEN is intentionally unset (public repo, CI non-interactive);
// the token-prompt path is covered by unit + fake-SSH tests.
func runOverlayStage(t *testing.T, state *digitalOceanCPUState) {
	if _, ok := os.LookupEnv("FORGE_OVERLAY_TOKEN"); ok {
		t.Fatal("FORGE_OVERLAY_TOKEN must be unset for this test (public repo, tokenless)")
	}
	prepareCandidateChart(t, state.ip, state.privKeyPath)
	loginCandidateRegistry(t, state.ip, state.privKeyPath)
	plan := prepareCandidateOverlay(t, state.runID, state.ip, state.privKeyPath)
	candidateConfig := writeCurrentOverlayForgeConfig(
		t, state.runID, state.ip, state.privKeyPath, state.chartVersion, plan,
	)
	out := applyOnce(t, state.forgeBin, state.forgeHome, candidateConfig)
	assertApplyMarkers(t, out, "action:     skip", "node ready: true", "certificate substrate applied: true",
		"chart applied: true", "overlay applied: true", "overlay commit:", "flux installed: true", "gitrepository: ready=True")
	t.Logf("apply output:\n%s", out)
	candidateCluster := kindtest.UseCluster(t, state.runID, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
	assertCandidateImageDigests(t, candidateCluster, "iterabase-system",
		controlPlaneDigestEnv, inferenceGatewayDigestEnv, toolRunnerDigestEnv)

	// The cloned overlay dir exists on the host (a real clone happened).
	overlayDir := "/var/lib/forge/overlay/" + state.runID
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	if _, err := sshOutput(sc, "test -d "+overlayDir+"/.git && test -f "+overlayDir+"/values.yaml"); err != nil {
		t.Fatalf("overlay clone not present on host at %s: %v", overlayDir, err)
	}
}

// writeCurrentOverlayForgeConfig uses the public exact-Flux fixture. Candidate
// image values affect only Forge's checkout and never change this source identity.
func writeCurrentOverlayForgeConfig(
	t *testing.T, name, ip, keyPath, chartVersion string, plan candidateOverlayPlan,
) string {
	return writeForgeConfigSpec(t, forgeConfigSpec{
		Name: name, Address: ip, SSHKeyPath: keyPath, RunLabel: true, DualStack: true,
		ChartVersion:    chartVersion,
		ChartRepository: os.Getenv("FORGE_E2E_CHART_REPOSITORY"),
		OverlayRepo:     plan.repository,
		OverlayRef:      plan.ref,
		Flux:            plan.flux,
	})
}
