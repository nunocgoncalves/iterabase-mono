package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/diagnostics"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/httpx"
	kindcluster "github.com/nunocgoncalves/iterabase-mono/testkit/e2e/kind"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/kube"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/poll"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

const testNamespace = "iterabase-system"

var testRelease = func() string {
	if configured := strings.TrimSpace(os.Getenv("ITERABASE_E2E_RELEASE")); configured != "" {
		return configured
	}
	return "iterabase"
}()

func kubePrometheusStackComponentName(component string) string {
	return kubePrometheusStackComponentNameForRelease(testRelease, component)
}

func kubePrometheusStackComponentNameForRelease(release, component string) string {
	const chartName = "kube-prometheus-stack"
	fullname := release
	if !strings.Contains(release, chartName) {
		fullname += "-" + chartName
	}
	if len(fullname) > 26 {
		fullname = fullname[:26]
	}
	return strings.TrimSuffix(fullname, "-") + "-" + component
}

type chartState struct {
	ctx                 context.Context
	chartsRoot          string
	outputDir           string
	diagnosticsDir      string
	redactor            *redact.Redactor
	runner              process.Runner
	cluster             *kindcluster.Cluster
	client              kube.Client
	forwards            []*kube.Forward
	platform            kube.Chart
	substrate           kube.Chart
	transitionBaselines map[string]transitionBaseline
	snapshots           map[string]lifecycleSnapshot
}

func newChartState(t *testing.T) *chartState {
	t.Helper()
	chartsRoot := os.Getenv("ITERABASE_CHARTS_ROOT")
	if chartsRoot == "" {
		var err error
		chartsRoot, err = filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatalf("resolve charts root: %v", err)
		}
	}
	chartsRoot, err := filepath.Abs(chartsRoot)
	if err != nil {
		t.Fatalf("resolve charts root: %v", err)
	}
	outputDir := filepath.Join(t.TempDir(), "evidence")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	diagnosticsDir := filepath.Join(outputDir, "diagnostics")
	if configured := os.Getenv("ITERABASE_E2E_DIAGNOSTICS"); configured != "" {
		diagnosticsDir = configured
		if err := os.MkdirAll(diagnosticsDir, 0o700); err != nil {
			t.Fatalf("create persistent diagnostics directory: %v", err)
		}
	}
	redactor := redact.New()
	state := &chartState{
		ctx: context.Background(), chartsRoot: chartsRoot, outputDir: outputDir, diagnosticsDir: diagnosticsDir, redactor: redactor,
		runner: process.Runner{Redactor: redactor, OutputDir: outputDir}, snapshots: make(map[string]lifecycleSnapshot),
	}
	state.platform, state.substrate = resolveCharts(t, chartsRoot)
	return state
}

func resolveCharts(t *testing.T, chartsRoot string) (kube.Chart, kube.Chart) {
	t.Helper()
	mode := sharede2e.FixtureMode(os.Getenv("ITERABASE_E2E_FIXTURE_MODE"))
	switch mode {
	case sharede2e.FixtureSource:
		return kube.Chart{Mode: mode, LocalPath: filepath.Join(chartsRoot, "charts", "iterabase-platform")},
			kube.Chart{Mode: mode, LocalPath: filepath.Join(chartsRoot, "charts", "cert-manager-substrate")}
	case sharede2e.FixtureCandidate:
		platform := os.Getenv("ITERABASE_PLATFORM_LOCAL_CHART")
		if platform == "" {
			t.Fatal("candidate charts scenario requires ITERABASE_PLATFORM_LOCAL_CHART")
		}
		platform, err := filepath.Abs(platform)
		if err != nil {
			t.Fatalf("resolve candidate platform chart: %v", err)
		}
		substrate := filepath.Join(filepath.Dir(platform), "cert-manager-substrate")
		return kube.Chart{Mode: mode, LocalPath: platform}, kube.Chart{Mode: mode, LocalPath: substrate}
	case sharede2e.FixturePublished:
		version := publishedPlatformVersion(t)
		return kube.Chart{Mode: mode, Reference: "oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform", Version: version},
			kube.Chart{Mode: mode, Reference: "oci://ghcr.io/nunocgoncalves/iterabase-charts/cert-manager-substrate", Version: version}
	default:
		t.Fatalf("unsupported charts fixture mode %q", mode)
		return kube.Chart{}, kube.Chart{}
	}
}

func publishedPlatformVersion(t *testing.T) string {
	t.Helper()
	path := os.Getenv("ITERABASE_E2E_PUBLISHED_FIXTURE")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published fixture: %v", err)
	}
	var fixture sharede2e.Fixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode published fixture: %v", err)
	}
	for _, input := range fixture.Inputs {
		if input.Name != "iterabase-platform" {
			continue
		}
		separator := strings.LastIndexByte(input.Reference, ':')
		if separator < 0 || separator == len(input.Reference)-1 {
			t.Fatalf("published platform reference has no version: %q", input.Reference)
		}
		return input.Reference[separator+1:]
	}
	t.Fatal("published fixture has no iterabase-platform input")
	return ""
}

