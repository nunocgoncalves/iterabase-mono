package e2e_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

func metalLBTransitionScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: transitionScenarioMetadata(
			"metallb-upgrade-reapply",
			"Installs the checksum-pinned hook-era predecessor 0.3.19 (recorded in transition-baselines.json) on MetalLB L2, upgrades to the current ordinary-resource chart, and proves pool/advertisement UID preservation, LoadBalancer VIP continuity, controller health, idempotent reapply, and the hook-predecessor rollback/forward boundary.",
			"test-e2e-metallb-transition", 55,
			[]string{"HOR-511"}, []string{"iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "resolve-predecessor-baselines", DependsOn: []string{"create-kind"}, Run: resolveTransitionBaselinesStage},
			{Name: "install-metallb-predecessor-substrate", DependsOn: []string{"resolve-predecessor-baselines"}, Run: installMetalLBPredecessorSubstrateStage},
			{Name: "install-metallb-predecessor", DependsOn: []string{"install-metallb-predecessor-substrate"}, Run: installMetalLBPredecessorStage},
			{Name: "record-pre-upgrade-signals", DependsOn: []string{"install-metallb-predecessor"}, Run: recordMetalLBPreUpgradeSignalsStage},
			{Name: "upgrade-current-metallb", DependsOn: []string{"record-pre-upgrade-signals"}, Run: upgradeCurrentMetalLBStage},
			{Name: "assert-metallb-preserved", DependsOn: []string{"upgrade-current-metallb"}, Run: assertMetalLBSignalsPreservedStage},
			{Name: "reapply-metallb", DependsOn: []string{"assert-metallb-preserved"}, Run: reapplyMetalLBStage},
			{Name: "predecessor-rollback-forward", DependsOn: []string{"reapply-metallb"}, Run: metalLBPredecessorRollbackForwardStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func resolveMetalLBSubnet(t *testing.T, state *chartState) {
	t.Helper()
	inspect := state.process(t, 30*time.Second, "docker", "network", "inspect", "kind", "-f", `{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}`)
	var subnet string
	for _, candidate := range strings.Fields(inspect) {
		if strings.Contains(candidate, ".") {
			subnet = strings.SplitN(candidate, "/", 2)[0]
			break
		}
	}
	parts := strings.Split(subnet, ".")
	if len(parts) != 4 {
		t.Fatalf("Kind network has no IPv4 subnet: %q", inspect)
	}
	state.internalPool = fmt.Sprintf("%s.%s.255.200-%s.%s.255.220", parts[0], parts[1], parts[0], parts[1])
	state.internalIngressIP = fmt.Sprintf("%s.%s.255.110", parts[0], parts[1])
	state.internalPoolInternal = fmt.Sprintf("%s.%s.255.100-%s.%s.255.120", parts[0], parts[1], parts[0], parts[1])
}

func metalLBTransitionValues(state *chartState) map[string]any {
	values := basePlatformValues()
	values["metallb"] = map[string]any{"enabled": true}
	values["metallb-config"] = map[string]any{
		"enabled":   true,
		"addresses": []string{state.internalPool},
		"additionalPools": []any{map[string]any{
			"name": "internal", "addresses": []string{state.internalPoolInternal}, "autoAssign": false,
		}},
	}
	values["internal-ingress-nginx"] = map[string]any{
		"enabled": true,
		"controller": map[string]any{"service": map[string]any{"annotations": map[string]any{
			"metallb.io/address-pool":    testRelease + "-internal",
			"metallb.io/loadBalancerIPs": state.internalIngressIP,
		}}},
	}
	return values
}

func installMetalLBPredecessorSubstrateStage(t *testing.T, state *chartState) {
	t.Helper()
	baseline := requireTransitionBaseline(t, state, metalLBSubstratePredecessorName)
	args := []string{"upgrade", "--install", testRelease + "-cert-manager", "--namespace", testNamespace,
		"--create-namespace", "--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "8m", baseline.Archive}
	state.process(t, 10*time.Minute, "helm", args...)
	assertReleaseChartVersion(t, state, testRelease+"-cert-manager", baseline.Chart, baseline.Version)
}

func installMetalLBPredecessorStage(t *testing.T, state *chartState) {
	t.Helper()
	resolveMetalLBSubnet(t, state)
	baseline := requireTransitionBaseline(t, state, metalLBPlatformPredecessorName)
	values := state.writeValues(t, "metallb-predecessor", metalLBTransitionValues(state))
	args := []string{"upgrade", "--install", testRelease, "--namespace", testNamespace,
		"--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "18m",
		"--values", values, baseline.Archive}
	state.process(t, 20*time.Minute, "helm", args...)
	assertReleaseChartVersion(t, state, testRelease, baseline.Chart, baseline.Version)
}

func recordMetalLBPreUpgradeSignalsStage(t *testing.T, state *chartState) {
	t.Helper()
	state.metalLB = &metalLBSnapshot{
		EdgePoolUID:     metalLBPoolUID(t, state, testRelease+"-edge"),
		InternalPoolUID: metalLBPoolUID(t, state, testRelease+"-internal"),
		InternalLBIP:    metalLBLBIP(t, state),
		AdvertUID:       metalLBAdvertUID(t, state, testRelease+"-edge"),
	}
	if state.metalLB.EdgePoolUID == "" || state.metalLB.InternalPoolUID == "" {
		t.Fatalf("hook-era predecessor did not create expected MetalLB pools")
	}
}

func upgradeCurrentMetalLBStage(t *testing.T, state *chartState) {
	t.Helper()
	// installPlatform runs the DES-HOR-511 pre-apply, adoptMetalLBHookObjects,
	// and the bounded bootstrap, then Helm-upgrades to the current chart.
	state.installPlatform(t, 15*time.Minute, state.writeValues(t, "metallb-current", metalLBTransitionValues(state)))
}

func assertMetalLBSignalsPreservedStage(t *testing.T, state *chartState) {
	t.Helper()
	before := state.metalLB
	if got := metalLBPoolUID(t, state, testRelease+"-edge"); got != before.EdgePoolUID {
		t.Fatalf("edge pool UID changed across upgrade: %s -> %s", before.EdgePoolUID, got)
	}
	if got := metalLBPoolUID(t, state, testRelease+"-internal"); got != before.InternalPoolUID {
		t.Fatalf("internal pool UID changed across upgrade: %s -> %s", before.InternalPoolUID, got)
	}
	if got := metalLBLBIP(t, state); got != before.InternalLBIP {
		t.Fatalf("internal LoadBalancer VIP changed across upgrade: %s -> %s", before.InternalLBIP, got)
	}
	if got := metalLBAdvertUID(t, state, testRelease+"-edge"); got != before.AdvertUID {
		t.Fatalf("l2advertisement UID changed across upgrade: %s -> %s", before.AdvertUID, got)
	}
	assertMetalLBControllerHealth(t, state)
	if before.InternalLBIP != "" {
		assertMetalLBRoute(t, state, before.InternalLBIP)
	}
}

func reapplyMetalLBStage(t *testing.T, state *chartState) {
	t.Helper()
	before := state.metalLB
	state.installPlatform(t, 10*time.Minute, state.writeValues(t, "metallb-reapply", metalLBTransitionValues(state)))
	if got := metalLBPoolUID(t, state, testRelease+"-edge"); got != before.EdgePoolUID {
		t.Fatalf("reapply changed edge pool UID: %s -> %s", before.EdgePoolUID, got)
	}
	if got := metalLBLBIP(t, state); got != before.InternalLBIP {
		t.Fatalf("reapply changed internal LoadBalancer VIP: %s -> %s", before.InternalLBIP, got)
	}
}

func metalLBPredecessorRollbackForwardStage(t *testing.T, state *chartState) {
	t.Helper()
	before := state.metalLB
	// Inverse boundary: roll back the platform to the hook-era predecessor. The
	// pool/VIP objects are kept resources, so they survive the rollback.
	state.process(t, 10*time.Minute, "helm", "rollback", testRelease, "1",
		"--namespace", testNamespace, "--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "8m")
	if got := metalLBPoolUID(t, state, testRelease+"-edge"); got != before.EdgePoolUID {
		t.Fatalf("rollback to hook predecessor changed edge pool UID: %s -> %s", before.EdgePoolUID, got)
	}
	// Forward recovery: re-upgrade to current and assert the signals once more.
	state.installPlatform(t, 15*time.Minute, state.writeValues(t, "metallb-forward", metalLBTransitionValues(state)))
	assertMetalLBSignalsPreservedStage(t, state)
}

func metalLBPoolUID(t *testing.T, state *chartState, name string) string {
	t.Helper()
	return state.kubectl(t, 30*time.Second, "get", "ipaddresspool", name, "-n", testNamespace,
		"-o", "jsonpath={.metadata.uid}")
}

func metalLBAdvertUID(t *testing.T, state *chartState, name string) string {
	t.Helper()
	return state.kubectl(t, 30*time.Second, "get", "l2advertisement", name, "-n", testNamespace,
		"-o", "jsonpath={.metadata.uid}")
}

func metalLBLBIP(t *testing.T, state *chartState) string {
	t.Helper()
	svc := testRelease + "-internal-ingress-nginx-controller"
	return state.kubectl(t, 30*time.Second, "get", "service", svc, "-n", testNamespace,
		"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
}

func assertMetalLBControllerHealth(t *testing.T, state *chartState) {
	t.Helper()
	name := testRelease + "-metallb-controller"
	if got := state.kubectl(t, 30*time.Second, "get", "deployment", name, "-n", testNamespace,
		"-o", "jsonpath={.status.readyReplicas}"); got != "1" {
		t.Fatalf("metallb controller ready replicas=%q want=1", got)
	}
	logs := state.kubectl(t, 30*time.Second, "logs", "deployment/"+name, "-n", testNamespace, "--tail=200")
	if strings.Contains(strings.ToLower(logs), "level=error") {
		t.Fatalf("metallb controller logged errors:\n%s", logs)
	}
}

func assertMetalLBRoute(t *testing.T, state *chartState, ip string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + ip)
	if err != nil {
		t.Fatalf("internal LoadBalancer route is not reachable at %s: %v", ip, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Fatalf("internal LoadBalancer route returned %s at %s", resp.Status, ip)
	}
}

// metalLBSnapshot holds the observable MetalLB identity signals captured before
// a mutation and asserted after, so preservation is deterministic.
type metalLBSnapshot struct {
	EdgePoolUID     string
	InternalPoolUID string
	InternalLBIP    string
	AdvertUID       string
}
