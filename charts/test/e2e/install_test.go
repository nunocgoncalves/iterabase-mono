package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/poll"
)

func freshInstallScenario() sharede2e.Definition {
	diagnostics, cleanup := scenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*chartState]{
		Metadata: chartScenarioMetadata(
			"fresh-install",
			"Installs the ordered certificate substrate and minimal platform edge, then proves manager, issuer, workload identity, and verified gateway readiness.",
			"test-e2e-install", 30,
			[]string{"HOR-408", "HOR-416"},
			[]string{"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart"},
		),
		NewState: newChartState,
		Stages: []sharede2e.Stage[*chartState]{
			{Name: "create-kind", Run: createKindStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"create-kind"}, Run: installCertificateSubstrateStage},
			{Name: "install-minimal-platform-edge", DependsOn: []string{"install-certificate-substrate"}, Run: installMinimalPlatformEdgeStage},
			{Name: "assert-manager-contract", DependsOn: []string{"install-minimal-platform-edge"}, Run: assertManagerContractStage},
			{Name: "assert-certificate-issuer", DependsOn: []string{"install-minimal-platform-edge"}, Run: assertCertificateIssuerStage},
			{Name: "assert-workload-identity", DependsOn: []string{"assert-certificate-issuer"}, Run: assertWorkloadIdentityStage},
			{Name: "assert-verified-edge", DependsOn: []string{"install-minimal-platform-edge", "assert-certificate-issuer"}, Run: assertVerifiedEdgeStage},
		},
		Diagnostics: diagnostics,
		Cleanup:     cleanup,
	})
}

func installCertificateSubstrateStage(t *testing.T, state *chartState) {
	state.installSubstrate(t)
}

func installMinimalPlatformEdgeStage(t *testing.T, state *chartState) {
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
	pool := fmt.Sprintf("%s.%s.255.200-%s.%s.255.250", parts[0], parts[1], parts[0], parts[1])
	values := basePlatformValues()
	values["metallb"] = map[string]any{"enabled": true}
	values["metallb-config"] = map[string]any{"enabled": true, "addresses": []string{pool}}
	applyCandidateImages(values)
	state.installPlatform(t, 15*time.Minute, state.writeValues(t, "fresh-install", values))
	assertCandidateImages(t, state)
}

func assertManagerContractStage(t *testing.T, state *chartState) {
	t.Helper()
	deployment := testRelease + "-control-plane-manager"
	subject := "system:serviceaccount:" + testNamespace + ":" + state.kubectl(t, 30*time.Second,
		"get", "deployment", deployment, "-n", testNamespace, "-o", "jsonpath={.spec.template.spec.serviceAccountName}")
	state.kubectl(t, 30*time.Second, "get", "crd", "agentpools.platform.iterabase.com", "workflows.platform.iterabase.com")
	for _, resource := range []string{
		"pods", "configmaps", "persistentvolumeclaims", "networkpolicies.networking.k8s.io",
		"agentpools.platform.iterabase.com", "workflows.platform.iterabase.com",
	} {
		if got := state.kubectl(t, 30*time.Second, "auth", "can-i", "list", resource, "--all-namespaces", "--as", subject); got != "yes" {
			t.Fatalf("%s cannot list %s", subject, resource)
		}
	}
	if got := state.kubectl(t, 30*time.Second, "auth", "can-i", "get", "secrets", "-n", testNamespace, "--as", subject); got != "yes" {
		t.Fatalf("%s cannot get namespace Secrets", subject)
	}
	state.kubectl(t, 3*time.Minute, "rollout", "status", "deployment/"+deployment, "-n", testNamespace, "--timeout=2m")
	// controller-runtime's cache-sync failure boundary is two minutes. Observe
	// once beyond it; this is not a retry or a performance assertion.
	time.Sleep(130 * time.Second)
	if got := state.kubectl(t, 30*time.Second, "get", "deployment", deployment, "-n", testNamespace, "-o", "jsonpath={.status.readyReplicas}"); got != "1" {
		t.Fatalf("manager ready replicas=%q want=1", got)
	}
	if got := state.kubectl(t, 30*time.Second, "get", "pods", "-n", testNamespace, "-l", "app.kubernetes.io/component=manager", "-o", "jsonpath={.items[0].status.containerStatuses[0].restartCount}"); got != "0" {
		t.Fatalf("manager restart count=%q want=0", got)
	}
	logs := state.kubectl(t, 30*time.Second, "logs", "deployment/"+deployment, "-n", testNamespace, "--all-containers", "--tail=500")
	if strings.Contains(logs, "Failed to run manager") {
		t.Fatal("control-plane manager exited after cache synchronization")
	}
}