func createKindStage(t *testing.T, state *chartState) {
	t.Helper()
	manager := kindcluster.Manager{Executor: state.runner}
	cluster, err := manager.Create(state.ctx, "charts")
	if err != nil {
		t.Fatalf("create Kind cluster: %v", err)
	}
	state.cluster = cluster
	state.client = kube.Client{Executor: state.runner, Kubeconfig: cluster.Kubeconfig, Redactor: state.redactor}
}

func chartDiagnostics(t *testing.T, state *chartState) {
	t.Helper()
	if state.cluster == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	err := (diagnostics.Collector{
		Executor: state.runner, Kubeconfig: state.cluster.Kubeconfig,
		OutputDir: state.diagnosticsDir, Redactor: state.redactor,
	}).Collect(ctx)
	if err != nil {
		t.Logf("best-effort diagnostics: %v", err)
	}
}

func cleanupForwards(t *testing.T, state *chartState) {
	t.Helper()
	for i := len(state.forwards) - 1; i >= 0; i-- {
		if err := state.forwards[i].Stop(); err != nil {
			t.Errorf("stop port-forward: %v", err)
		}
	}
	state.forwards = nil
}

func cleanupKind(t *testing.T, state *chartState) {
	t.Helper()
	if state.cluster == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if err := state.cluster.Delete(ctx); err != nil {
		t.Errorf("delete Kind cluster: %v", err)
	}
}

func scenarioHooks() ([]sharede2e.Hook[*chartState], []sharede2e.Hook[*chartState]) {
	return []sharede2e.Hook[*chartState]{{Name: "shared-cluster-evidence", Run: chartDiagnostics}},
		[]sharede2e.Hook[*chartState]{
			{Name: "stop-port-forwards", Run: cleanupForwards},
			{Name: "delete-kind-cluster", Run: cleanupKind},
		}
}

func (state *chartState) kubectl(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()
	out, err := state.client.Kubectl(state.ctx, timeout, args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

func (state *chartState) kubectlResult(timeout time.Duration, args ...string) (string, error) {
	return state.client.Kubectl(state.ctx, timeout, args...)
}

func (state *chartState) process(t *testing.T, timeout time.Duration, name string, args ...string) string {
	t.Helper()
	result, err := state.runner.Run(state.ctx, process.Command{Name: name, Args: args, Timeout: timeout})
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, result.Output)
	}
	return strings.TrimSpace(result.Output)
}

func (state *chartState) writeValues(t *testing.T, name string, values map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s values: %v", name, err)
	}
	return state.writeManifest(t, name+".json", string(append(data, '\n')))
}

