package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
			[]string{"HOR-408", "HOR-418", "HOR-416"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"create-kind"}, Run: installCertificateSubstrateStage},
			{Name: "install-observability", DependsOn: []string{"install-certificate-substrate"}, Run: installObservabilityStage},
			{Name: "assert-stack-readiness", DependsOn: []string{"install-observability"}, Run: assertStackReadinessStage},
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
	values := runtimePlatformValues()
	state.installPlatform(t, 20*time.Minute,
		filepathFromCharts(state, "values-observability.yaml"),
		state.writeValues(t, "observability-runtime", values),
	)
	assertCandidateImages(t, state)
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
	} {
		state.waitForPods(t, selector, 7*time.Minute)
	}
}

func assertEndpointSeparationStage(t *testing.T, state *chartState) {
	t.Helper()
	checks := []struct {
		name, component, service string
	}{
		{"postgresql", "database", testRelease + "-postgresql"},
		{"postgresql", "exporter", testRelease + "-postgresql-exporter"},
		{"redis", "cache", testRelease + "-redis"},
		{"redis", "exporter", testRelease + "-redis-exporter"},
	}
	sets := make(map[string][]string)
	for _, check := range checks {
		selector := fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s,app.kubernetes.io/component=%s", check.name, testRelease, check.component)
		expected := strings.Fields(state.kubectl(t, 30*time.Second, "get", "pods", "-n", testNamespace, "-l", selector,
			"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`))
		actual := strings.Fields(state.kubectl(t, 30*time.Second, "get", "endpointslices", "-n", testNamespace,
			"-l", "kubernetes.io/service-name="+check.service,
			"-o", `jsonpath={range .items[*].endpoints[*]}{.targetRef.name}{"\n"}{end}`))
		if err := assertEndpointSet(check.service, expected, actual); err != nil {
			t.Fatal(err)
		}
		sets[check.service] = actual
	}
	if err := assertDisjointEndpointSets(testRelease+"-postgresql", sets[testRelease+"-postgresql"], testRelease+"-postgresql-exporter", sets[testRelease+"-postgresql-exporter"]); err != nil {
		t.Fatal(err)
	}
	if err := assertDisjointEndpointSets(testRelease+"-redis", sets[testRelease+"-redis"], testRelease+"-redis-exporter", sets[testRelease+"-redis-exporter"]); err != nil {
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
	body := requireHTTP(t, client, http.MethodGet, forward.URL+"/api/v1/targets?state=active", nil, http.StatusOK)
	if err := assertDiscoveredTargets(body, []string{"postgresql", "redis"}, false); err != nil {
		t.Fatal(err)
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

func waitPrometheusValue(ctx context.Context, client *http.Client, baseURL, query, want string, timeout time.Duration) error {
	return poll.Until(ctx, timeout, 5*time.Second, func(context.Context) (bool, string, error) {
		_, value, err := prometheusInstantSample(client, baseURL, query)
		if err != nil {
			return false, "query failed", err
		}
		return value == want, "value=" + value, nil
	})
}

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
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, "", err
	}
	if payload.Status != "success" || len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Value) != 2 {
		return 0, "", fmt.Errorf("query returned no sample")
	}
	var timestamp float64
	var value string
	if err := json.Unmarshal(payload.Data.Result[0].Value[0], &timestamp); err != nil {
		return 0, "", err
	}
	if err := json.Unmarshal(payload.Data.Result[0].Value[1], &value); err != nil {
		return 0, "", err
	}
	return timestamp, value, nil
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
