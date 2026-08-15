package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

type endpointPod struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type endpointSlice struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Endpoints []struct {
		TargetRef struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"targetRef"`
	} `json:"endpoints"`
}

func projectEndpointPodsJSON(fieldsJSON []byte) ([]byte, error) {
	var fields []json.RawMessage
	if err := json.Unmarshal(fieldsJSON, &fields); err != nil {
		return nil, fmt.Errorf("decode projected pod fields: %w", err)
	}
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("projected pod fields have odd length %d", len(fields))
	}
	fieldSetSize := len(fields) / 2
	pods := make([]endpointPod, 0, fieldSetSize)
	for index := 0; index < fieldSetSize; index++ {
		var pod endpointPod
		if err := json.Unmarshal(fields[index], &pod.Name); err != nil {
			return nil, fmt.Errorf("decode projected pod name: %w", err)
		}
		if err := json.Unmarshal(fields[index+fieldSetSize], &pod.Labels); err != nil {
			return nil, fmt.Errorf("decode projected pod labels: %w", err)
		}
		pods = append(pods, pod)
	}
	return json.Marshal(pods)
}

func projectEndpointSlicesJSON(fieldsJSON []byte) ([]byte, error) {
	var fields []json.RawMessage
	if err := json.Unmarshal(fieldsJSON, &fields); err != nil {
		return nil, fmt.Errorf("decode projected EndpointSlice fields: %w", err)
	}
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("projected EndpointSlice fields have odd length %d", len(fields))
	}
	fieldSetSize := len(fields) / 2
	endpointSlices := make([]endpointSlice, 0, fieldSetSize)
	for index := 0; index < fieldSetSize; index++ {
		var endpointSlice endpointSlice
		if err := json.Unmarshal(fields[index], &endpointSlice.Metadata.Labels); err != nil {
			return nil, fmt.Errorf("decode projected EndpointSlice labels: %w", err)
		}
		if err := json.Unmarshal(fields[index+fieldSetSize], &endpointSlice.Endpoints); err != nil {
			return nil, fmt.Errorf("decode projected EndpointSlice endpoints: %w", err)
		}
		endpointSlices = append(endpointSlices, endpointSlice)
	}
	return json.Marshal(endpointSlices)
}

func assertServiceEndpointsJSON(podsJSON, slicesJSON []byte, release string) error {
	var pods []endpointPod
	var endpointSlices []endpointSlice
	if err := json.Unmarshal(podsJSON, &pods); err != nil {
		return fmt.Errorf("decode pod metadata: %w", err)
	}
	if err := json.Unmarshal(slicesJSON, &endpointSlices); err != nil {
		return fmt.Errorf("decode EndpointSlices: %w", err)
	}
	selectedPods := func(name, component string) []string {
		var selected []string
		for _, pod := range pods {
			if pod.Labels["app.kubernetes.io/name"] == name && pod.Labels["app.kubernetes.io/instance"] == release && pod.Labels["app.kubernetes.io/component"] == component {
				selected = append(selected, pod.Name)
			}
		}
		sort.Strings(selected)
		return selected
	}
	endpointPods := func(service string) []string {
		var selected []string
		for _, slice := range endpointSlices {
			if slice.Metadata.Labels["kubernetes.io/service-name"] != service {
				continue
			}
			for _, endpoint := range slice.Endpoints {
				if endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.Name != "" {
					selected = append(selected, endpoint.TargetRef.Name)
				}
			}
		}
		sort.Strings(selected)
		return selected
	}
	checks := []struct {
		name, component, service string
	}{
		{"postgresql", "database", release + "-postgresql"},
		{"postgresql", "exporter", release + "-postgresql-exporter"},
		{"redis", "cache", release + "-redis"},
		{"redis", "exporter", release + "-redis-exporter"},
	}
	sets := make(map[string][]string)
	for _, check := range checks {
		expected := selectedPods(check.name, check.component)
		actual := endpointPods(check.service)
		if len(expected) == 0 {
			return fmt.Errorf("%s: no component=%s pods found", check.service, check.component)
		}
		if !slices.Equal(actual, expected) {
			return fmt.Errorf("%s endpoints %v != expected %v", check.service, actual, expected)
		}
		sets[check.service] = actual
	}
	for _, pair := range [][2]string{{release + "-postgresql", release + "-postgresql-exporter"}, {release + "-redis", release + "-redis-exporter"}} {
		for _, dataPod := range sets[pair[0]] {
			if slices.Contains(sets[pair[1]], dataPod) {
				return fmt.Errorf("%s and %s overlap on %s", pair[0], pair[1], dataPod)
			}
		}
	}
	return nil
}

func assertHistoricalPrometheusSample(body []byte, intervalEnd float64, want string) error {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode query_range: %w", err)
	}
	if payload.Status != "success" {
		return fmt.Errorf("query status=%q", payload.Status)
	}
	for _, result := range payload.Data.Result {
		for _, sample := range result.Values {
			if len(sample) != 2 {
				continue
			}
			var timestamp float64
			var value string
			if json.Unmarshal(sample[0], &timestamp) != nil || json.Unmarshal(sample[1], &value) != nil {
				continue
			}
			if timestamp <= intervalEnd && value == want {
				return nil
			}
		}
	}
	return fmt.Errorf("no value=%s sample at or before bounded interval end %.3f", want, intervalEnd)
}

func assertDiscoveredTargets(body []byte, names []string, requireHTTPS bool) error {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Active []struct {
				ScrapePool string            `json:"scrapePool"`
				ScrapeURL  string            `json:"scrapeUrl"`
				Health     string            `json:"health"`
				Labels     map[string]string `json:"labels"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode Prometheus targets: %w", err)
	}
	if payload.Status != "success" {
		return fmt.Errorf("targets status=%q", payload.Status)
	}
	for _, name := range names {
		matched := false
		verified := false
		for _, target := range payload.Data.Active {
			identity := target.ScrapePool + " " + target.ScrapeURL + " " + target.Labels["job"] + " " + target.Labels["service"]
			if !strings.Contains(identity, name) {
				continue
			}
			matched = true
			if requireHTTPS && !strings.HasPrefix(target.ScrapeURL, "https://") {
				// Prometheus and Alertmanager expose an independent plaintext
				// config-reloader metrics port in the same ServiceMonitor.
				continue
			}
			if target.Health != "up" {
				return fmt.Errorf("target %s health=%s (%s)", name, target.Health, identity)
			}
			verified = true
		}
		if !matched {
			return fmt.Errorf("no active target matched %q", name)
		}
		if requireHTTPS && !verified {
			return fmt.Errorf("no verified HTTPS target matched %q", name)
		}
	}
	return nil
}

