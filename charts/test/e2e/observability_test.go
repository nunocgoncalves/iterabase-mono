package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/httpx"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/poll"
)

func observabilityScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: chartScenarioMetadata(
			"observability",
			"Installs only the chart-owned observability composition and proves stack readiness, monitor discovery, disjoint endpoints, client paths, and unambiguous Prometheus/Loki persistence.",
			"test-e2e-observability", 40,
			[]string{"HOR-408", "HOR-414", "HOR-418", "HOR-416"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"create-kind"}, Run: installCertificateSubstrateStage},
			{Name: "install-observability", DependsOn: []string{"install-certificate-substrate"}, Run: installObservabilityStage},
			{Name: "install-harness-worker", DependsOn: []string{"install-observability"}, Run: installObservabilityHarnessStage},
			{Name: "assert-stack-readiness", DependsOn: []string{"install-harness-worker"}, Run: assertStackReadinessStage},
			{Name: "assert-grafana-dashboards", DependsOn: []string{"assert-stack-readiness"}, Run: assertGrafanaDashboardsStage},
			{Name: "assert-endpoint-separation", DependsOn: []string{"assert-stack-readiness"}, Run: assertEndpointSeparationStage},
			{Name: "assert-monitor-discovery", DependsOn: []string{"assert-stack-readiness"}, Run: assertMonitorDiscoveryStage},
			{Name: "assert-prometheus-persistence", DependsOn: []string{"assert-monitor-discovery"}, Run: assertPrometheusPersistenceStage},
			{Name: "assert-loki-persistence", DependsOn: []string{"assert-stack-readiness"}, Run: assertLokiPersistenceStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func installObservabilityStage(t *testing.T, state *chartState) {
	t.Helper()
	values := observabilityPlatformValues()
	state.installPlatform(t, 20*time.Minute,
		filepathFromCharts(state, "values-observability.yaml"),
		state.writeValues(t, "observability-runtime", values),
	)
	assertCandidateImages(t, state)
}

func observabilityPlatformValues() map[string]any {
	values := runtimePlatformValues()
	controlPlane := values["control-plane"].(map[string]any)
	if os.Getenv("HARNESS_IMAGE_REPO") != "" && os.Getenv("HARNESS_IMAGE_TAG") != "" {
		controlPlane["dispatch"] = map[string]any{
			"enabled":      true,
			"defaultModel": map[string]any{"id": "e2e-model", "api": "openai"},
		}
	}
	if repository, tag := os.Getenv("TOOL_RUNNER_IMAGE_REPO"), os.Getenv("TOOL_RUNNER_IMAGE_TAG"); repository != "" && tag != "" {
		controlPlane["toolRunner"] = map[string]any{
			"enabled": true,
			"image":   map[string]any{"repository": repository, "tag": tag},
			"flux":    map[string]any{"namespace": testNamespace, "sourceName": "missing-e2e-source"},
		}
	}
	inference, _ := values["inference-gateway"].(map[string]any)
	if inference == nil {
		inference = map[string]any{}
		values["inference-gateway"] = inference
	}
	if os.Getenv("HARNESS_IMAGE_REPO") != "" && os.Getenv("HARNESS_IMAGE_TAG") != "" {
		inference["workload"] = map[string]any{"enabled": true}
	}
	return values
}

func installObservabilityHarnessStage(t *testing.T, state *chartState) {
	t.Helper()
	repository, tag := os.Getenv("HARNESS_IMAGE_REPO"), os.Getenv("HARNESS_IMAGE_TAG")
	if repository == "" || tag == "" {
		t.Log("HARNESS_IMAGE_REPO/TAG absent; candidate harness target is not part of this local fixture")
		return
	}
	manifest := fmt.Sprintf(`apiVersion: platform.iterabase.com/v1alpha1
kind: AgentPool
metadata:
  name: observability-e2e
  namespace: %s
spec:
  replicas: 1
  workerImage: %s:%s
  podSecurity: baseline
  identity:
    trustDomain: iterabase.local
    caSecretRef: {name: %s-control-plane-gateway-ca}
    certMountPath: /etc/harness/tls
  sandbox:
    storageClassName: standard
    accessMode: ReadWriteOnce
    size: 1Gi
    mountPath: /data/sandboxes
  gateways:
    controlPlane:
      url: https://%s-control-plane-dispatch:8091
      serverName: %s-control-plane-dispatch.%s.svc
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: control-plane, app.kubernetes.io/component: dispatch}}}
    toolGateway:
      url: https://%s-control-plane-gateway:8090
      serverName: %s-control-plane-gateway.%s.svc
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: control-plane, app.kubernetes.io/component: gateway}}}
    inferenceGateway:
      url: https://%s-inference-gateway:8443
      serverName: %s-inference-gateway.%s.svc
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: inference-gateway}}}
  networkPolicy: {egress: denied}
  workspaceTools: false
  piDirs: [/pi/product, /pi/client]
  walDir: /var/harness/wal
  probe: {port: 8081}
`, testNamespace, repository, tag, testRelease, testRelease, testRelease, testNamespace,
		testRelease, testRelease, testNamespace, testRelease, testRelease, testNamespace)
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "observability-agentpool.yaml", manifest))
	state.waitForPods(t, "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=harness", 7*time.Minute)
}

