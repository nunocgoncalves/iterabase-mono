package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

type objectList[T any] struct {
	Items []T `json:"items"`
}

type endpointPod struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

func assertEndpointSet(service string, expected, actual []string) error {
	sort.Strings(expected)
	sort.Strings(actual)
	if len(expected) == 0 {
		return fmt.Errorf("%s has no selected component pods", service)
	}
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("%s endpoints %v != expected %v", service, actual, expected)
	}
	return nil
}

func assertDisjointEndpointSets(first string, firstPods []string, second string, secondPods []string) error {
	for _, pod := range firstPods {
		if slices.Contains(secondPods, pod) {
			return fmt.Errorf("%s and %s overlap on %s", first, second, pod)
		}
	}
	return nil
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

func assertServiceEndpointsJSON(podsJSON, slicesJSON []byte, release string) error {
	var pods objectList[endpointPod]
	var endpointSlices objectList[endpointSlice]
	if err := json.Unmarshal(podsJSON, &pods); err != nil {
		return fmt.Errorf("decode pods: %w", err)
	}
	if err := json.Unmarshal(slicesJSON, &endpointSlices); err != nil {
		return fmt.Errorf("decode EndpointSlices: %w", err)
	}
	selectedPods := func(name, component string) []string {
		var selected []string
		for _, pod := range pods.Items {
			labels := pod.Metadata.Labels
			if labels["app.kubernetes.io/name"] == name && labels["app.kubernetes.io/instance"] == release && labels["app.kubernetes.io/component"] == component {
				selected = append(selected, pod.Metadata.Name)
			}
		}
		sort.Strings(selected)
		return selected
	}
	endpointPods := func(service string) []string {
		var selected []string
		for _, slice := range endpointSlices.Items {
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

func TestUnitEndpointSeparationRejectsExporterLeak(t *testing.T) {
	pods := `{"items":[
		{"metadata":{"name":"pg","labels":{"app.kubernetes.io/name":"postgresql","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"database"}}},
		{"metadata":{"name":"pg-exp","labels":{"app.kubernetes.io/name":"postgresql","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"exporter"}}},
		{"metadata":{"name":"redis","labels":{"app.kubernetes.io/name":"redis","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"cache"}}},
		{"metadata":{"name":"redis-exp","labels":{"app.kubernetes.io/name":"redis","app.kubernetes.io/instance":"iterabase","app.kubernetes.io/component":"exporter"}}}
	]}`
	slices := `{"items":[
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-postgresql"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"pg"}},{"targetRef":{"kind":"Pod","name":"pg-exp"}}]},
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-postgresql-exporter"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"pg-exp"}}]},
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-redis"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"redis"}}]},
		{"metadata":{"labels":{"kubernetes.io/service-name":"iterabase-redis-exporter"}},"endpoints":[{"targetRef":{"kind":"Pod","name":"redis-exp"}}]}
	]}`
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

func TestUnitTLSTargetRejectsPlaintext(t *testing.T) {
	body := []byte(`{"status":"success","data":{"activeTargets":[{"scrapePool":"serviceMonitor/ns/prometheus/0","scrapeUrl":"http://prometheus:9090/metrics","health":"up","labels":{"job":"prometheus"}}]}}`)
	if err := assertDiscoveredTargets(body, []string{"prometheus"}, true); err == nil {
		t.Fatal("plaintext target incorrectly proved verified HTTPS")
	}
}
