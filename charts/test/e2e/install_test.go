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
			"Installs the ordered certificate substrate plus class-isolated public/private ingress planes, then proves manager, issuer, workload identity, fixed private allocation, route isolation, and verified gateway readiness.",
			"test-e2e-install", 30,
			[]string{"HOR-408", "HOR-414", "HOR-416", "HOR-475"},
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
			{Name: "assert-private-ingress-plane", DependsOn: []string{"install-minimal-platform-edge", "assert-certificate-issuer"}, Run: assertPrivateIngressPlaneStage},
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
	state.internalIngressIP = fmt.Sprintf("%s.%s.255.180", parts[0], parts[1])
	internalPool := fmt.Sprintf("%s-%s.%s.255.190", state.internalIngressIP, parts[0], parts[1])
	values := basePlatformValues()
	values["metallb"] = map[string]any{"enabled": true}
	values["metallb-config"] = map[string]any{
		"enabled":   true,
		"addresses": []string{pool},
		"additionalPools": []any{map[string]any{
			"name": "internal", "addresses": []string{internalPool}, "autoAssign": false,
		}},
	}
	values["internal-ingress-nginx"] = map[string]any{
		"enabled": true,
		"controller": map[string]any{"service": map[string]any{"annotations": map[string]any{
			"metallb.io/address-pool":    testRelease + "-internal",
			"metallb.io/loadBalancerIPs": state.internalIngressIP,
		}}},
	}
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

func assertPrivateIngressPlaneStage(t *testing.T, state *chartState) {
	t.Helper()
	if got := state.kubectl(t, 30*time.Second, "get", "ingressclass/nginx", "-o", "jsonpath={.spec.controller}"); got != "k8s.io/ingress-nginx" {
		t.Fatalf("public IngressClass controller=%q", got)
	}
	if got := state.kubectl(t, 30*time.Second, "get", "ingressclass/nginx-internal", "-o", "jsonpath={.spec.controller}"); got != "k8s.io/ingress-nginx-internal" {
		t.Fatalf("private IngressClass controller=%q", got)
	}
	if got := state.kubectl(t, 30*time.Second, "get", "ipaddresspool/"+testRelease+"-internal", "-n", testNamespace, "-o", "jsonpath={.spec.autoAssign}"); got != "false" {
		t.Fatalf("private MetalLB pool autoAssign=%q", got)
	}

	service := testRelease + "-internal-ingress-nginx-controller"
	var address string
	err := poll.Until(state.ctx, 3*time.Minute, 2*time.Second, func(context.Context) (bool, string, error) {
		out, observeErr := state.client.Kubectl(state.ctx, 30*time.Second, "get", "service/"+service, "-n", testNamespace,
			"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
		if observeErr != nil {
			return false, "read private LoadBalancer address", observeErr
		}
		address = strings.TrimSpace(out)
		return address == state.internalIngressIP, address, nil
	})
	if err != nil {
		t.Fatalf("private ingress did not receive fixed MetalLB address %s: %v", state.internalIngressIP, err)
	}

	const host = "private-gateway.iterabase.local"
	manifest := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: private-ingress-smoke
  namespace: %s
  annotations:
    cert-manager.io/cluster-issuer: selfsigned
spec:
  ingressClassName: nginx-internal
  tls:
    - hosts: [%s]
      secretName: private-ingress-smoke-tls
  rules:
    - host: %s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: %s-gateway
                port:
                  number: 8080
`, testNamespace, host, host, testRelease)
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "private-ingress-smoke.yaml", manifest))
	state.kubectl(t, 3*time.Minute, "wait", "--for=condition=Ready", "certificate/private-ingress-smoke-tls", "-n", testNamespace, "--timeout=2m")
	certificate := decodeSecretValue(t, state, "private-ingress-smoke-tls", "tls.crt")

	privateForward := state.forward(t, "svc/"+service, 443, "https")
	privateClient := verifiedDialClient(t, certificate, host, fmt.Sprintf("127.0.0.1:%d", privateForward.LocalPort))
	if err := waitHTTPReady(state.ctx, privateClient, "https://"+host+"/health", 2*time.Minute); err != nil {
		t.Fatalf("private ingress route did not become ready: %v", err)
	}
	requireHTTP(t, privateClient, http.MethodGet, "https://"+host+"/health", nil, http.StatusOK)
	state.stopForward(t, privateForward)

	// The public controller must not load an Ingress owned by the private class.
	// Trust only the private leaf: a response would prove the public controller
	// served that route and certificate instead of its unrelated default leaf.
	publicForward := state.forward(t, "svc/"+testRelease+"-ingress-nginx-controller", 443, "https")
	publicClient := verifiedDialClient(t, certificate, host, fmt.Sprintf("127.0.0.1:%d", publicForward.LocalPort))
	if response, requestErr := publicClient.Get("https://" + host + "/health"); requestErr == nil {
		_ = response.Body.Close()
		t.Fatalf("public ingress served private-class host with status %d", response.StatusCode)
	}
	state.stopForward(t, publicForward)
}