func TestUnitPublishedPlatformVersionUsesOCITag(t *testing.T) {
	fixture := `{"mode":"published","inputs":[{"name":"iterabase-platform","kind":"published-chart","reference":"oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform:0.3.1"}]}`
	path := t.TempDir() + "/fixture.json"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ITERABASE_E2E_PUBLISHED_FIXTURE", path)
	if got := publishedPlatformVersion(t); got != "0.3.1" {
		t.Fatalf("published version=%q want=0.3.1", got)
	}
}

func TestUnitProjectedEndpointJSONFeedsSharedAssertion(t *testing.T) {
	podFields := `[
		"pg", "pg-exp", "redis", "redis-exp",
		{"app.kubernetes.io/name":"postgresql","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"database"},
		{"app.kubernetes.io/name":"postgresql","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"exporter"},
		{"app.kubernetes.io/name":"redis","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"cache"},
		{"app.kubernetes.io/name":"redis","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"exporter"}
	]`
	sliceFields := `[
		{"kubernetes.io/service-name":"iterabase-postgresql"},
		{"kubernetes.io/service-name":"iterabase-postgresql-exporter"},
		{"kubernetes.io/service-name":"iterabase-redis"},
		{"kubernetes.io/service-name":"iterabase-redis-exporter"},
		[{"targetRef":{"kind":"Pod","name":"pg"}}],
		[{"targetRef":{"kind":"Pod","name":"pg-exp"}}],
		[{"targetRef":{"kind":"Pod","name":"redis"}}],
		[{"targetRef":{"kind":"Pod","name":"redis-exp"}}]
	]`
	podsJSON, err := projectEndpointPodsJSON([]byte(podFields))
	if err != nil {
		t.Fatal(err)
	}
	slicesJSON, err := projectEndpointSlicesJSON([]byte(sliceFields))
	if err != nil {
		t.Fatal(err)
	}
	if err := assertServiceEndpointsJSON(podsJSON, slicesJSON, "iterabase"); err != nil {
		t.Fatal(err)
	}
}

