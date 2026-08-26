package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nunocgoncalves/iterabase-mono/forge/test/e2e/internal/remotecluster"
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
	markers := []string{"action:     skip", "node ready: true", "certificate substrate applied: true",
		"chart applied: true", "overlay applied: true", "overlay commit:", "flux installed: true", "gitrepository: ready=True"}
	if state.managedRWX {
		markers = append(markers, "rwx storage mode: managed-longhorn", "rwx storage prerequisites ready: true", "rwx storage substrate applied: true")
	} else {
		markers = append(markers, "rwx storage mode: external")
	}
	assertApplyMarkers(t, out, markers...)
	t.Logf("apply output:\n%s", out)
	candidateCluster := remotecluster.Use(t, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
	assertCandidateImageDigests(t, candidateCluster, "iterabase-system",
		controlPlaneDigestEnv, inferenceGatewayDigestEnv, toolRunnerDigestEnv)
	if state.managedRWX {
		assertManagedLonghornInternalTLS(t, candidateCluster, state.runID)
	}

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

func assertManagedLonghornInternalTLS(t *testing.T, cluster *remotecluster.Cluster, platformRelease string) {
	t.Helper()
	const storageNamespace = "longhorn-system"
	issuer := strings.TrimSpace(cluster.Kubectl(t, "get", "certificate/longhorn-grpc-tls", "-n", storageNamespace, "-o", "jsonpath={.spec.issuerRef.name}"))
	if issuer != "internal-ca" {
		t.Fatalf("Longhorn gRPC leaf issuer=%q want internal-ca", issuer)
	}
	rootCA := strings.TrimSpace(cluster.Kubectl(t, "get", "secret/"+platformRelease+"-internal-ca-root", "-n", "iterabase-system", "-o", `jsonpath={.data.ca\.crt}`))
	leafCA := strings.TrimSpace(cluster.Kubectl(t, "get", "secret/longhorn-grpc-tls", "-n", storageNamespace, "-o", `jsonpath={.data.ca\.crt}`))
	if rootCA == "" || leafCA != rootCA {
		t.Fatal("Longhorn gRPC leaf is not chained to the platform internal CA")
	}

	attestation := strings.TrimSpace(cluster.Kubectl(t, "get", "configmap", "-n", storageNamespace,
		"-l", "platform.iterabase.com/evidence=longhorn-grpc-mtls", "-o",
		`jsonpath={.items[0].data.result}{"|"}{.items[0].data.authenticatedServices}{"|"}{.items[0].data.unauthenticatedTLSRejected}{"|"}{.items[0].data.plaintextRejected}`))
	parts := strings.Split(attestation, "|")
	if len(parts) != 4 || parts[0] != "pass" || parts[1] == "0" || parts[1] != parts[2] || parts[1] != parts[3] {
		t.Fatalf("Longhorn gRPC mTLS rejection attestation=%q", attestation)
	}

	pods := strings.Fields(cluster.Kubectl(t, "get", "pods", "-n", storageNamespace,
		"-l", "longhorn.io/component=instance-manager", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`))
	if len(pods) == 0 {
		t.Fatal("no current Longhorn instance-manager pod")
	}
	for _, pod := range pods {
		logs := cluster.PodLogs(t, storageNamespace, pod, "")
		if !strings.Contains(logs, "Creating gRPC server with mtls auth") ||
			strings.Contains(logs, "Creating gRPC server with no auth") ||
			strings.Contains(logs, "starting without TLS") {
			t.Fatalf("instance-manager %s does not prove mTLS-only startup", pod)
		}
	}
	t.Logf("verified internal-CA Longhorn gRPC mTLS and %s authenticated/plaintext rejection probes", parts[1])
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
