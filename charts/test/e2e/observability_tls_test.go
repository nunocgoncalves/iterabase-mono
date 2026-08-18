package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/poll"
)

func observabilityTLSScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: chartScenarioMetadata(
			"observability-tls",
			"Proves the observability stack, exporters, self-monitors, Grafana datasources/sidecars, Loki gateway, Promtail, and Alertmanager use verified internal-CA HTTPS identities.",
			"test-e2e-observability-tls", 45,
			[]string{"HOR-408", "HOR-414", "HOR-418", "HOR-420", "HOR-416"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"create-kind"}, Run: installCertificateSubstrateStage},
			{Name: "install-tool-source", DependsOn: []string{"install-certificate-substrate"}, Run: installObservabilityToolSourceStage},
			{Name: "install-observability-tls", DependsOn: []string{"install-tool-source"}, Run: installObservabilityTLSStage},
			{Name: "install-harness-worker", DependsOn: []string{"install-observability-tls"}, Run: installObservabilityHarnessStage},
			{Name: "assert-stack-readiness", DependsOn: []string{"install-harness-worker"}, Run: assertStackReadinessStage},
			{Name: "assert-issued-identities", DependsOn: []string{"assert-stack-readiness"}, Run: assertObservabilityIdentitiesStage},
			{Name: "assert-verified-stack-https", DependsOn: []string{"assert-issued-identities"}, Run: assertVerifiedStackHTTPSStage},
			{Name: "assert-tls-endpoint-separation", DependsOn: []string{"assert-stack-readiness"}, Run: assertEndpointSeparationStage},
			{Name: "assert-exporter-client-paths", DependsOn: []string{"assert-verified-stack-https"}, Run: assertTLSExporterPathsStage},
			{Name: "assert-verified-self-monitors", DependsOn: []string{"assert-verified-stack-https"}, Run: assertVerifiedSelfMonitorsStage},
			{Name: "assert-grafana-datasources-sidecars", DependsOn: []string{"assert-verified-stack-https"}, Run: assertGrafanaTLSPathsStage},
			{Name: "assert-loki-gateway", DependsOn: []string{"assert-verified-stack-https"}, Run: assertLokiGatewayTLSStage},
			{Name: "assert-promtail-loki", DependsOn: []string{"assert-verified-stack-https"}, Run: assertTLSPromtailPathStage},
			{Name: "assert-prometheus-alertmanager", DependsOn: []string{"assert-verified-stack-https"}, Run: assertTLSAlertmanagerPathStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func installObservabilityTLSStage(t *testing.T, state *chartState) {
	t.Helper()
	state.installPlatform(t, 22*time.Minute,
		filepathFromCharts(state, "values-observability.yaml"),
		filepathFromCharts(state, "values-tls.yaml"),
		state.writeValues(t, "observability-tls-runtime", observabilityPlatformValues()),
	)
	assertCandidateImages(t, state)
}

func assertObservabilityIdentitiesStage(t *testing.T, state *chartState) {
	t.Helper()
	state.kubectl(t, 4*time.Minute, "wait", "--for=condition=Ready", "clusterissuer/internal-ca", "--timeout=3m")
	for _, certificate := range []string{
		"observability-prometheus-tls", "observability-alertmanager-tls", "observability-grafana-tls", "observability-loki-tls",
	} {
		state.kubectl(t, 4*time.Minute, "wait", "--for=condition=Ready", "certificate/"+certificate, "-n", testNamespace, "--timeout=3m")
	}
}

func assertVerifiedStackHTTPSStage(t *testing.T, state *chartState) {
	t.Helper()
	ca := decodeSecretValue(t, state, testRelease+"-internal-ca-root", "ca.crt")
	checks := []struct {
		service    string
		port       int
		serverName string
		path       string
	}{
		{testRelease + "-kube-prometheus-prometheus", 9090, testRelease + "-kube-prometheus-prometheus." + testNamespace + ".svc", "/-/healthy"},
		{testRelease + "-kube-prometheus-alertmanager", 9093, testRelease + "-kube-prometheus-alertmanager." + testNamespace + ".svc", "/-/healthy"},
		{testRelease + "-grafana", 80, testRelease + "-grafana." + testNamespace + ".svc", "/api/health"},
		{testRelease + "-loki", 3100, testRelease + "-loki." + testNamespace + ".svc", "/ready"},
	}
	for _, check := range checks {
		forward := state.forward(t, "svc/"+check.service, check.port, "https")
		client := verifiedClient(t, ca, check.serverName)
		if err := waitHTTPReady(state.ctx, client, forward.URL+check.path, 2*time.Minute); err != nil {
			t.Fatalf("verified HTTPS %s: %v", check.service, err)
		}
		requireHTTP(t, client, http.MethodGet, forward.URL+check.path, nil, http.StatusOK)
		state.stopForward(t, forward)
	}
}

func assertTLSExporterPathsStage(t *testing.T, state *chartState) {
	t.Helper()
	ca := decodeSecretValue(t, state, testRelease+"-internal-ca-root", "ca.crt")
	forward := state.forward(t, "svc/"+testRelease+"-kube-prometheus-prometheus", 9090, "https")
	client := verifiedClient(t, ca, testRelease+"-kube-prometheus-prometheus."+testNamespace+".svc")
	for _, metric := range []string{"pg_up", "redis_up"} {
		if err := waitPrometheusValue(state.ctx, client, forward.URL, metric, "1", 5*time.Minute); err != nil {
			t.Fatalf("%s did not become 1 over verified Prometheus HTTPS: %v", metric, err)
		}
	}
	assertPlatformMetrics(t, state, client, forward.URL)
	state.stopForward(t, forward)
}

func assertVerifiedSelfMonitorsStage(t *testing.T, state *chartState) {
	t.Helper()
	ca := decodeSecretValue(t, state, testRelease+"-internal-ca-root", "ca.crt")
	forward := state.forward(t, "svc/"+testRelease+"-kube-prometheus-prometheus", 9090, "https")
	client := verifiedClient(t, ca, testRelease+"-kube-prometheus-prometheus."+testNamespace+".svc")
	var last []byte
	err := poll.Until(state.ctx, 5*time.Minute, 5*time.Second, func(context.Context) (bool, string, error) {
		req, requestErr := http.NewRequestWithContext(state.ctx, http.MethodGet, forward.URL+"/api/v1/targets?state=active", nil)
		if requestErr != nil {
			return false, "build request", requestErr
		}
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			return false, "query targets", requestErr
		}
		defer func() { _ = resp.Body.Close() }()
		var payload json.RawMessage
		if decodeErr := json.NewDecoder(resp.Body).Decode(&payload); decodeErr != nil {
			return false, "decode targets", decodeErr
		}
		last = payload
		assertErr := assertDiscoveredTargets(last, []string{
			"prometheus-internal-tls", "alertmanager-internal-tls", "grafana-internal-tls", "loki-internal-tls",
		}, true)
		if assertErr != nil {
			return false, assertErr.Error(), nil
		}
		return true, "all verified stack targets are up", nil
	})
	if err != nil {
		t.Fatalf("verified stack self-monitors did not converge: %v\n%s", err, stateSafeBody(last))
	}
	if err := assertDiscoveredTargets(last, []string{
		"prometheus-internal-tls", "alertmanager-internal-tls", "grafana-internal-tls", "loki-internal-tls",
	}, true); err != nil {
		t.Fatal(err)
	}
	state.stopForward(t, forward)
}