func TestUnitEndpointSeparationRejectsExporterLeak(t *testing.T) {
	pods := `[
		{"name":"pg","labels":{"app.kubernetes.io/name":"postgresql","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"database"}},
		{"name":"pg-exp","labels":{"app.kubernetes.io/name":"postgresql","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"exporter"}},
		{"name":"redis","labels":{"app.kubernetes.io/name":"redis","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"cache"}},
		{"name":"redis-exp","labels":{"app.kubernetes.io/name":"redis","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"exporter"}}
	]`
	slices := `[
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-postgresql"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"pg"}},{"targetRef":{"kind":"Pod","name":"pg-exp"}}]},
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-postgresql-exporter"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"pg-exp"}}]},
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-redis"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"redis"}}]},
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-redis-exporter"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"redis-exp"}}]}
	]`
	if err := assertServiceEndpointsJSON([]byte(pods), []byte(slices), "iterabase"); err == nil {
		t.Fatal("intentional selector break passed")
	}
}

func TestUnitHistoricalSampleRejectsFreshReplacement(t *testing.T) {
	body := []byte(`{"status":"success","data":{"result":[{"values":[[200,"1"]]}]}}`)
	if err := assertHistoricalPrometheusSample(body, 100, "1"); err == nil {
		t.Fatal("post-interval sample incorrectly proved persistence")
	}
}

func TestUnitPrometheusNoSampleIsPendingReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(server.Close)

	ready, observation, err := observePrometheusValue(server.Client(), server.URL, "redis_up", "1")
	if err != nil {
		t.Fatalf("empty successful query should remain pending: %v", err)
	}
	if ready || observation != "query returned no sample" {
		t.Fatalf("ready=%t observation=%q", ready, observation)
	}
}

func TestUnitPrometheusMalformedSuccessFailsImmediately(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing data", body: `{"status":"success"}`},
		{name: "null data", body: `{"status":"success","data":null}`},
		{name: "missing result type", body: `{"status":"success","data":{"result":[]}}`},
		{name: "unexpected result type", body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`},
		{name: "missing result", body: `{"status":"success","data":{"resultType":"vector"}}`},
		{name: "null result", body: `{"status":"success","data":{"resultType":"vector","result":null}}`},
		{name: "non-array result", body: `{"status":"success","data":{"resultType":"vector","result":{}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			if _, _, err := observePrometheusValue(server.Client(), server.URL, "redis_up", "1"); err == nil {
				t.Fatal("malformed successful query was treated as pending readiness")
			}
		})
	}
}

func TestUnitPrometheusObservationErrorsFailImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`not-json`))
	}))
	t.Cleanup(server.Close)

	if _, _, err := observePrometheusValue(server.Client(), server.URL, "redis_up", "1"); err == nil {
		t.Fatal("malformed Prometheus response was treated as pending readiness")
	}
}

func TestUnitTLSTargetRejectsPlaintext(t *testing.T) {
	body := []byte(`{"status":"success","data":{"activeTargets":[{"scrapePool":"serviceMonitor/ns/prometheus/0","scrapeUrl":"http://prometheus:9090/metrics","health":"up","labels":{"job":"prometheus"}}]}}`)
	if err := assertDiscoveredTargets(body, []string{"prometheus"}, true); err == nil {
		t.Fatal("plaintext target incorrectly proved verified HTTPS")
	}
}