func assertCertificateIssuerStage(t *testing.T, state *chartState) {
	t.Helper()
	state.kubectl(t, 3*time.Minute, "wait", "--for=condition=Ready", "clusterissuer/selfsigned", "--timeout=2m")
	state.kubectl(t, 3*time.Minute, "rollout", "status", "daemonset/cert-manager-csi-driver", "-n", testNamespace, "--timeout=2m")
	state.kubectl(t, 30*time.Second, "get", "csidriver", "csi.cert-manager.io")
}

func assertWorkloadIdentityStage(t *testing.T, state *chartState) {
	t.Helper()
	manifest := `apiVersion: v1
kind: Pod
metadata:
  name: workload-identity-csi-smoke
  namespace: iterabase-system
spec:
  restartPolicy: Never
  containers:
    - name: verify
      image: busybox:1.37.0
      command: ["/bin/sh", "-c"]
      args: ["test -s /tls/tls.crt && test -s /tls/tls.key && test -s /tls/ca.crt"]
      volumeMounts:
        - name: workload-identity
          mountPath: /tls
          readOnly: true
  volumes:
    - name: workload-identity
      csi:
        driver: csi.cert-manager.io
        readOnly: true
        volumeAttributes:
          csi.cert-manager.io/issuer-name: platform-spiffe-ca
          csi.cert-manager.io/issuer-kind: ClusterIssuer
          csi.cert-manager.io/uri-sans: spiffe://iterabase.local/pools/ci/workers/csi-smoke
          csi.cert-manager.io/duration: 1h
`
	path := state.writeManifest(t, "workload-identity.yaml", manifest)
	state.kubectl(t, 30*time.Second, "apply", "-f", path)
	state.kubectl(t, 4*time.Minute, "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/workload-identity-csi-smoke", "-n", testNamespace, "--timeout=3m")
}

func assertVerifiedEdgeStage(t *testing.T, state *chartState) {
	t.Helper()
	var address string
	err := poll.Until(state.ctx, 3*time.Minute, 2*time.Second, func(context.Context) (bool, string, error) {
		out, observeErr := state.client.Kubectl(state.ctx, 30*time.Second,
			"get", "service", "-n", testNamespace, "-l", "app.kubernetes.io/name=ingress-nginx",
			"-o", "jsonpath={.items[0].status.loadBalancer.ingress[0].ip}")
		if observeErr != nil {
			return false, "read LoadBalancer address", observeErr
		}
		address = strings.TrimSpace(out)
		return address != "", address, nil
	})
	if err != nil {
		t.Fatalf("wait for ingress LoadBalancer address: %v", err)
	}
	state.kubectl(t, 3*time.Minute, "wait", "--for=condition=Ready", "certificate/"+testRelease+"-gateway-tls", "-n", testNamespace, "--timeout=2m")
	certificate := decodeSecretValue(t, state, testRelease+"-gateway-tls", "tls.crt")
	// A MetalLB address is host-routable on Linux CI but not across every local
	// Docker backend. Prove allocation above, then exercise the same ingress
	// Service through a loopback port-forward for a portable verified-TLS check.
	forward := state.forward(t, "svc/"+testRelease+"-ingress-nginx-controller", 443, "https")
	client := verifiedDialClient(t, certificate, "gateway.iterabase.local", fmt.Sprintf("127.0.0.1:%d", forward.LocalPort))
	if err := waitHTTPReady(state.ctx, client, "https://gateway.iterabase.local/health", 2*time.Minute); err != nil {
		t.Fatalf("verified gateway edge did not become ready: %v", err)
	}
	requireHTTP(t, client, http.MethodGet, "https://gateway.iterabase.local/health", nil, http.StatusOK)
	state.stopForward(t, forward)
}
