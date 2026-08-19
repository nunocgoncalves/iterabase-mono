package e2e_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/httpx"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/kube"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"gopkg.in/yaml.v3"
)

const (
	platformPredecessorName  = "supported-platform-predecessor"
	substratePredecessorName = "supported-substrate-predecessor"
	transitionFieldManager   = "iterabase-chart-e2e"
	transitionMarker         = "hor-475-persisted-state"
)

var operatorCRDs = []string{
	"alertmanagers.monitoring.coreos.com",
	"podmonitors.monitoring.coreos.com",
	"prometheuses.monitoring.coreos.com",
	"prometheusrules.monitoring.coreos.com",
	"servicemonitors.monitoring.coreos.com",
}

type transitionBaseline struct {
	Name       string
	Chart      string
	Repository string
	Version    string
	Checksum   string
	Archive    string
}

type bundledCRDHeader struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
}

type bundledCRD struct {
	header   bundledCRDHeader
	manifest string
}

type lifecycleSnapshot struct {
	Secrets map[string]string
	PVCs    map[string]string
	Pods    map[string]string
}

type helmHistoryEntry struct {
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Chart    string `json:"chart"`
}

func nMinusOneUpgradeScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: transitionScenarioMetadata(
			"n-minus-one-upgrade",
			"Upgrades the checksum-pinned supported predecessor to the exact current chart pair and proves schema ownership, persistent state, immutable Secrets, PVCs, Jobs, hooks, and rollout health.",
			"test-e2e-upgrade", 40,
			[]string{"HOR-415", "HOR-418", "HOR-475"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "resolve-supported-predecessor", DependsOn: []string{"create-kind"}, Run: resolveTransitionBaselinesStage},
			{Name: "install-predecessor-substrate", DependsOn: []string{"resolve-supported-predecessor"}, Run: installPredecessorSubstrateStage},
			{Name: "install-predecessor-platform", DependsOn: []string{"install-predecessor-substrate"}, Run: installPredecessorLifecyclePlatformStage},
			{Name: "seed-persisted-state", DependsOn: []string{"install-predecessor-platform"}, Run: seedPersistedStateStage},
			{Name: "capture-predecessor-state", DependsOn: []string{"seed-persisted-state"}, Run: capturePredecessorStateStage},
			{Name: "upgrade-current-pair", DependsOn: []string{"capture-predecessor-state"}, Run: upgradeCurrentLifecyclePairStage},
			{Name: "assert-upgrade-contract", DependsOn: []string{"upgrade-current-pair"}, Run: assertUpgradeContractStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func featureEnableUpgradeScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: transitionScenarioMetadata(
			"feature-enable-upgrade",
			"Starts from the supported internal-TLS predecessor without observability, pre-applies exact current operator CRDs, then enables the verified-HTTPS observability composition.",
			"test-e2e-feature-enable", 50,
			[]string{"HOR-415", "HOR-418", "HOR-420", "HOR-475"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "resolve-supported-predecessor", DependsOn: []string{"create-kind"}, Run: resolveTransitionBaselinesStage},
			{Name: "install-predecessor-substrate", DependsOn: []string{"resolve-supported-predecessor"}, Run: installPredecessorSubstrateStage},
			{Name: "install-predecessor-with-feature-disabled", DependsOn: []string{"install-predecessor-substrate"}, Run: installPredecessorFeatureDisabledStage},
			{Name: "assert-operator-crds-absent", DependsOn: []string{"install-predecessor-with-feature-disabled"}, Run: assertOperatorCRDsAbsentStage},
			{Name: "preapply-current-operator-crds", DependsOn: []string{"assert-operator-crds-absent"}, Run: preapplyCurrentOperatorCRDsStage},
			{Name: "enable-observability-tls", DependsOn: []string{"preapply-current-operator-crds"}, Run: enableObservabilityTLSStage},
			{Name: "assert-feature-stack-readiness", DependsOn: []string{"enable-observability-tls"}, Run: assertFeatureStackReadinessStage},
			{Name: "assert-feature-identities", DependsOn: []string{"assert-feature-stack-readiness"}, Run: assertObservabilityIdentitiesStage},
			{Name: "assert-feature-endpoint-separation", DependsOn: []string{"assert-feature-stack-readiness"}, Run: assertEndpointSeparationStage},
			{Name: "assert-feature-verified-stack", DependsOn: []string{"assert-feature-identities"}, Run: assertVerifiedStackHTTPSStage},
			{Name: "assert-feature-client-paths", DependsOn: []string{"assert-feature-verified-stack", "assert-feature-endpoint-separation"}, Run: assertFeatureEnableClientPathsStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func reapplyRollbackRecoveryScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: transitionScenarioMetadata(
			"reapply-rollback-recovery",
			"Reapplies current intent without rolling stable workloads, then exercises the supported inverse rollback to the declared predecessor and forward recovery while retaining state.",
			"test-e2e-reapply-rollback", 45,
			[]string{"HOR-415", "HOR-418", "HOR-475"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "resolve-supported-predecessor", DependsOn: []string{"create-kind"}, Run: resolveTransitionBaselinesStage},
			{Name: "install-predecessor-substrate", DependsOn: []string{"resolve-supported-predecessor"}, Run: installPredecessorSubstrateStage},
			{Name: "install-predecessor-platform", DependsOn: []string{"install-predecessor-substrate"}, Run: installPredecessorLifecyclePlatformStage},
			{Name: "seed-persisted-state", DependsOn: []string{"install-predecessor-platform"}, Run: seedPersistedStateStage},
			{Name: "upgrade-current-pair", DependsOn: []string{"seed-persisted-state"}, Run: upgradeCurrentLifecyclePairStage},
			{Name: "capture-current-state", DependsOn: []string{"upgrade-current-pair"}, Run: captureCurrentStateStage},
			{Name: "reapply-current-intent", DependsOn: []string{"capture-current-state"}, Run: reapplyCurrentPairStage},
			{Name: "assert-idempotent-reapply", DependsOn: []string{"reapply-current-intent"}, Run: assertIdempotentReapplyStage},
			{Name: "inverse-rollback", DependsOn: []string{"assert-idempotent-reapply"}, Run: inverseRollbackStage},
			{Name: "assert-rollback-boundary", DependsOn: []string{"inverse-rollback"}, Run: assertRollbackBoundaryStage},
			{Name: "forward-recovery", DependsOn: []string{"assert-rollback-boundary"}, Run: forwardRecoveryStage},
			{Name: "assert-forward-recovery", DependsOn: []string{"forward-recovery"}, Run: assertForwardRecoveryStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func transitionBaselineFixturePath() string {
	if path := os.Getenv("ITERABASE_E2E_TRANSITION_BASELINES"); path != "" {
		return path
	}
	return "transition-baselines.json"
}

func loadTransitionBaselineFixture(t *testing.T) sharede2e.Fixture {
	t.Helper()
	data, err := os.ReadFile(transitionBaselineFixturePath())
	if err != nil {
		t.Fatalf("read transition baseline fixture: %v", err)
	}
	fixture, err := decodeTransitionBaselineFixture(data)
	if err != nil {
		t.Fatalf("decode transition baseline fixture: %v", err)
	}
	return fixture
}

func decodeTransitionBaselineFixture(data []byte) (sharede2e.Fixture, error) {
	var fixture sharede2e.Fixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return sharede2e.Fixture{}, err
	}
	if fixture.Mode != sharede2e.FixturePublished {
		return sharede2e.Fixture{}, fmt.Errorf("transition baselines must use published mode")
	}
	if err := fixture.Validate(); err != nil {
		return sharede2e.Fixture{}, err
	}
	if len(fixture.Inputs) != 2 {
		return sharede2e.Fixture{}, fmt.Errorf("transition baseline fixture must contain exactly two charts")
	}
	expected := map[string]string{
		platformPredecessorName:  "iterabase-platform",
		substratePredecessorName: "cert-manager-substrate",
	}
	versions := make(map[string]string)
	for _, input := range fixture.Inputs {
		chart, _, version, err := parsePublishedChartReference(input.Reference)
		if err != nil {
			return sharede2e.Fixture{}, fmt.Errorf("%s: %w", input.Name, err)
		}
		if input.Kind != "published-chart" || expected[input.Name] != chart {
			return sharede2e.Fixture{}, fmt.Errorf("unexpected transition baseline %s=%s", input.Name, input.Reference)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(input.Checksum) {
			return sharede2e.Fixture{}, fmt.Errorf("transition baseline %s requires a canonical archive checksum", input.Name)
		}
		versions[input.Name] = version
	}
	if versions[platformPredecessorName] != versions[substratePredecessorName] {
		return sharede2e.Fixture{}, fmt.Errorf("platform and substrate predecessors must use the same version")
	}
	return fixture, nil
}

func parsePublishedChartReference(reference string) (chart, repository, version string, err error) {
	separator := strings.LastIndexByte(reference, ':')
	if separator <= len("oci://") || separator == len(reference)-1 {
		return "", "", "", fmt.Errorf("published chart reference has no exact version: %q", reference)
	}
	repository, version = reference[:separator], reference[separator+1:]
	chart = filepath.Base(repository)
	if chart == "." || chart == "/" || strings.Contains(strings.ToLower(version), "latest") {
		return "", "", "", fmt.Errorf("invalid published chart reference %q", reference)
	}
	return chart, repository, version, nil
}

func mergeChartFixtureInput(t *testing.T, fixture sharede2e.Fixture, required sharede2e.FixtureInput) sharede2e.Fixture {
	t.Helper()
	for _, input := range fixture.Inputs {
		if input.Name != required.Name || input.Kind != required.Kind {
			continue
		}
		if input != required {
			t.Fatalf("fixture input %s does not match chart-owned authority: got %+v want %+v", required.Name, input, required)
		}
		return fixture
	}
	fixture.Inputs = append(fixture.Inputs, required)
	return fixture
}

func transitionBaselines(fixture sharede2e.Fixture) (map[string]transitionBaseline, error) {
	baselines := make(map[string]transitionBaseline, len(fixture.Inputs))
	for _, input := range fixture.Inputs {
		chart, repository, version, err := parsePublishedChartReference(input.Reference)
		if err != nil {
			return nil, err
		}
		baselines[input.Name] = transitionBaseline{
			Name: input.Name, Chart: chart, Repository: repository, Version: version, Checksum: input.Checksum,
		}
	}
	return baselines, nil
}

func resolveTransitionBaselinesStage(t *testing.T, state *chartState) {
	t.Helper()
	baselines, err := transitionBaselines(loadTransitionBaselineFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	assertCurrentPairNewerThanPredecessor(t, state, baselines)
	directory := filepath.Join(state.outputDir, "transition-baselines")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create transition baseline directory: %v", err)
	}
	environment := map[string]string{
		platformPredecessorName:  "ITERABASE_E2E_PREDECESSOR_PLATFORM_ARCHIVE",
		substratePredecessorName: "ITERABASE_E2E_PREDECESSOR_SUBSTRATE_ARCHIVE",
	}
	for name, baseline := range baselines {
		archive := os.Getenv(environment[name])
		if archive == "" {
			state.process(t, 4*time.Minute, "helm", "pull", baseline.Repository, "--version", baseline.Version, "--destination", directory)
			archive = filepath.Join(directory, baseline.Chart+"-"+baseline.Version+".tgz")
		}
		archive, err = filepath.Abs(archive)
		if err != nil {
			t.Fatalf("resolve %s archive: %v", name, err)
		}
		if err := verifyArchiveChecksum(archive, baseline.Checksum); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
		baseline.Archive = archive
		baselines[name] = baseline
	}
	state.transitionBaselines = baselines
}

func assertCurrentPairNewerThanPredecessor(t *testing.T, state *chartState, baselines map[string]transitionBaseline) {
	t.Helper()
	for _, pair := range []struct {
		name  string
		chart kube.Chart
	}{
		{name: platformPredecessorName, chart: state.platform},
		{name: substratePredecessorName, chart: state.substrate},
	} {
		baseline, ok := baselines[pair.name]
		if !ok {
			t.Fatalf("transition baseline %s is missing", pair.name)
		}
		current := currentChartVersion(t, state, pair.chart)
		if err := newerChartVersionError(current, baseline.Version); err != nil {
			t.Fatalf("%s: %v", pair.name, err)
		}
	}
}

func newerChartVersionError(current, predecessor string) error {
	comparison, err := compareNumericVersions(current, predecessor)
	if err != nil {
		return fmt.Errorf("compare current chart %q with predecessor %q: %w", current, predecessor, err)
	}
	if comparison <= 0 {
		return fmt.Errorf("current chart version %s is not newer than supported predecessor %s", current, predecessor)
	}
	return nil
}

func verifyArchiveChecksum(path, expected string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	actual := hex.EncodeToString(digest[:])
	if actual != expected {
		return fmt.Errorf("archive checksum %s != expected %s", actual, expected)
	}
	return nil
}

func installPredecessorSubstrateStage(t *testing.T, state *chartState) {
	baseline := requireTransitionBaseline(t, state, substratePredecessorName)
	args := []string{"upgrade", "--install", testRelease + "-cert-manager", "--namespace", testNamespace,
		"--create-namespace", "--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "8m", baseline.Archive}
	state.process(t, 10*time.Minute, "helm", args...)
	assertReleaseChartVersion(t, state, testRelease+"-cert-manager", baseline.Chart, baseline.Version)
}

func installPredecessorLifecyclePlatformStage(t *testing.T, state *chartState) {
	installPredecessorPlatform(t, state, lifecyclePlatformValueFiles(t, state)...)
	assertLifecycleHealth(t, state)
	assertReleaseMechanics(t, state)
}

func installPredecessorFeatureDisabledStage(t *testing.T, state *chartState) {
	installPredecessorPlatform(t, state,
		filepathFromCharts(state, "values-tls.yaml"),
		state.writeValues(t, "feature-disabled", runtimePlatformValues(t)),
	)
	assertInternalIdentitiesStage(t, state)
	assertGatewayDependenciesStage(t, state)
	assertControlPlaneVerifiedHTTPSStage(t, state)
}

func installPredecessorPlatform(t *testing.T, state *chartState, valueFiles ...string) {
	t.Helper()
	baseline := requireTransitionBaseline(t, state, platformPredecessorName)
	args := []string{"upgrade", "--install", testRelease, "--namespace", testNamespace,
		"--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "18m"}
	for _, values := range valueFiles {
		args = append(args, "--values", values)
	}
	args = append(args, baseline.Archive)
	state.process(t, 20*time.Minute, "helm", args...)
	assertReleaseChartVersion(t, state, testRelease, baseline.Chart, baseline.Version)
}

func requireTransitionBaseline(t *testing.T, state *chartState, name string) transitionBaseline {
	t.Helper()
	baseline, ok := state.transitionBaselines[name]
	if !ok || baseline.Archive == "" {
		t.Fatalf("transition baseline %s is unresolved", name)
	}
	return baseline
}

func lifecyclePlatformValueFiles(t *testing.T, state *chartState) []string {
	t.Helper()
	values := runtimePlatformValues(t)
	values["minio"] = map[string]any{
		"enabled":         true,
		"artifactService": map[string]any{"enabled": true, "bucket": "iterabase-artifacts"},
	}
	controlPlane := values["control-plane"].(map[string]any)
	controlPlane["artifact"] = map[string]any{"enabled": true}
	return []string{state.writeValues(t, "lifecycle-runtime", values)}
}

func upgradeCurrentLifecyclePairStage(t *testing.T, state *chartState) {
	state.installSubstrate(t)
	state.installPlatform(t, 18*time.Minute, lifecyclePlatformValueFiles(t, state)...)
	assertCandidateImages(t, state)
}

func enableObservabilityTLSStage(t *testing.T, state *chartState) {
	state.installSubstrate(t)
	state.installPlatform(t, 22*time.Minute,
		filepathFromCharts(state, "values-observability.yaml"),
		filepathFromCharts(state, "values-tls.yaml"),
		state.writeValues(t, "feature-enabled", runtimePlatformValues(t)),
	)
	assertCandidateImages(t, state)
}

func seedPersistedStateStage(t *testing.T, state *chartState) {
	t.Helper()
	state.kubectl(t, 2*time.Minute, "exec", "-n", testNamespace, "statefulset/"+testRelease+"-postgresql", "--",
		"psql", "-U", "controlplane", "-d", "controlplane", "-v", "ON_ERROR_STOP=1", "-c",
		"CREATE TABLE IF NOT EXISTS e2e_chart_transition (marker text PRIMARY KEY); INSERT INTO e2e_chart_transition(marker) VALUES ('"+transitionMarker+"') ON CONFLICT DO NOTHING;")
	state.kubectl(t, 30*time.Second, "exec", "-n", testNamespace, "statefulset/"+testRelease+"-minio", "--",
		"sh", "-c", "printf '%s' '"+transitionMarker+"' > /data/"+transitionMarker)
	assertPersistedState(t, state)
}

func assertPersistedState(t *testing.T, state *chartState) {
	t.Helper()
	postgres := state.kubectl(t, 90*time.Second, "exec", "-n", testNamespace, "statefulset/"+testRelease+"-postgresql", "--",
		"psql", "-At", "-U", "controlplane", "-d", "controlplane", "-c",
		"SELECT marker FROM e2e_chart_transition WHERE marker='"+transitionMarker+"';")
	if postgres != transitionMarker {
		t.Fatalf("PostgreSQL persisted marker=%q want=%q", postgres, transitionMarker)
	}
	minio := state.kubectl(t, 30*time.Second, "exec", "-n", testNamespace, "statefulset/"+testRelease+"-minio", "--",
		"cat", "/data/"+transitionMarker)
	if minio != transitionMarker {
		t.Fatalf("MinIO persisted marker=%q want=%q", minio, transitionMarker)
	}
}

func capturePredecessorStateStage(t *testing.T, state *chartState) {
	state.snapshots["predecessor"] = captureLifecycleSnapshot(t, state)
}

func captureCurrentStateStage(t *testing.T, state *chartState) {
	assertLifecycleHealth(t, state)
	assertPersistedState(t, state)
	state.snapshots["current"] = captureLifecycleSnapshot(t, state)
}

func captureLifecycleSnapshot(t *testing.T, state *chartState) lifecycleSnapshot {
	t.Helper()
	snapshot := lifecycleSnapshot{Secrets: map[string]string{}, PVCs: map[string]string{}, Pods: map[string]string{}}
	for _, name := range []string{
		testRelease + "-postgresql", testRelease + "-minio", testRelease + "-minio-artifacts",
		testRelease + "-control-plane-jwt", testRelease + "-gateway-admin",
	} {
		snapshot.Secrets[name] = secretDigest(t, state, name)
	}
	for _, name := range []string{"data-" + testRelease + "-postgresql-0", "data-" + testRelease + "-minio-0"} {
		snapshot.PVCs[name] = state.kubectl(t, 30*time.Second, "get", "pvc/"+name, "-n", testNamespace,
			"-o", "jsonpath={.metadata.uid}/{.spec.volumeName}")
	}
	for _, release := range []string{testRelease, testRelease + "-cert-manager"} {
		releaseSelector := "app.kubernetes.io/instance=" + release
		workloadData := state.kubectl(t, 30*time.Second, "get", "deployments,statefulsets,daemonsets", "-n", testNamespace,
			"-l", releaseSelector, "-o", "json")
		workloads, err := stableWorkloadSelectorsJSON([]byte(workloadData))
		if err != nil {
			t.Fatalf("discover stable workloads for release %s: %v", release, err)
		}
		for _, workload := range workloads {
			identities := strings.Fields(state.kubectl(t, 30*time.Second, "get", "pods", "-n", testNamespace,
				"-l", workload.Selector, "-o", `go-template={{range .items}}{{if not .metadata.deletionTimestamp}}{{.metadata.name}}={{.metadata.uid}}{{"\n"}}{{end}}{{end}}`))
			if len(identities) == 0 {
				t.Fatalf("%s/%s has no stable pod identity", release, workload.Key)
			}
			for _, identity := range identities {
				name, uid, ok := strings.Cut(identity, "=")
				if !ok || name == "" || uid == "" {
					t.Fatalf("%s/%s returned invalid pod identity %q", release, workload.Key, identity)
				}
				snapshot.Pods[release+"/"+workload.Key+"/"+name] = uid
			}
		}
	}
	return snapshot
}

type stableWorkloadSelector struct {
	Key      string
	Selector string
}

func stableWorkloadSelectorsJSON(workloadData []byte) ([]stableWorkloadSelector, error) {
	var workloads struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Selector struct {
					MatchLabels      map[string]string `json:"matchLabels"`
					MatchExpressions []json.RawMessage `json:"matchExpressions"`
				} `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(workloadData, &workloads); err != nil {
		return nil, fmt.Errorf("decode stable workloads: %w", err)
	}
	if len(workloads.Items) == 0 {
		return nil, errors.New("release has no stable workloads")
	}
	selectors := make([]stableWorkloadSelector, 0, len(workloads.Items))
	for _, workload := range workloads.Items {
		if workload.Metadata.Name == "" || len(workload.Spec.Selector.MatchLabels) == 0 || len(workload.Spec.Selector.MatchExpressions) != 0 {
			return nil, fmt.Errorf("%s/%s requires an exact matchLabels selector", workload.Kind, workload.Metadata.Name)
		}
		labels := make([]string, 0, len(workload.Spec.Selector.MatchLabels))
		for key, value := range workload.Spec.Selector.MatchLabels {
			labels = append(labels, key+"="+value)
		}
		sort.Strings(labels)
		selectors = append(selectors, stableWorkloadSelector{
			Key: strings.ToLower(workload.Kind) + "/" + workload.Metadata.Name, Selector: strings.Join(labels, ","),
		})
	}
	sort.Slice(selectors, func(i, j int) bool { return selectors[i].Key < selectors[j].Key })
	return selectors, nil
}

func secretDigest(t *testing.T, state *chartState, name string) string {
	t.Helper()
	result, err := state.runner.Run(state.ctx, process.Command{
		Name: "bash", Args: []string{"-o", "pipefail", "-c", `kubectl --kubeconfig "$KUBECONFIG_PATH" get secret "$SECRET_NAME" -n "$SECRET_NAMESPACE" -o go-template='{{range $key, $value := .data}}{{$key}}={{$value}}{{"\n"}}{{end}}' | sha256sum | awk '{print $1}'`},
		Env:     map[string]string{"KUBECONFIG_PATH": state.cluster.Kubeconfig, "SECRET_NAME": name, "SECRET_NAMESPACE": testNamespace},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("hash Secret %s without retaining its values: %v", name, err)
	}
	digest := strings.TrimSpace(result.Output)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
		t.Fatalf("Secret %s returned invalid digest %q", name, digest)
	}
	return digest
}

func assertUpgradeContractStage(t *testing.T, state *chartState) {
	currentPlatform := currentChartVersion(t, state, state.platform)
	currentSubstrate := currentChartVersion(t, state, state.substrate)
	assertReleaseChartVersion(t, state, testRelease, "iterabase-platform", currentPlatform)
	assertReleaseChartVersion(t, state, testRelease+"-cert-manager", "cert-manager-substrate", currentSubstrate)
	assertSchemaOwnership(t, state)
	assertLifecycleHealth(t, state)
	assertReleaseMechanics(t, state)
	assertPersistedState(t, state)
	assertRetainedState(t, state.snapshots["predecessor"], captureLifecycleSnapshot(t, state), false)
}

func assertRetainedState(t *testing.T, before, after lifecycleSnapshot, includePods bool) {
	t.Helper()
	if err := retainedStateError(before, after, includePods); err != nil {
		t.Fatal(err)
	}
}

func retainedStateError(before, after lifecycleSnapshot, includePods bool) error {
	if !mapsEqual(before.Secrets, after.Secrets) {
		return fmt.Errorf("generated Secret digests changed: before=%v after=%v", before.Secrets, after.Secrets)
	}
	if !mapsEqual(before.PVCs, after.PVCs) {
		return fmt.Errorf("PVC identities changed: before=%v after=%v", before.PVCs, after.PVCs)
	}
	if includePods && !mapsEqual(before.Pods, after.Pods) {
		return fmt.Errorf("idempotent reapply rolled workloads: before=%v after=%v", before.Pods, after.Pods)
	}
	return nil
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func assertSchemaOwnership(t *testing.T, state *chartState) {
	t.Helper()
	manifest := state.process(t, 90*time.Second, "helm", "get", "manifest", testRelease, "-n", testNamespace,
		"--kubeconfig", state.cluster.Kubeconfig)
	for _, crd := range []string{"agentpools.platform.iterabase.com", "workflows.platform.iterabase.com"} {
		if !strings.Contains(manifest, "name: "+crd) {
			t.Fatalf("Helm release does not retain declarative ownership of %s", crd)
		}
		managedBy := state.kubectl(t, 30*time.Second, "get", "crd/"+crd,
			"-o", "jsonpath={.metadata.labels.app\\.kubernetes\\.io/managed-by}")
		instance := state.kubectl(t, 30*time.Second, "get", "crd/"+crd,
			"-o", "jsonpath={.metadata.labels.app\\.kubernetes\\.io/instance}")
		storedVersion := state.kubectl(t, 30*time.Second, "get", "crd/"+crd, "-o", "jsonpath={.status.storedVersions[0]}")
		if managedBy != "Helm" || instance != testRelease || storedVersion != "v1alpha1" {
			t.Fatalf("%s ownership/schema managed-by=%q instance=%q storedVersion=%q", crd, managedBy, instance, storedVersion)
		}
	}
}

func assertReleaseMechanics(t *testing.T, state *chartState) {
	t.Helper()
	hooks := state.process(t, 60*time.Second, "helm", "get", "hooks", testRelease+"-cert-manager", "-n", testNamespace,
		"--kubeconfig", state.cluster.Kubeconfig)
	if !strings.Contains(hooks, "startupapicheck") || !strings.Contains(hooks, "helm.sh/hook: post-install") {
		t.Fatalf("certificate substrate does not retain its startup API hook: %s", stateSafeBody([]byte(hooks)))
	}
	state.kubectl(t, 4*time.Minute, "wait", "--for=condition=Complete", "job", "-n", testNamespace,
		"-l", "app.kubernetes.io/name=minio,app.kubernetes.io/component=artifact-provisioner", "--timeout=3m")
}

func assertLifecycleHealth(t *testing.T, state *chartState) {
	t.Helper()
	for _, workload := range []string{
		"statefulset/" + testRelease + "-postgresql",
		"statefulset/" + testRelease + "-minio",
		"deployment/" + testRelease + "-redis",
		"deployment/" + testRelease + "-control-plane-api",
		"deployment/" + testRelease + "-control-plane-manager",
		"deployment/" + testRelease + "-gateway",
	} {
		state.kubectl(t, 6*time.Minute, "rollout", "status", workload, "-n", testNamespace, "--timeout=5m")
	}
	client, err := httpx.Client(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	controlPlane := state.forward(t, "svc/"+testRelease+"-control-plane-api", 8080, "http")
	requireHTTP(t, client, http.MethodGet, controlPlane.URL+"/healthz", nil, http.StatusOK)
	state.stopForward(t, controlPlane)
	gateway := state.forward(t, "svc/"+testRelease+"-gateway", 8080, "http")
	body := requireHTTP(t, client, http.MethodGet, gateway.URL+"/readyz", nil, http.StatusOK)
	if !strings.Contains(string(body), `"fresh":true`) {
		t.Fatalf("gateway snapshot is not fresh: %s", stateSafeBody(body))
	}
	state.stopForward(t, gateway)
}

func assertOperatorCRDsAbsentStage(t *testing.T, state *chartState) {
	for _, crd := range operatorCRDs {
		name := state.kubectl(t, 30*time.Second, "get", "crd/"+crd, "--ignore-not-found", "-o", "name")
		if name != "" {
			t.Fatalf("operator CRD %s unexpectedly existed before feature enablement: %s", crd, name)
		}
	}
}

func preapplyCurrentOperatorCRDsStage(t *testing.T, state *chartState) {
	t.Helper()
	// Do not pass a CRD schema through text redaction before applying it: schema
	// property names can look credential-shaped even though this exact chart
	// manifest contains no Secret values. Redirect stdout to a private file and
	// retain only command stderr through the shared process runner.
	path := filepath.Join(t.TempDir(), "current-platform-crds.yaml")
	args := []string{"-o", "pipefail", "-c", `helm show crds "$@" > "$CRD_OUTPUT"`, "--"}
	args = append(args, helmChartArgs(state.platform)...)
	if _, err := state.runner.Run(state.ctx, process.Command{
		Name: "bash", Args: args, Env: map[string]string{"CRD_OUTPUT": path}, Timeout: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("extract exact current platform CRDs: %v", err)
	}
	manifest, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exact current platform CRDs: %v", err)
	}
	selected, err := selectBundledCRDs(string(manifest))
	if err != nil {
		t.Fatalf("select authoritative current platform CRDs: %v", err)
	}
	for _, crd := range operatorCRDs {
		if !strings.Contains(selected, "name: "+crd) {
			t.Fatalf("exact current platform archive does not bundle %s", crd)
		}
	}
	if err := os.WriteFile(path, []byte(selected), 0o600); err != nil {
		t.Fatalf("write selected current platform CRDs: %v", err)
	}
	state.kubectl(t, 3*time.Minute, "apply", "--server-side", "--force-conflicts", "--field-manager="+transitionFieldManager, "-f", path)
	for _, crd := range operatorCRDs {
		state.kubectl(t, 3*time.Minute, "wait", "--for=condition=Established", "crd/"+crd, "--timeout=2m")
	}
	// CRD schemas contain credential-shaped property names that text redaction
	// can make invalid JSON. Keep the exact kubectl payload in a private
	// temporary file and expose only command stderr to the shared runner.
	statusPath := filepath.Join(t.TempDir(), "operator-crd-statuses.json")
	if err := os.WriteFile(statusPath, nil, 0o600); err != nil {
		t.Fatalf("create operator CRD status file: %v", err)
	}
	statusArgs := []string{"-o", "pipefail", "-c", `kubectl --kubeconfig "$KUBECONFIG_PATH" get "$@" -o json > "$CRD_STATUS_OUTPUT"`, "--"}
	for _, crd := range operatorCRDs {
		statusArgs = append(statusArgs, "crd/"+crd)
	}
	if _, err := state.runner.Run(state.ctx, process.Command{
		Name: "bash", Args: statusArgs,
		Env: map[string]string{"KUBECONFIG_PATH": state.cluster.Kubeconfig, "CRD_STATUS_OUTPUT": statusPath}, Timeout: 30 * time.Second,
	}); err != nil {
		t.Fatalf("read operator CRD statuses: %v", err)
	}
	statuses, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read operator CRD status file: %v", err)
	}
	if err := assertEstablishedCRDsJSON(statuses, operatorCRDs); err != nil {
		t.Fatal(err)
	}
	for _, crd := range operatorCRDs {
		managers := state.kubectl(t, 30*time.Second, "get", "crd/"+crd, "-o", "jsonpath={.metadata.managedFields[*].manager}")
		if !slices.Contains(strings.Fields(managers), transitionFieldManager) {
			t.Fatalf("%s was not server-side applied by %s: %s", crd, transitionFieldManager, managers)
		}
	}
}

func selectBundledCRDs(raw string) (string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	selected := make(map[string]bundledCRD)
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("decode bundled CRDs: %w", err)
		}
		var header bundledCRDHeader
		if err := document.Decode(&header); err != nil {
			return "", fmt.Errorf("decode bundled CRD header: %w", err)
		}
		if header.APIVersion != "apiextensions.k8s.io/v1" || header.Kind != "CustomResourceDefinition" {
			continue
		}
		if header.Metadata.Name == "" {
			return "", errors.New("bundled CRD is missing metadata.name")
		}
		var resource any
		if err := document.Decode(&resource); err != nil {
			return "", fmt.Errorf("decode bundled CRD %s: %w", header.Metadata.Name, err)
		}
		encoded, err := yaml.Marshal(resource)
		if err != nil {
			return "", fmt.Errorf("encode bundled CRD %s: %w", header.Metadata.Name, err)
		}
		candidate := bundledCRD{header: header, manifest: strings.TrimSpace(string(encoded))}
		if existing, duplicate := selected[header.Metadata.Name]; duplicate {
			candidate, err = selectBundledCRD(existing, candidate)
			if err != nil {
				return "", err
			}
		}
		selected[header.Metadata.Name] = candidate
	}
	if len(selected) == 0 {
		return "", errors.New("exact current platform archive contains no CRDs")
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	manifests := make([]string, 0, len(names))
	for _, name := range names {
		manifests = append(manifests, selected[name].manifest)
	}
	return strings.Join(manifests, "\n---\n") + "\n", nil
}

func selectBundledCRD(existing, candidate bundledCRD) (bundledCRD, error) {
	if existing.manifest == candidate.manifest {
		return existing, nil
	}
	const versionAnnotation = "operator.prometheus.io/version"
	existingVersion := existing.header.Metadata.Annotations[versionAnnotation]
	candidateVersion := candidate.header.Metadata.Annotations[versionAnnotation]
	if existingVersion == "" && candidateVersion == "" {
		return bundledCRD{}, fmt.Errorf("conflicting duplicate bundled CRD %q has no authoritative version annotation", existing.header.Metadata.Name)
	}
	if existingVersion == "" {
		return candidate, nil
	}
	if candidateVersion == "" {
		return existing, nil
	}
	comparison, err := compareNumericVersions(existingVersion, candidateVersion)
	if err != nil {
		return bundledCRD{}, fmt.Errorf("compare duplicate bundled CRD %q versions: %w", existing.header.Metadata.Name, err)
	}
	if comparison < 0 {
		return candidate, nil
	}
	if comparison > 0 {
		return existing, nil
	}
	return bundledCRD{}, fmt.Errorf("conflicting duplicate bundled CRD %q has equal authoritative version %q", existing.header.Metadata.Name, existingVersion)
}

func compareNumericVersions(left, right string) (int, error) {
	parse := func(version string) ([]int, error) {
		parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid numeric version %q", version)
		}
		values := make([]int, len(parts))
		for index, part := range parts {
			value, err := strconv.Atoi(part)
			if err != nil || value < 0 {
				return nil, fmt.Errorf("invalid numeric version %q", version)
			}
			values[index] = value
		}
		return values, nil
	}
	leftParts, err := parse(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parse(right)
	if err != nil {
		return 0, err
	}
	for index := 0; index < max(len(leftParts), len(rightParts)); index++ {
		var leftPart, rightPart int
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		if leftPart < rightPart {
			return -1, nil
		}
		if leftPart > rightPart {
			return 1, nil
		}
	}
	return 0, nil
}

func assertEstablishedCRDsJSON(data []byte, required []string) error {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("decode operator CRDs: %w", err)
	}
	established := make(map[string]bool, len(list.Items))
	for _, item := range list.Items {
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Established" && condition.Status == "True" {
				established[item.Metadata.Name] = true
			}
		}
	}
	for _, name := range required {
		if !established[name] {
			return fmt.Errorf("operator CRD %s is not Established", name)
		}
	}
	return nil
}

func assertFeatureEnableClientPathsStage(t *testing.T, state *chartState) {
	assertTLSExporterPaths(t, state, false, false)
	assertVerifiedSelfMonitorsStage(t, state)
	assertGrafanaTLSPathsStage(t, state)
	assertLokiGatewayTLSStage(t, state)
	assertTLSPromtailPathStage(t, state)
	assertTLSAlertmanagerPathStage(t, state)
	assertGatewayDependenciesStage(t, state)
	assertRenderedTLSClientConfigStage(t, state)
	assertRedisTransportStage(t, state)
	assertPostgreSQLTransportStage(t, state)
}

func reapplyCurrentPairStage(t *testing.T, state *chartState) {
	state.installSubstrate(t)
	state.installPlatform(t, 18*time.Minute, lifecyclePlatformValueFiles(t, state)...)
}

func assertIdempotentReapplyStage(t *testing.T, state *chartState) {
	before := state.snapshots["current"]
	after := captureLifecycleSnapshot(t, state)
	assertRetainedState(t, before, after, true)
	assertPersistedState(t, state)
	assertLifecycleHealth(t, state)
	assertReleaseRevision(t, state, testRelease, 3)
	assertReleaseRevision(t, state, testRelease+"-cert-manager", 3)
}

func inverseRollbackStage(t *testing.T, state *chartState) {
	// The supported post-0.3 inverse boundary restores the platform first, then
	// its same-version substrate. CRDs, generated Secrets, and PVCs are retained.
	rollbackRelease(t, state, testRelease, 1)
	rollbackRelease(t, state, testRelease+"-cert-manager", 1)
}

func rollbackRelease(t *testing.T, state *chartState, release string, revision int) {
	t.Helper()
	state.process(t, 12*time.Minute, "helm", "rollback", release, strconv.Itoa(revision), "--namespace", testNamespace,
		"--kubeconfig", state.cluster.Kubeconfig, "--wait", "--timeout", "10m")
}

func assertRollbackBoundaryStage(t *testing.T, state *chartState) {
	platform := requireTransitionBaseline(t, state, platformPredecessorName)
	substrate := requireTransitionBaseline(t, state, substratePredecessorName)
	assertRollbackReleaseHistory(t, state, testRelease, platform.Chart, platform.Version, 4)
	assertRollbackReleaseHistory(t, state, testRelease+"-cert-manager", substrate.Chart, substrate.Version, 4)
	assertPersistedState(t, state)
	assertRetainedState(t, state.snapshots["current"], captureLifecycleSnapshot(t, state), false)
	assertLifecycleHealth(t, state)
}

func forwardRecoveryStage(t *testing.T, state *chartState) {
	state.installSubstrate(t)
	state.installPlatform(t, 18*time.Minute, lifecyclePlatformValueFiles(t, state)...)
	assertCandidateImages(t, state)
}

func assertForwardRecoveryStage(t *testing.T, state *chartState) {
	assertReleaseChartVersion(t, state, testRelease, "iterabase-platform", currentChartVersion(t, state, state.platform))
	assertReleaseChartVersion(t, state, testRelease+"-cert-manager", "cert-manager-substrate", currentChartVersion(t, state, state.substrate))
	assertPersistedState(t, state)
	assertRetainedState(t, state.snapshots["current"], captureLifecycleSnapshot(t, state), false)
	assertLifecycleHealth(t, state)
	assertSchemaOwnership(t, state)
}

func currentChartVersion(t *testing.T, state *chartState, chart kube.Chart) string {
	t.Helper()
	// Keep the Helm input selection identical to installation rather than
	// trusting a parallel version constant.
	args := []string{"show", "chart"}
	if chart.LocalPath != "" {
		args = append(args, chart.LocalPath)
	} else {
		args = append(args, chart.Reference, "--version", chart.Version)
	}
	metadata := state.process(t, 60*time.Second, "helm", args...)
	for _, line := range strings.Split(metadata, "\n") {
		if version, ok := strings.CutPrefix(line, "version:"); ok {
			return strings.Trim(strings.TrimSpace(version), `"'`)
		}
	}
	t.Fatalf("chart metadata has no version: %s", metadata)
	return ""
}

func assertReleaseChartVersion(t *testing.T, state *chartState, release, chart, version string) {
	t.Helper()
	entry, err := currentHelmHistoryEntry([]byte(state.process(t, 60*time.Second, "helm", "history", release,
		"--namespace", testNamespace, "--kubeconfig", state.cluster.Kubeconfig, "--output", "json")))
	if err != nil {
		t.Fatal(err)
	}
	want := chart + "-" + version
	if entry.Chart != want || entry.Status != "deployed" {
		t.Fatalf("%s current history chart=%q status=%q want chart=%q deployed", release, entry.Chart, entry.Status, want)
	}
}

func assertReleaseRevision(t *testing.T, state *chartState, release string, want int) {
	t.Helper()
	entry, err := currentHelmHistoryEntry([]byte(state.process(t, 60*time.Second, "helm", "history", release,
		"--namespace", testNamespace, "--kubeconfig", state.cluster.Kubeconfig, "--output", "json")))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Revision != want || entry.Status != "deployed" {
		t.Fatalf("%s revision=%d status=%q want revision=%d deployed", release, entry.Revision, entry.Status, want)
	}
}

func assertRollbackReleaseHistory(t *testing.T, state *chartState, release, chart, version string, revision int) {
	t.Helper()
	entry, err := currentHelmHistoryEntry([]byte(state.process(t, 60*time.Second, "helm", "history", release,
		"--namespace", testNamespace, "--kubeconfig", state.cluster.Kubeconfig, "--output", "json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackHistoryError(entry, chart, version, revision); err != nil {
		t.Fatalf("%s rollback boundary: %v", release, err)
	}
}

func rollbackHistoryError(entry helmHistoryEntry, chart, version string, revision int) error {
	wantChart := chart + "-" + version
	if entry.Revision != revision || entry.Status != "deployed" || entry.Chart != wantChart {
		return fmt.Errorf("revision=%d status=%q chart=%q want revision=%d status=deployed chart=%q",
			entry.Revision, entry.Status, entry.Chart, revision, wantChart)
	}
	return nil
}

func currentHelmHistoryEntry(data []byte) (helmHistoryEntry, error) {
	var history []helmHistoryEntry
	if err := json.Unmarshal(data, &history); err != nil {
		return helmHistoryEntry{}, fmt.Errorf("decode Helm history: %w", err)
	}
	if len(history) == 0 {
		return helmHistoryEntry{}, fmt.Errorf("Helm history is empty")
	}
	sort.Slice(history, func(i, j int) bool { return history[i].Revision < history[j].Revision })
	return history[len(history)-1], nil
}

func TestUnitTransitionBaselineFixtureRejectsMutableOrMismatchedInputs(t *testing.T) {
	valid, err := os.ReadFile("transition-baselines.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTransitionBaselineFixture(valid); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	for name, mutate := range map[string]func(string) string{
		"latest":       func(value string) string { return strings.Replace(value, ":0.3.10", ":latest", 1) },
		"bad checksum": func(value string) string { return strings.Replace(value, "71b50a9b", "notahash", 1) },
		"version mismatch": func(value string) string {
			return strings.Replace(value, "cert-manager-substrate:0.3.10", "cert-manager-substrate:0.3.9", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTransitionBaselineFixture([]byte(mutate(string(valid)))); err == nil {
				t.Fatal("intentional transition baseline break passed")
			}
		})
	}
}

func TestUnitBundledCRDsSelectAuthoritativeOperatorSchema(t *testing.T) {
	authoritative := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: servicemonitors.monitoring.coreos.com
  annotations:
    operator.prometheus.io/version: 0.93.0
spec:
  group: monitoring.coreos.com`
	stale := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: servicemonitors.monitoring.coreos.com
spec:
  group: stale.example.com`
	selected, err := selectBundledCRDs(stale + "\n---\n" + authoritative)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selected, "group: monitoring.coreos.com") || strings.Contains(selected, "stale.example.com") {
		t.Fatalf("authoritative operator schema was not selected:\n%s", selected)
	}
	ambiguous := strings.ReplaceAll(stale, "stale.example.com", "first.example.com") + "\n---\n" + strings.ReplaceAll(stale, "stale.example.com", "second.example.com")
	if _, err := selectBundledCRDs(ambiguous); err == nil {
		t.Fatal("ambiguous duplicate bundled CRDs passed")
	}
}

func TestUnitOperatorCRDsMustBeEstablishedBeforeFeatureEnable(t *testing.T) {
	fixture := []byte(`{"items":[{"metadata":{"name":"servicemonitors.monitoring.coreos.com"},"status":{"conditions":[{"type":"Established","status":"False"}]}}]}`)
	if err := assertEstablishedCRDsJSON(fixture, []string{"servicemonitors.monitoring.coreos.com"}); err == nil {
		t.Fatal("unestablished operator CRD passed")
	}
}

func TestUnitRetainedStateRejectsSecretPVCAndReapplyRolloutChanges(t *testing.T) {
	baseline := lifecycleSnapshot{Secrets: map[string]string{"secret": "a"}, PVCs: map[string]string{"pvc": "b"}, Pods: map[string]string{"pods": "c"}}
	changes := []lifecycleSnapshot{
		{Secrets: map[string]string{"secret": "changed"}, PVCs: baseline.PVCs, Pods: baseline.Pods},
		{Secrets: baseline.Secrets, PVCs: map[string]string{"pvc": "changed"}, Pods: baseline.Pods},
		{Secrets: baseline.Secrets, PVCs: baseline.PVCs, Pods: map[string]string{"pods": "changed"}},
	}
	for index, changed := range changes {
		if err := retainedStateError(baseline, changed, true); err == nil {
			t.Fatalf("intentional retained-state break %d passed", index)
		}
	}
}

func TestUnitRollbackBoundaryRejectsIncorrectHelmHistory(t *testing.T) {
	entry, err := currentHelmHistoryEntry([]byte(`[{"revision":3,"status":"superseded","chart":"iterabase-platform-0.3.11"},{"revision":4,"status":"deployed","chart":"iterabase-platform-0.3.10"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackHistoryError(entry, "iterabase-platform", "0.3.10", 4); err != nil {
		t.Fatalf("valid rollback history rejected: %v", err)
	}
	for name, changed := range map[string]helmHistoryEntry{
		"wrong revision": {Revision: 3, Status: "deployed", Chart: "iterabase-platform-0.3.10"},
		"wrong status":   {Revision: 4, Status: "failed", Chart: "iterabase-platform-0.3.10"},
		"wrong chart":    {Revision: 4, Status: "deployed", Chart: "iterabase-platform-0.3.11"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := rollbackHistoryError(changed, "iterabase-platform", "0.3.10", 4); err == nil {
				t.Fatal("intentional rollback history break passed")
			}
		})
	}
}

func TestUnitTransitionCurrentMustBeNewerThanPredecessor(t *testing.T) {
	metadata := transitionScenarioMetadata("fixture-mode-contract", "fixture mode contract", "", 0, nil, nil)
	if !slices.Equal(metadata.FixtureModes, []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate}) {
		t.Fatalf("transition fixture modes=%v", metadata.FixtureModes)
	}
	if err := newerChartVersionError("0.3.11", "0.3.10"); err != nil {
		t.Fatalf("newer current chart rejected: %v", err)
	}
	for _, current := range []string{"0.3.10", "0.3.9", "0.3.1", "0.3.11-rc.1"} {
		if err := newerChartVersionError(current, "0.3.10"); err == nil {
			t.Fatalf("non-newer or unsupported current chart version %q passed", current)
		}
	}
}

func TestUnitStableWorkloadSnapshotIncludesEveryController(t *testing.T) {
	workloads := []byte(`{"items":[
		{"kind":"Deployment","metadata":{"name":"manager"},"spec":{"selector":{"matchLabels":{"component":"manager","app":"control-plane"}}}},
		{"kind":"DaemonSet","metadata":{"name":"csi"},"spec":{"selector":{"matchLabels":{"app":"csi"}}}}
	]}`)
	selectors, err := stableWorkloadSelectorsJSON(workloads)
	if err != nil {
		t.Fatal(err)
	}
	want := []stableWorkloadSelector{
		{Key: "daemonset/csi", Selector: "app=csi"},
		{Key: "deployment/manager", Selector: "app=control-plane,component=manager"},
	}
	if !slices.Equal(selectors, want) {
		t.Fatalf("stable workload selectors=%v want=%v", selectors, want)
	}
}

func TestUnitTransitionBaselineOrderIsStable(t *testing.T) {
	fixture := loadTransitionBaselineFixture(t)
	names := make([]string, 0, len(fixture.Inputs))
	for _, input := range fixture.Inputs {
		names = append(names, input.Name)
	}
	if !slices.Equal(names, []string{platformPredecessorName, substratePredecessorName}) {
		t.Fatalf("transition baseline order=%v", names)
	}
}
