package e2e_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/kube"
)

// metalLBContinuitySample is one observation of the internal-ingress Service's
// LoadBalancer identity and route reachability captured during a blocking Helm
// operation, so continuity is observed continuously rather than only before and
// after.
type metalLBContinuitySample struct {
	At          time.Time
	VIP         string
	RouteOK     bool
	RouteStatus int
}

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
	// The DES-HOR-511 pre-apply + hook-object adoption run before the upgrade, and
	// the blocking Helm upgrade itself is observed continuously (VIP identity,
	// service LoadBalancer status, and route reachability) rather than sampled
	// only before and after.
	state.installMetalLBObserved(t, 15*time.Minute,
		[]string{state.writeValues(t, "metallb-current", metalLBTransitionValues(state))}, state.metalLB.InternalLBIP)
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
	state.installMetalLBObserved(t, 10*time.Minute,
		[]string{state.writeValues(t, "metallb-reapply", metalLBTransitionValues(state))}, before.InternalLBIP)
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
	// Capture the predecessor desired specs so the rollback boundary can prove
	// desired-state restoration. Ownership/hook metadata is NOT cached here: per
	// the founder correction (2026-08-21) it must be re-queried from the live
	// objects after the rollback completes.
	edgeSpec := metalLBPoolSpec(t, state, testRelease+"-edge")
	internalSpec := metalLBPoolSpec(t, state, testRelease+"-internal")

	// Inverse boundary: roll back the platform to the hook-era predecessor. The
	// pool/VIP objects are kept (helm.sh/resource-policy: keep) and Helm-adopted,
	// so they survive the rollback rather than being torn down by the 0.3.19 hooks.
	state.process(t, 10*time.Minute, "helm", "rollback", testRelease, "1",
		"--namespace", testNamespace, "--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "8m")

	// Prove predecessor-pool restoration: UIDs + desired specs are unchanged across
	// the hook-predecessor rollback.
	if got := metalLBPoolUID(t, state, testRelease+"-edge"); got != before.EdgePoolUID {
		t.Fatalf("rollback to hook predecessor changed edge pool UID: %s -> %s", before.EdgePoolUID, got)
	}
	if got := metalLBPoolSpec(t, state, testRelease+"-edge"); got != edgeSpec {
		t.Fatalf("rollback changed edge pool desired spec:\n%s\n!= predecessor\n%s", got, edgeSpec)
	}
	if got := metalLBPoolSpec(t, state, testRelease+"-internal"); got != internalSpec {
		t.Fatalf("rollback changed internal pool desired spec:\n%s\n!= predecessor\n%s", got, internalSpec)
	}
	// Re-query the LIVE objects after rollback and verify their ACTUAL ownership
	// and hook metadata (not values cached before rollback): ownership retained and
	// the transient hook markers absent.
	livePoolMeta := metalLBPoolMetadata(t, state, testRelease+"-edge")
	liveAdvertMeta := metalLBAdvertMetadata(t, state, testRelease+"-edge")
	if !strings.Contains(livePoolMeta, "meta.helm.sh/release-name") || !strings.Contains(livePoolMeta, `"helm.sh/resource-policy":"keep"`) {
		t.Fatalf("post-rollback edge pool ownership/keep metadata missing: %s", livePoolMeta)
	}
	if strings.Contains(livePoolMeta, "helm.sh/hook-") {
		t.Fatalf("post-rollback edge pool still carries hook metadata: %s", livePoolMeta)
	}
	if !strings.Contains(liveAdvertMeta, "meta.helm.sh/release-name") || strings.Contains(liveAdvertMeta, "helm.sh/hook-") {
		t.Fatalf("post-rollback edge advertisement ownership/hook metadata unexpected: %s", liveAdvertMeta)
	}
	if got := metalLBLBIP(t, state); got != before.InternalLBIP {
		t.Fatalf("rollback changed internal LoadBalancer VIP: %s -> %s", before.InternalLBIP, got)
	}
	assertMetalLBRoute(t, state, before.InternalLBIP)

	// Safe predecessor reapply / forward recovery: re-upgrade to the current chart
	// under continuous observation and re-assert every preserved signal.
	state.installMetalLBObserved(t, 15*time.Minute,
		[]string{state.writeValues(t, "metallb-forward", metalLBTransitionValues(state))}, before.InternalLBIP)
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

// installMetalLBObserved mirrors installPlatform's pre-apply + pure-hook
// adoption, then performs the blocking Helm upgrade in the background while the
// main goroutine continuously samples service LoadBalancer identity and route
// reachability throughout the operation (not merely before and after). It then
// asserts VIP identity never changed during the operation and that the route was
// observed reachable, and re-asserts the steady-state Fail policy.
func (state *chartState) installMetalLBObserved(t *testing.T, timeout time.Duration, valueFiles []string, expectedVIP string) {
	t.Helper()
	state.preapplyAllCRDs(t, valueFiles...)
	state.adoptMetalLBHookObjects(t)
	svc := testRelease + "-internal-ingress-nginx-controller"

	done := make(chan error, 1)
	go func() {
		_, err := state.client.HelmUpgrade(state.ctx, kube.HelmOptions{
			Release: testRelease, Namespace: testNamespace, Chart: state.platform,
			ValueFiles: valueFiles, Wait: true, Timeout: timeout,
		})
		done <- err
	}()
	// The DES-HOR-511 pre-apply + hook-object adoption run before the upgrade, and
	// the blocking Helm upgrade itself is observed continuously (VIP identity,
	// service LoadBalancer status, and route reachability) rather than sampled
	// only before and after. Every observation is fail-closed (founder decision
	// 2026-08-21): any kubectl read error, empty/changed VIP, or route failure
	// fails the operation; no failed/empty sample is skipped.
	samples, opErr := state.observeMetalLBContinuity(svc, expectedVIP, done, timeout)
	if opErr != nil {
		t.Fatalf("MetalLB continuity/upgrade failed during blocking operation: %v", opErr)
	}
	state.assertMetalLBContinuity(t, samples)
	if final := state.metalLBValidationPolicy(t); final != metalLBPolicyFail {
		t.Fatalf("metallb validation failurePolicy not converged to %s: got %q", metalLBPolicyFail, final)
	}
}

// observeMetalLBContinuity samples the internal-ingress Service's LoadBalancer IP
// and route reachability at a fixed cadence until the blocking upgrade finishes
// or the deadline expires. It is FAIL-CLOSED (founder decision 2026-08-21): any
// sample that hits a kubectl read error, an empty or changed VIP, or a route
// failure returns immediately with an error and does not skip the failed sample,
// so an operation can never be accepted because another sample succeeded.
func (state *chartState) observeMetalLBContinuity(svc, expectedVIP string, done <-chan error, max time.Duration) ([]metalLBContinuitySample, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.After(max)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	samples := []metalLBContinuitySample{}
	for {
		select {
		case upgradeErr := <-done:
			if upgradeErr != nil {
				return samples, fmt.Errorf("helm upgrade failed: %w", upgradeErr)
			}
			return samples, nil
		case <-deadline:
			return samples, fmt.Errorf("metalLB continuity observation timed out before upgrade completed")
		case <-ticker.C:
			sample := metalLBContinuitySample{At: time.Now()}
			vip, err := state.client.Kubectl(state.ctx, 30*time.Second, "get", "service", svc, "-n", testNamespace,
				"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
			if err != nil {
				return samples, fmt.Errorf("kubectl read of %s failed during operation: %v", svc, err)
			}
			sample.VIP = strings.TrimSpace(vip)
			if sample.VIP == "" {
				return samples, fmt.Errorf("LoadBalancer VIP became empty during operation")
			}
			if sample.VIP != expectedVIP {
				return samples, fmt.Errorf("LoadBalancer VIP changed during operation: %s -> %s", expectedVIP, sample.VIP)
			}
			resp, err := client.Get("http://" + sample.VIP)
			if err != nil {
				return samples, fmt.Errorf("route lost during operation at %s: %v", sample.VIP, err)
			}
			sample.RouteOK = resp.StatusCode < 500
			sample.RouteStatus = resp.StatusCode
			resp.Body.Close()
			if !sample.RouteOK {
				return samples, fmt.Errorf("route unreachable during operation at %s: %s", sample.VIP, resp.Status)
			}
			samples = append(samples, sample)
		}
	}
}

// assertMetalLBContinuity verifies a fail-closed observation actually sampled the
// Live LoadBalancer identity throughout the operation (violations already fail
// inside observeMetalLBContinuity), so the continuity claim is backed by at least
// one healthy continuous observation rather than a before/after-only sample.
func (state *chartState) assertMetalLBContinuity(t *testing.T, samples []metalLBContinuitySample) {
	t.Helper()
	if len(samples) == 0 {
		t.Fatal("no MetalLB continuity samples observed during the blocking operation")
	}
}

func metalLBPoolSpec(t *testing.T, state *chartState, name string) string {
	t.Helper()
	return state.kubectl(t, 30*time.Second, "get", "ipaddresspool", name, "-n", testNamespace,
		"-o", "jsonpath={.spec}")
}

func metalLBPoolMetadata(t *testing.T, state *chartState, name string) string {
	t.Helper()
	return state.kubectl(t, 30*time.Second, "get", "ipaddresspool", name, "-n", testNamespace,
		"-o", "jsonpath={.metadata.annotations}")
}

func metalLBAdvertMetadata(t *testing.T, state *chartState, name string) string {
	t.Helper()
	return state.kubectl(t, 30*time.Second, "get", "l2advertisement", name, "-n", testNamespace,
		"-o", "jsonpath={.metadata.annotations}")
}

// metalLBSnapshot holds the observable MetalLB identity signals captured before
// a mutation and asserted after, so preservation is deterministic.
type metalLBSnapshot struct {
	EdgePoolUID     string
	InternalPoolUID string
	InternalLBIP    string
	AdvertUID       string
}