func assertGrafanaTLSPathsStage(t *testing.T, state *chartState) {
	t.Helper()
	ca := decodeSecretValue(t, state, testRelease+"-internal-ca-root", "ca.crt")
	username := string(decodeSecretValue(t, state, testRelease+"-grafana", "admin-user"))
	password := string(decodeSecretValue(t, state, testRelease+"-grafana", "admin-password"))
	state.redactor.Add(username, password)
	forward := state.forward(t, "svc/"+testRelease+"-grafana", 80, "https")
	client := verifiedClient(t, ca, testRelease+"-grafana."+testNamespace+".svc")
	assertGrafanaDashboardSuite(t, client, forward.URL, username, password)
	for _, datasource := range []struct {
		uid, healthPath string
	}{
		{"prometheus", "/api/v1/status/buildinfo"},
		{"alertmanager", "/api/v2/status"},
		{"loki", "/ready"},
	} {
		// Exercise the actual upstream health API through Grafana's datasource
		// proxy. This proves URL, Service identity, CA trust, and connectivity;
		// not every built-in datasource plugin implements Grafana's optional
		// plugin-health endpoint.
		endpoint := forward.URL + "/api/datasources/proxy/uid/" + datasource.uid + datasource.healthPath
		err := poll.Until(state.ctx, 3*time.Minute, 4*time.Second, func(context.Context) (bool, string, error) {
			req, requestErr := http.NewRequestWithContext(state.ctx, http.MethodGet, endpoint, nil)
			if requestErr != nil {
				return false, "build request", requestErr
			}
			req.SetBasicAuth(username, password)
			resp, requestErr := client.Do(req)
			if requestErr != nil {
				return false, "datasource request", requestErr
			}
			_ = resp.Body.Close()
			return resp.StatusCode == http.StatusOK, fmt.Sprintf("status=%d", resp.StatusCode), nil
		})
		if err != nil {
			t.Fatalf("Grafana datasource %s did not pass its upstream health API: %v", datasource.uid, err)
		}
	}
	state.stopForward(t, forward)

	// Force both sidecars to process an update, then require a successful reload
	// and reject TLS verification/handshake errors in their retained logs.
	stamp := fmt.Sprintf("%d", time.Now().UnixNano())
	for _, configMap := range []string{testRelease + "-iterabase-datasources-tls", testRelease + "-loki-datasource"} {
		state.kubectl(t, 30*time.Second, "annotate", "configmap/"+configMap, "-n", testNamespace, "e2e.iterabase.com/reload="+stamp, "--overwrite")
	}
	dashboards := strings.Fields(state.kubectl(t, 30*time.Second, "get", "configmap", "-n", testNamespace,
		"-l", "grafana_dashboard=1", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`))
	if len(dashboards) == 0 {
		t.Fatal("no dashboard ConfigMap available to trigger the dashboard sidecar")
	}
	state.kubectl(t, 30*time.Second, "annotate", "configmap/"+dashboards[0], "-n", testNamespace,
		"e2e.iterabase.com/reload="+stamp, "--overwrite")
	for _, container := range []string{"grafana-sc-datasources", "grafana-sc-dashboard"} {
		var logs string
		err := poll.Until(state.ctx, 2*time.Minute, 3*time.Second, func(context.Context) (bool, string, error) {
			out, observeErr := state.client.Kubectl(state.ctx, 30*time.Second, "logs", "statefulset/"+testRelease+"-grafana", "-n", testNamespace, "-c", container, "--tail=200")
			if observeErr != nil {
				return false, "read sidecar logs", observeErr
			}
			logs = out
			if strings.Contains(logs, "Response: 200") {
				return true, "successful reload observed", nil
			}
			return false, "no successful reload response yet", nil
		})
		if err != nil {
			t.Fatalf("%s did not complete verified reload: %v\n%s", container, err, logs)
		}
		assertNoTLSFailure(t, container, logs)
	}
}

func assertLokiGatewayTLSStage(t *testing.T, state *chartState) {
	t.Helper()
	forward := state.forward(t, "svc/"+testRelease+"-loki-gateway", 80, "http")
	client := &http.Client{Timeout: 15 * time.Second}
	if err := waitHTTPReady(state.ctx, client, forward.URL+"/loki/api/v1/labels", 2*time.Minute); err != nil {
		t.Fatalf("Loki gateway did not reach TLS backend: %v", err)
	}
	state.stopForward(t, forward)
	logs := state.kubectl(t, 30*time.Second, "logs", "deployment/"+testRelease+"-loki-gateway", "-n", testNamespace, "--tail=300")
	assertNoTLSFailure(t, "Loki gateway", logs)
}

func assertTLSPromtailPathStage(t *testing.T, state *chartState) {
	t.Helper()
	ca := decodeSecretValue(t, state, testRelease+"-internal-ca-root", "ca.crt")
	marker := fmt.Sprintf("tlslogpath%d", time.Now().UnixNano())
	emitter := "tls-log-emitter-" + marker
	started := time.Now().Add(-time.Minute)
	state.kubectl(t, 30*time.Second, "run", emitter, "-n", testNamespace, "--image=busybox:1.37.0", "--restart=Never", "--",
		"sh", "-c", fmt.Sprintf("i=0; while [ $i -lt 15 ]; do echo %s; sleep 1; i=$((i+1)); done", marker))
	state.kubectl(t, 2*time.Minute, "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/"+emitter, "-n", testNamespace, "--timeout=90s")
	forward := state.forward(t, "svc/"+testRelease+"-loki", 3100, "https")
	client := verifiedClient(t, ca, testRelease+"-loki."+testNamespace+".svc")
	if err := waitMarker(state.ctx, client, lokiMarkerURL(forward.URL, emitter, marker, started, time.Now().Add(time.Minute)), marker, 4*time.Minute); err != nil {
		t.Fatalf("Promtail did not deliver to verified HTTPS Loki: %v", err)
	}
	state.stopForward(t, forward)
	state.kubectl(t, 30*time.Second, "delete", "pod", emitter, "-n", testNamespace, "--wait=true")
	logs := state.kubectl(t, 30*time.Second, "logs", "daemonset/"+testRelease+"-promtail", "-n", testNamespace, "--tail=300")
	assertNoTLSFailure(t, "Promtail", logs)
}

func assertTLSAlertmanagerPathStage(t *testing.T, state *chartState) {
	t.Helper()
	manifest := `apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: iterabase-tls-path-probe
  namespace: iterabase-system
spec:
  groups:
    - name: iterabase-tls-path-probe
      interval: 5s
      rules:
        - alert: IterabaseTLSPathProbe
          expr: vector(1)
          labels:
            severity: none
          annotations:
            summary: TLS E2E path probe
`
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "tls-alert.yaml", manifest))
	ca := decodeSecretValue(t, state, testRelease+"-internal-ca-root", "ca.crt")
	forward := state.forward(t, "svc/"+testRelease+"-kube-prometheus-alertmanager", 9093, "https")
	client := verifiedClient(t, ca, testRelease+"-kube-prometheus-alertmanager."+testNamespace+".svc")
	err := poll.Until(state.ctx, 5*time.Minute, 5*time.Second, func(context.Context) (bool, string, error) {
		resp, requestErr := client.Get(forward.URL + "/api/v2/alerts")
		if requestErr != nil {
			return false, "query alerts", requestErr
		}
		defer func() { _ = resp.Body.Close() }()
		var alerts []struct {
			Labels map[string]string `json:"labels"`
		}
		if decodeErr := json.NewDecoder(resp.Body).Decode(&alerts); decodeErr != nil {
			return false, "decode alerts", decodeErr
		}
		for _, alert := range alerts {
			if alert.Labels["alertname"] == "IterabaseTLSPathProbe" {
				return true, "probe delivered", nil
			}
		}
		return false, "probe not delivered", nil
	})
	if err != nil {
		t.Fatalf("Prometheus did not deliver to verified HTTPS Alertmanager: %v", err)
	}
	state.stopForward(t, forward)
}

func assertNoTLSFailure(t *testing.T, component, logs string) {
	t.Helper()
	lower := strings.ToLower(logs)
	for _, marker := range []string{"certificate verify failed", "tls handshake error", "server gave http response to https client", "client sent an http request to an https server"} {
		if strings.Contains(lower, marker) {
			t.Fatalf("%s logs contain %q:\n%s", component, marker, logs)
		}
	}
}