func filepathFromCharts(state *chartState, name string) string {
	return filepath.Join(state.chartsRoot, name)
}

func assertStackReadinessStage(t *testing.T, state *chartState) {
	t.Helper()
	for _, selector := range []string{
		"app.kubernetes.io/name=prometheus",
		"app.kubernetes.io/name=grafana",
		"app.kubernetes.io/name=loki,app.kubernetes.io/component=single-binary",
		"app.kubernetes.io/name=alertmanager",
		"app.kubernetes.io/name=promtail",
		"app.kubernetes.io/name=postgresql,app.kubernetes.io/component=exporter",
		"app.kubernetes.io/name=redis,app.kubernetes.io/component=exporter",
		"app.kubernetes.io/name=control-plane,app.kubernetes.io/component=api",
		"app.kubernetes.io/name=control-plane,app.kubernetes.io/component=manager",
		"app.kubernetes.io/name=control-plane,app.kubernetes.io/component=gateway",
		"app.kubernetes.io/name=inference-gateway",
	} {
		state.waitForPods(t, selector, 7*time.Minute)
	}
	if os.Getenv("HARNESS_IMAGE_REPO") != "" && os.Getenv("HARNESS_IMAGE_TAG") != "" {
		state.waitForPods(t, "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=dispatch", 7*time.Minute)
		state.waitForPods(t, "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=harness", 7*time.Minute)
	}
	if os.Getenv("TOOL_RUNNER_IMAGE_REPO") != "" && os.Getenv("TOOL_RUNNER_IMAGE_TAG") != "" {
		state.waitForPods(t, "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=tool-runner", 7*time.Minute)
	}
}

func assertGrafanaDashboardsStage(t *testing.T, state *chartState) {
	t.Helper()
	username := string(decodeSecretValue(t, state, testRelease+"-grafana", "admin-user"))
	password := string(decodeSecretValue(t, state, testRelease+"-grafana", "admin-password"))
	state.redactor.Add(username, password)
	forward := state.forward(t, "svc/"+testRelease+"-grafana", 80, "http")
	client := &http.Client{Timeout: 15 * time.Second}
	assertGrafanaDashboardSuite(t, client, forward.URL, username, password)
	state.stopForward(t, forward)
}