func (state *chartState) writeManifest(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func assertCandidateImages(t *testing.T, state *chartState) {
	t.Helper()
	for _, image := range []struct {
		selector string
		prefix   string
	}{
		{selector: "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=api", prefix: "CONTROL_PLANE"},
		{selector: "app.kubernetes.io/name=inference-gateway", prefix: "INFERENCE_GATEWAY"},
	} {
		digest := os.Getenv(image.prefix + "_IMAGE_DIGEST")
		if digest == "" {
			continue
		}
		imageIDs := state.kubectl(t, 30*time.Second, "get", "pods", "-n", testNamespace, "-l", image.selector,
			"-o", "jsonpath={.items[*].status.containerStatuses[*].imageID}")
		if !strings.Contains(imageIDs, digest) {
			t.Fatalf("%s pods do not run exact candidate digest %s: %s", image.prefix, digest, imageIDs)
		}
	}
}

func basePlatformValues() map[string]any {
	return map[string]any{
		"external-dns": map[string]any{"enabled": false},
		"minio":        map[string]any{"enabled": false},
		"control-plane": map[string]any{
			"artifact":   map[string]any{"enabled": false},
			"dispatch":   map[string]any{"enabled": false},
			"toolRunner": map[string]any{"enabled": false},
		},
	}
}

func runtimePlatformValues() map[string]any {
	values := basePlatformValues()
	values["ingress-nginx"] = map[string]any{"enabled": false}
	values["metallb"] = map[string]any{"enabled": false}
	values["metallb-config"] = map[string]any{"enabled": false}
	applyCandidateImages(values)
	return values
}

func applyCandidateImages(values map[string]any) {
	setImage := func(component, prefix string) {
		repository := os.Getenv(prefix + "_IMAGE_REPO")
		tag := os.Getenv(prefix + "_IMAGE_TAG")
		if repository == "" && tag == "" {
			return
		}
		componentValues, _ := values[component].(map[string]any)
		if componentValues == nil {
			componentValues = map[string]any{}
			values[component] = componentValues
		}
		image, _ := componentValues["image"].(map[string]any)
		if image == nil {
			image = map[string]any{}
			componentValues["image"] = image
		}
		if repository != "" {
			image["repository"] = repository
		}
		if tag != "" {
			image["tag"] = tag
		}
	}
	setImage("control-plane", "CONTROL_PLANE")
	setImage("inference-gateway", "INFERENCE_GATEWAY")
}

func (state *chartState) installSubstrate(t *testing.T) {
	t.Helper()
	out, err := state.client.HelmUpgrade(state.ctx, kube.HelmOptions{
		Release: testRelease + "-cert-manager", Namespace: testNamespace, Chart: state.substrate,
		CreateNamespace: true, Wait: true, Timeout: 8 * time.Minute,
	})
	if err != nil {
		t.Fatalf("install certificate substrate: %v\n%s", err, out)
	}
}

func (state *chartState) installPlatform(t *testing.T, timeout time.Duration, valueFiles ...string) {
	t.Helper()
	out, err := state.client.HelmUpgrade(state.ctx, kube.HelmOptions{
		Release: testRelease, Namespace: testNamespace, Chart: state.platform,
		ValueFiles: valueFiles, Wait: true, Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("install platform: %v\n%s", err, out)
	}
}

func (state *chartState) waitForPods(t *testing.T, selector string, timeout time.Duration) {
	t.Helper()
	err := poll.Until(state.ctx, timeout, 3*time.Second, func(context.Context) (bool, string, error) {
		out, err := state.client.Kubectl(state.ctx, 30*time.Second, "get", "pods", "-n", testNamespace, "-l", selector, "-o", "name")
		if err != nil {
			return false, "list pods", err
		}
		return strings.TrimSpace(out) != "", strings.TrimSpace(out), nil
	})
	if err != nil {
		t.Fatalf("pods for %q did not appear: %v", selector, err)
	}
	state.kubectl(t, timeout+time.Minute, "wait", "--for=condition=Ready", "pod", "-n", testNamespace, "-l", selector, "--timeout", timeout.String())
}

func (state *chartState) firstPod(t *testing.T, selector string) string {
	t.Helper()
	var pod string
	err := poll.Until(state.ctx, 2*time.Minute, 2*time.Second, func(context.Context) (bool, string, error) {
		out, err := state.client.Kubectl(state.ctx, 30*time.Second, "get", "pods", "-n", testNamespace, "-l", selector, "-o", "jsonpath={.items[0].metadata.name}")
		if err != nil {
			return false, "get first pod", err
		}
		pod = strings.TrimSpace(out)
		return pod != "", pod, nil
	})
	if err != nil {
		t.Fatalf("find pod for %q: %v", selector, err)
	}
	return pod
}

func (state *chartState) forward(t *testing.T, resource string, port int, scheme string) *kube.Forward {
	t.Helper()
	forward, err := state.client.PortForward(state.ctx, testNamespace, resource, port, scheme)
	if err != nil {
		t.Fatalf("port-forward %s: %v", resource, err)
	}
	state.forwards = append(state.forwards, forward)
	return forward
}

func (state *chartState) stopForward(t *testing.T, forward *kube.Forward) {
	t.Helper()
	if err := forward.Stop(); err != nil {
		t.Fatalf("stop port-forward: %v", err)
	}
	for i, candidate := range state.forwards {
		if candidate == forward {
			state.forwards = append(state.forwards[:i], state.forwards[i+1:]...)
			return
		}
	}
}

func decodeSecretValue(t *testing.T, state *chartState, name, key string) []byte {
	t.Helper()
	jsonPath := fmt.Sprintf("jsonpath={.data.%s}", strings.ReplaceAll(key, ".", `\.`))
	encoded := state.kubectl(t, 30*time.Second, "get", "secret", name, "-n", testNamespace, "-o", jsonPath)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode %s/%s: %v", name, key, err)
	}
	return decoded
}

func verifiedClient(t *testing.T, ca []byte, serverName string) *http.Client {
	t.Helper()
	client, err := httpx.TLSClient(httpx.TLSOptions{Timeout: 15 * time.Second, RootCAPEM: ca, ServerName: serverName})
	if err != nil {
		t.Fatalf("create verified TLS client: %v", err)
	}
	return client
}

func verifiedDialClient(t *testing.T, ca []byte, serverName, address string) *http.Client {
	t.Helper()
	client := verifiedClient(t, ca, serverName)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("verified client transport has unexpected type")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	return client
}

func requireHTTP(t *testing.T, client *http.Client, method, url string, configure func(*http.Request), want int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}
	if configure != nil {
		configure(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if readErr != nil {
		t.Fatalf("read %s: %v", url, readErr)
	}
	if len(body) > 2<<20 {
		t.Fatalf("read %s: response exceeds 2 MiB", url)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s status=%d want=%d body=%s", url, resp.StatusCode, want, stateSafeBody(body))
	}
	return body
}

func stateSafeBody(body []byte) string {
	const limit = 1000
	value := string(body)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func waitHTTPReady(ctx context.Context, client *http.Client, url string, timeout time.Duration) error {
	return poll.Until(ctx, timeout, 2*time.Second, func(context.Context) (bool, string, error) {
		resp, err := client.Get(url)
		if err != nil {
			return false, err.Error(), nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 300, fmt.Sprintf("status %d", resp.StatusCode), nil
	})
}