func assertGrafanaDashboardSuite(t *testing.T, client *http.Client, baseURL, username, password string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/search?type=dash-db&limit=500", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(username, password)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Grafana dashboard search status=%d", resp.StatusCode)
	}
	var found []struct {
		UID         string `json:"uid"`
		Title       string `json:"title"`
		FolderTitle string `json:"folderTitle"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&found); err != nil {
		t.Fatal(err)
	}
	type dashboardContract struct{ title, folder string }
	want := map[string]dashboardContract{
		"iterabase-platform-overview":         {"00 — Platform Overview", "Iterabase"},
		"iterabase-control-plane":             {"10 — Control Plane", "Iterabase"},
		"iterabase-execution-runtime":         {"20 — Execution Runtime", "Iterabase"},
		"iterabase-tool-runtime":              {"30 — Tool Runtime", "Iterabase"},
		"iterabase-inference-model-serving":   {"40 — Inference and Model Serving", "Iterabase"},
		"iterabase-data-storage":              {"50 — Data and Storage", "Iterabase"},
		"iterabase-platform-infrastructure":   {"60 — Platform Infrastructure", "Iterabase"},
		"iterabase-infrastructure-components": {"Infrastructure — Data, Edge and GPU", "Infrastructure"},
		"iterabase-observability-stack":       {"Observability — Metrics, Logs and Alerts", "Observability"},
	}
	for _, dashboard := range found {
		contract, ok := want[dashboard.UID]
		if ok && dashboard.Title == contract.title && dashboard.FolderTitle == contract.folder {
			delete(want, dashboard.UID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("Grafana missing organized platform dashboards: %v", want)
	}
}

func assertEndpointSeparationStage(t *testing.T, state *chartState) {
	t.Helper()
	// Project only membership fields so shared evidence redaction cannot rewrite
	// unrelated Pod configuration before the same JSON assertion used by the
	// intentional-break fixture consumes it.
	podFields := state.kubectl(t, 30*time.Second, "get", "pods", "-n", testNamespace,
		"-o", `jsonpath-as-json={.items[*].metadata['name','labels']}`)
	podsJSON, err := projectEndpointPodsJSON([]byte(podFields))
	if err != nil {
		t.Fatal(err)
	}
	sliceFields := state.kubectl(t, 30*time.Second, "get", "endpointslices", "-n", testNamespace,
		"-o", `jsonpath-as-json={.items[*]['metadata.labels','endpoints']}`)
	slicesJSON, err := projectEndpointSlicesJSON([]byte(sliceFields))
	if err != nil {
		t.Fatal(err)
	}
	if err := assertServiceEndpointsJSON(podsJSON, slicesJSON, testRelease); err != nil {
		t.Fatal(err)
	}
}

func assertMonitorDiscoveryStage(t *testing.T, state *chartState) {
	t.Helper()
	forward := state.forward(t, "svc/"+testRelease+"-kube-prometheus-prometheus", 9090, "http")
	client, err := httpx.Client(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"pg_up", "redis_up"} {
		if err := waitPrometheusValue(state.ctx, client, forward.URL, metric, "1", 5*time.Minute); err != nil {
			t.Fatalf("%s did not become 1: %v", metric, err)
		}
	}
	assertPlatformMetrics(t, state, client, forward.URL)
	body := requireHTTP(t, client, http.MethodGet, forward.URL+"/api/v1/targets?state=active", nil, http.StatusOK)
	targets := []string{"postgresql", "redis"}
	if os.Getenv("CONTROL_PLANE_IMAGE_REPO") != "" && os.Getenv("CONTROL_PLANE_IMAGE_TAG") != "" {
		targets = append(targets, "control-plane")
	}
	if os.Getenv("INFERENCE_GATEWAY_IMAGE_REPO") != "" && os.Getenv("INFERENCE_GATEWAY_IMAGE_TAG") != "" {
		targets = append(targets, "inference-gateway")
	}
	if err := assertDiscoveredTargets(body, targets, false); err != nil {
		t.Fatal(err)
	}
	rules := string(requireHTTP(t, client, http.MethodGet, forward.URL+"/api/v1/rules", nil, http.StatusOK))
	for _, alert := range []string{"IterabasePlatformTargetDown", "IterabaseGatewayOutcomeUnknown", "IterabaseDispatchWithoutWorkers", "IterabaseInferenceGatewayHighErrorRate"} {
		if !strings.Contains(rules, alert) {
			t.Fatalf("Prometheus did not load shipped alert %s", alert)
		}
	}
	state.stopForward(t, forward)
}

func assertPrometheusPersistenceStage(t *testing.T, state *chartState) {
	t.Helper()
	client, err := httpx.Client(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	forward := state.forward(t, "svc/"+testRelease+"-kube-prometheus-prometheus", 9090, "http")
	timestamp, value, err := prometheusInstantSample(client, forward.URL, `up{job="postgresql"}`)
	if err != nil || value != "1" {
		t.Fatalf("capture pre-restart PostgreSQL sample: timestamp=%v value=%q err=%v", timestamp, value, err)
	}
	// Stop the producer before the restart. The bounded historical interval ends
	// at the captured timestamp, so a fresh post-restart scrape cannot satisfy it.
	state.kubectl(t, 2*time.Minute, "scale", "deployment/"+testRelease+"-postgresql-exporter", "-n", testNamespace, "--replicas=0")
	state.kubectl(t, 3*time.Minute, "rollout", "status", "deployment/"+testRelease+"-postgresql-exporter", "-n", testNamespace, "--timeout=2m")
	state.stopForward(t, forward)
	pod := state.firstPod(t, "app.kubernetes.io/name=prometheus")
	state.kubectl(t, 2*time.Minute, "delete", "pod", pod, "-n", testNamespace, "--wait=true")
	state.waitForPods(t, "app.kubernetes.io/name=prometheus", 6*time.Minute)
	forward = state.forward(t, "svc/"+testRelease+"-kube-prometheus-prometheus", 9090, "http")
	query := url.Values{
		"query": {`up{job="postgresql"}`},
		"start": {strconv.FormatFloat(timestamp-30, 'f', 3, 64)},
		"end":   {strconv.FormatFloat(timestamp+0.001, 'f', 3, 64)},
		"step":  {"1"},
	}
	body := requireHTTP(t, client, http.MethodGet, forward.URL+"/api/v1/query_range?"+query.Encode(), nil, http.StatusOK)
	if err := assertHistoricalPrometheusSample(body, timestamp+0.001, "1"); err != nil {
		t.Fatalf("pre-restart Prometheus sample did not survive: %v", err)
	}
	state.stopForward(t, forward)
}

func assertLokiPersistenceStage(t *testing.T, state *chartState) {
	t.Helper()
	client, err := httpx.Client(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("lokipersist%d", time.Now().UnixNano())
	emitter := "log-emitter-" + marker
	started := time.Now().Add(-time.Minute)
	state.kubectl(t, 30*time.Second, "run", emitter, "-n", testNamespace, "--image=busybox:1.37.0", "--restart=Never", "--",
		"sh", "-c", fmt.Sprintf("i=0; while [ $i -lt 20 ]; do echo %s; sleep 1; i=$((i+1)); done", marker))
	state.kubectl(t, 2*time.Minute, "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/"+emitter, "-n", testNamespace, "--timeout=90s")
	ended := time.Now().Add(time.Minute)
	forward := state.forward(t, "svc/"+testRelease+"-loki", 3100, "http")
	queryURL := lokiMarkerURL(forward.URL, emitter, marker, started, ended)
	if err := waitMarker(state.ctx, client, queryURL, marker, 4*time.Minute); err != nil {
		t.Fatalf("Promtail did not ingest stopped producer marker: %v", err)
	}
	state.kubectl(t, 30*time.Second, "delete", "pod", emitter, "-n", testNamespace, "--wait=true")
	state.stopForward(t, forward)
	pod := state.firstPod(t, "app.kubernetes.io/name=loki,app.kubernetes.io/component=single-binary")
	state.kubectl(t, 2*time.Minute, "delete", "pod", pod, "-n", testNamespace, "--wait=true")
	state.waitForPods(t, "app.kubernetes.io/name=loki,app.kubernetes.io/component=single-binary", 6*time.Minute)
	forward = state.forward(t, "svc/"+testRelease+"-loki", 3100, "http")
	body := requireHTTP(t, client, http.MethodGet, lokiMarkerURL(forward.URL, emitter, marker, started, ended), nil, http.StatusOK)
	if !strings.Contains(string(body), marker) {
		t.Fatalf("pre-restart Loki marker missing from fixed historical interval: %s", stateSafeBody(body))
	}
	state.stopForward(t, forward)
}

func assertPlatformMetrics(t *testing.T, state *chartState, client *http.Client, prometheusURL string) {
	t.Helper()
	queries := []string{}
	if os.Getenv("CONTROL_PLANE_IMAGE_REPO") != "" && os.Getenv("CONTROL_PLANE_IMAGE_TAG") != "" {
		queries = append(queries,
			`count(up{job="control-plane",component="api"} == 1)`,
			`count(up{job="control-plane",component="manager"} == 1)`,
			`count(up{job="control-plane",component="gateway"} == 1)`,
			`count(control_plane_build_info{component="api"})`,
			`count(control_plane_build_info{component="gateway"})`,
			`count(go_goroutines{job="control-plane",component="manager"})`,
		)
	}
	if os.Getenv("INFERENCE_GATEWAY_IMAGE_REPO") != "" && os.Getenv("INFERENCE_GATEWAY_IMAGE_TAG") != "" {
		queries = append(queries,
			`count(up{job="inference-gateway",component="inference-gateway"} == 1)`,
			`count(inference_gateway_build_info)`,
		)
	}
	if os.Getenv("HARNESS_IMAGE_REPO") != "" && os.Getenv("HARNESS_IMAGE_TAG") != "" {
		queries = append(queries,
			`count(up{job="control-plane",component="dispatch"} == 1)`,
			`count(up{job="control-plane",component="harness"} == 1)`,
			`count(control_plane_harness_build_info)`,
		)
	}
	if os.Getenv("TOOL_RUNNER_IMAGE_REPO") != "" && os.Getenv("TOOL_RUNNER_IMAGE_TAG") != "" {
		queries = append(queries,
			`count(up{job="control-plane",component="tool-runner"} == 1)`,
			`count(up{job="control-plane",component="tool-materializer"} == 1)`,
		)
	}
	for _, query := range queries {
		if err := waitPrometheusValue(state.ctx, client, prometheusURL, query, "1", 5*time.Minute); err != nil {
			t.Fatalf("representative platform metric %s did not become queryable: %v", query, err)
		}
	}
}

func waitPrometheusValue(ctx context.Context, client *http.Client, baseURL, query, want string, timeout time.Duration) error {
	return poll.Until(ctx, timeout, 5*time.Second, func(context.Context) (bool, string, error) {
		return observePrometheusValue(client, baseURL, query, want)
	})
}

func observePrometheusValue(client *http.Client, baseURL, query, want string) (bool, string, error) {
	_, value, err := prometheusInstantSample(client, baseURL, query)
	if errors.Is(err, errPrometheusNoSample) {
		return false, "query returned no sample", nil
	}
	if err != nil {
		return false, "query failed", err
	}
	return value == want, "value=" + value, nil
}

var errPrometheusNoSample = errors.New("query returned no sample")

func prometheusInstantSample(client *http.Client, baseURL, query string) (float64, string, error) {
	endpoint := baseURL + "/api/v1/query?" + url.Values{"query": {query}}.Encode()
	resp, err := client.Get(endpoint)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var payload struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, "", err
	}
	if payload.Status != "success" {
		return 0, "", fmt.Errorf("query status=%q", payload.Status)
	}
	if len(payload.Data) == 0 {
		return 0, "", fmt.Errorf("query response missing data")
	}
	var data struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return 0, "", fmt.Errorf("decode query data: %w", err)
	}
	if data.ResultType != "vector" {
		return 0, "", fmt.Errorf("query resultType=%q, want %q", data.ResultType, "vector")
	}
	if len(data.Result) == 0 {
		return 0, "", fmt.Errorf("query response missing result")
	}
	var result []struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data.Result, &result); err != nil {
		return 0, "", fmt.Errorf("decode query result: %w", err)
	}
	if result == nil {
		return 0, "", fmt.Errorf("query result must be an array")
	}
	if len(result) == 0 {
		return 0, "", errPrometheusNoSample
	}
	if len(result[0].Value) != 2 {
		return 0, "", fmt.Errorf("query returned malformed sample")
	}
	var timestamp *float64
	var value *string
	if err := json.Unmarshal(result[0].Value[0], &timestamp); err != nil {
		return 0, "", fmt.Errorf("decode query timestamp: %w", err)
	}
	if timestamp == nil {
		return 0, "", fmt.Errorf("query timestamp must be a number")
	}
	if err := json.Unmarshal(result[0].Value[1], &value); err != nil {
		return 0, "", fmt.Errorf("decode query value: %w", err)
	}
	if value == nil {
		return 0, "", fmt.Errorf("query value must be a string")
	}
	return *timestamp, *value, nil
}

func lokiMarkerURL(baseURL, pod, marker string, start, end time.Time) string {
	query := fmt.Sprintf(`{pod=%q} |= %q`, pod, marker)
	values := url.Values{
		"query":     {query},
		"start":     {strconv.FormatInt(start.UnixNano(), 10)},
		"end":       {strconv.FormatInt(end.UnixNano(), 10)},
		"direction": {"forward"},
		"limit":     {"20"},
	}
	return baseURL + "/loki/api/v1/query_range?" + values.Encode()
}

func waitMarker(ctx context.Context, client *http.Client, endpoint, marker string, timeout time.Duration) error {
	return poll.Until(ctx, timeout, 5*time.Second, func(context.Context) (bool, string, error) {
		resp, err := client.Get(endpoint)
		if err != nil {
			return false, "query failed", err
		}
		defer func() { _ = resp.Body.Close() }()
		var payload json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return false, "decode failed", err
		}
		found := strings.Contains(string(payload), marker)
		return found, fmt.Sprintf("marker found=%t", found), nil
	})
}
