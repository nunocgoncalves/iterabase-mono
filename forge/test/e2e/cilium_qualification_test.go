package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/forge/test/e2e/internal/remotecluster"
	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"golang.org/x/crypto/ssh"
)

const (
	ciliumQualificationEnv       = "FORGE_E2E_CILIUM_QUALIFICATION"
	ciliumSourceImageArchiveEnv  = "FORGE_E2E_SOURCE_IMAGE_ARCHIVE"
	hor527CandidateSourceSHA     = "e76f12a14db99b7d8e44fa3e62d95d1d7195caee"
	hor527CandidateChartVersion  = "0.3.23"
	ciliumQualificationVersion   = "1.19.7"
	ciliumQualificationChartHash = "af6aeba999b438b897e71452051aab2c014bb89369ab34ca46a33003eb0d017e"
	ciliumPolicyProbeImage       = "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"
	ciliumRecoveryLimit          = 60 * time.Second
)

type ciliumQualificationState struct {
	*digitalOceanCPUState
	candidateChecksums     string
	agentPoolPVCUID        string
	longhornVolume         string
	initialWorkerUIDs      map[string]string
	initialShareManager    string
	initialShareManagerUID string
	priorPayload           string
	priorChecksum          string
	postChecksum           string
	faultStartedAt         time.Time
	faultAcceptedAt        time.Time
	recoveredAt            time.Time
	lastObservation        ciliumRecoveryObservation
}

// TestCiliumCleanClusterLonghornRecoveryQualification is the bounded
// DES-HOR-537-01 exception to Forge's scheduled TestE2E catalogue. It is
// opt-in, source-pinned, and invoked only by the dedicated qualification job;
// ordinary unit, PR, nightly, release, and catalogue execution skip it.
func TestCiliumCleanClusterLonghornRecoveryQualification(t *testing.T) {
	if os.Getenv(ciliumQualificationEnv) != "true" {
		t.Skipf("%s=true is required for the bounded HOR-537 qualification", ciliumQualificationEnv)
	}

	suite := sharede2e.NewSuite(sharede2e.SuiteMetadata{
		Name: "forge-cilium-qualification", Owner: "forge", Entrypoint: "forge/test/e2e",
	}, sharede2e.FixtureFromEnv)
	suite.Add(sharede2e.Define(sharede2e.Scenario[*ciliumQualificationState]{
		Metadata: sharede2e.ScenarioMetadata{
			Name:         "clean-cluster-longhorn-recovery",
			Description:  "Bootstraps a clean K3s host directly with checksum-pinned Cilium and proves the exact HOR-527 candidate autonomously restores fresh-worker RWX read/write integrity within 60 seconds of established share-manager loss.",
			Tier:         sharede2e.TierF3,
			References:   []string{"HOR-537", "DES-HOR-537-01", "HOR-527"},
			FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource},
			MakeTarget:   "test-e2e-cilium-recovery", TimeoutMinutes: 75,
			Capacity: "cpu", Mandatory: true,
		},
		NewState: newCiliumQualificationState,
		Stages: []sharede2e.Stage[*ciliumQualificationState]{
			{Name: "provision-clean-host", Run: ciliumQualificationStage(failureDomainProvisioning, provisionCiliumQualificationHost)},
			{Name: "bootstrap-k3s-and-cilium", DependsOn: []string{"provision-clean-host"}, Run: ciliumQualificationStage(failureDomainSubstrate, bootstrapCiliumQualificationCluster)},
			{Name: "prove-cilium-policy-authority", DependsOn: []string{"bootstrap-k3s-and-cilium"}, Run: ciliumQualificationStage(failureDomainSubstrate, proveCiliumPolicyAuthority)},
			{Name: "install-exact-hor527-candidate", DependsOn: []string{"prove-cilium-policy-authority"}, Run: ciliumQualificationStage(failureDomainForgeHandoff, installCiliumQualificationCandidate)},
			{Name: "establish-two-worker-rwx-checksum", DependsOn: []string{"install-exact-hor527-candidate"}, Run: ciliumQualificationStage(failureDomainDependentSmoke, establishCiliumQualificationAgentPool)},
			{Name: "recover-rwx-within-sixty-seconds", DependsOn: []string{"establish-two-worker-rwx-checksum"}, Run: ciliumQualificationStage(failureDomainSubstrate, qualifyCiliumRecovery)},
		},
		Diagnostics: []sharede2e.Hook[*ciliumQualificationState]{{Name: "bounded-causal-evidence", Run: collectCiliumQualificationDiagnostics}},
		Cleanup:     []sharede2e.Hook[*ciliumQualificationState]{{Name: "destroy-qualification-host", Run: func(t *testing.T, state *ciliumQualificationState) { state.cleanup(t) }}},
	}))
	suite.Run(t)
}

func newCiliumQualificationState(t *testing.T) *ciliumQualificationState {
	t.Helper()
	if source := os.Getenv("ITERABASE_E2E_SOURCE_SHA"); source != hor527CandidateSourceSHA {
		t.Fatalf("HOR-537 requires exact PR #65 source %s, got %q", hor527CandidateSourceSHA, source)
	}
	cpu := newDigitalOceanCPUStateForScenario(t, "cilium-clean-cluster-recovery")
	cpu.runID = fmt.Sprintf("hor537-cilium-%d", time.Now().Unix())
	if !cpu.managedRWX {
		t.Fatalf("HOR-537 requires the exact managed-RWX chart archives through %s", storageChartArchiveEnv)
	}
	if cpu.chartVersion != hor527CandidateChartVersion {
		t.Fatalf("HOR-537 candidate chart version=%q want=%s", cpu.chartVersion, hor527CandidateChartVersion)
	}
	state := &ciliumQualificationState{digitalOceanCPUState: cpu}
	state.candidateChecksums = validateCiliumQualificationInputs(t, state)
	return state
}

func validateCiliumQualificationInputs(t *testing.T, state *ciliumQualificationState) string {
	t.Helper()
	paths := []string{
		os.Getenv(platformChartArchiveEnv),
		os.Getenv(substrateChartArchiveEnv),
		os.Getenv(storageChartArchiveEnv),
		os.Getenv(ciliumSourceImageArchiveEnv),
	}
	for index, path := range paths {
		if path == "" {
			t.Fatalf("HOR-537 candidate input %d is empty", index)
		}
	}
	checksumsPath := filepath.Join(filepath.Dir(paths[0]), "checksums.txt")
	data, err := os.ReadFile(checksumsPath)
	if err != nil {
		t.Fatalf("read exact candidate checksums: %v", err)
	}
	want := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid candidate checksum line %q", line)
		}
		want[fields[1]] = fields[0]
	}
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read exact candidate input %s: %v", path, readErr)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(contents))
		if want[filepath.Base(path)] != got {
			t.Fatalf("exact candidate checksum mismatch for %s: got=%s want=%s", filepath.Base(path), got, want[filepath.Base(path)])
		}
	}
	if filepath.Base(paths[3]) != "exact-source-images-"+hor527CandidateSourceSHA+".tar" {
		t.Fatalf("source image archive is not bound to PR #65: %s", filepath.Base(paths[3]))
	}

	harnessCommand := exec.Command("git", "rev-parse", "HEAD")
	harnessCommand.Dir = filepath.Join("..", "..", "..")
	harnessOutput, err := harnessCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve qualification harness source: %v\n%s", err, harnessOutput)
	}
	evidence := fmt.Sprintf("candidate_source=%s\nqualification_harness_source=%s\ncandidate_chart_version=%s\n%s\ncilium_chart_version=%s\ncilium_chart_sha256=%s\n",
		hor527CandidateSourceSHA, strings.TrimSpace(string(harnessOutput)), hor527CandidateChartVersion,
		strings.TrimSpace(string(data)), ciliumQualificationVersion, ciliumQualificationChartHash)
	writeCiliumQualificationEvidence(t, state, "qualification-inputs.txt", evidence)
	return strings.TrimSpace(string(data))
}

func ciliumQualificationStage(domain string, run func(*testing.T, *ciliumQualificationState)) func(*testing.T, *ciliumQualificationState) {
	return func(t *testing.T, state *ciliumQualificationState) {
		state.diagnostics.setDomain(domain)
		run(t, state)
	}
}

func provisionCiliumQualificationHost(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	provisionCPUStage(t, state.digitalOceanCPUState)
}

func bootstrapCiliumQualificationCluster(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("dial clean Cilium host: %v", err)
	}
	defer client.Close()

	prepare := `sudo bash -ceu '
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y open-iscsi nfs-common jq iptables
systemctl enable --now iscsid
modprobe iscsi_tcp
mount --make-rshared /
findmnt -n -o PROPAGATION / | grep -Eq "(^|,)r?shared(,|$)"
install -d -m 0750 /var/lib/longhorn
'`
	mustSSHOutput(t, client, prepare)

	installK3s := fmt.Sprintf(`curl -sfL https://get.k3s.io | sudo env INSTALL_K3S_VERSION=%s sh -s - server \
  --cluster-cidr 10.42.0.0/16 \
  --service-cidr 10.43.0.0/16 \
  --flannel-backend=none \
  --disable-network-policy \
  --disable traefik \
  --disable servicelb \
  --tls-san %s \
  --write-kubeconfig-mode 0644`, candidateShellQuote(threeNodeK3sVersion), candidateShellQuote(state.ip))
	mustSSHOutput(t, client, installK3s)

	installCilium := fmt.Sprintf(`set -eu
if ! sudo helm version --short >/dev/null 2>&1; then
  p=$(mktemp)
  curl -fsSL -o "$p" https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-4
  test -s "$p"
  sudo bash "$p"
  rm -f "$p"
fi
sudo helm repo add cilium https://helm.cilium.io --force-update
sudo helm repo update cilium
sudo rm -f /tmp/cilium-%s.tgz
sudo helm pull cilium/cilium --version %s --destination /tmp
printf '%s  /tmp/cilium-%s.tgz\n' | sudo sha256sum -c -
sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade --install cilium /tmp/cilium-%s.tgz \
  --namespace kube-system \
  --set operator.replicas=1 \
  --set ipam.mode=cluster-pool \
  --set 'ipam.operator.clusterPoolIPv4PodCIDRList={10.42.0.0/16}' \
  --set ipv4.enabled=true \
  --set ipv6.enabled=false \
  --set kubeProxyReplacement=false \
  --set waitForKubeProxy=true \
  --set routingMode=tunnel \
  --set tunnelProtocol=vxlan \
  --set cni.exclusive=true \
  --set policyEnforcementMode=default \
  --set hubble.relay.enabled=false \
  --set hubble.ui.enabled=false \
  --wait --timeout 15m
sudo k3s kubectl rollout status -n kube-system daemonset/cilium --timeout=10m
sudo k3s kubectl rollout status -n kube-system deployment/cilium-operator --timeout=10m
`, ciliumQualificationVersion, ciliumQualificationVersion, ciliumQualificationChartHash,
		ciliumQualificationVersion, ciliumQualificationVersion)
	mustSSHOutput(t, client, installCilium)
	readyOutput, readyErr := sshOutput(client, `sudo k3s kubectl wait --for=condition=Ready node --all --timeout=5m
sudo k3s kubectl rollout status -n kube-system deployment/coredns --timeout=5m`)
	if readyErr != nil {
		diagnostics, _ := sshOutput(client, `{
  echo '=== node ==='
  sudo k3s kubectl get node -o yaml
  echo '=== Cilium pods and logs ==='
  sudo k3s kubectl get pods -n kube-system -o wide
  for pod in $(sudo k3s kubectl get pods -n kube-system -l k8s-app=cilium -o name); do sudo k3s kubectl logs -n kube-system "$pod" -c cilium-agent --tail=300; done
  for pod in $(sudo k3s kubectl get pods -n kube-system -l io.cilium/app=operator -o name); do sudo k3s kubectl logs -n kube-system "$pod" --all-containers --tail=300; done
  echo '=== CNI paths ==='
  sudo find -L /etc/cni/net.d /var/lib/rancher/k3s/agent/etc/cni/net.d /opt/cni/bin /var/lib/rancher/k3s/data/cni -maxdepth 2 -type f -print 2>&1 || true
  echo '=== recent k3s journal ==='
  sudo journalctl -u k3s --no-pager -n 500
} 2>&1`)
		writeCiliumQualificationEvidence(t, state, "cilium-node-readiness-failure.txt", readyOutput+"\n"+diagnostics)
		t.Fatalf("Cilium did not make the clean K3s node Ready: %v\n%s\n%s", readyErr, readyOutput, diagnostics)
	}

	evidence := mustSSHOutput(t, client, ciliumBaselineEvidenceCommand())
	for _, marker := range []string{
		threeNodeK3sVersion,
		"cilium-helm-identity=cilium|1.19.7|1.19.7",
		"--flannel-backend=none",
		"--disable-network-policy",
		"\nOK\n",
	} {
		if !strings.Contains(evidence, marker) {
			t.Fatalf("clean Cilium bootstrap evidence missing %q:\n%s", marker, evidence)
		}
	}
	for _, forbidden := range []string{"--flannel-backend=vxlan", "FLANNEL-FWD", "KUBE-ROUTER"} {
		if strings.Contains(evidence, forbidden) {
			t.Fatalf("clean Cilium bootstrap retained forbidden %q:\n%s", forbidden, evidence)
		}
	}
	writeCiliumQualificationEvidence(t, state, "cilium-bootstrap.txt", evidence)
}

func ciliumBaselineEvidenceCommand() string {
	return `set -eu
printf '%s\n' '=== k3s identity ==='
sudo k3s --version
sudo k3s kubectl version -o json | jq -c '{serverVersion:.serverVersion.gitVersion}'
printf '%s\n' '=== k3s unit ==='
sudo systemctl cat k3s
unit=$(sudo systemctl cat k3s)
printf '%s' "$unit" | grep -F -- '--flannel-backend=none'
printf '%s' "$unit" | grep -F -- '--disable-network-policy'
! printf '%s' "$unit" | grep -F -- '--flannel-backend=vxlan'
printf '%s\n' '=== absent legacy CNI and policy controller ==='
! sudo k3s kubectl get pods -A -o name | grep -Ei 'flannel|kube-router'
! ip link show flannel.1 >/dev/null 2>&1
! pgrep -af '[f]lanneld|[k]ube-router' >/dev/null
! sudo iptables-save | grep -E 'FLANNEL|KUBE-ROUTER'
test ! -e /run/flannel/subnet.env
printf '%s\n' '=== Cilium Helm identity and values ==='
cilium_metadata=$(sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml get metadata cilium -n kube-system -o json)
printf '%s\n' "$cilium_metadata"
cilium_chart=$(printf '%s' "$cilium_metadata" | jq -r '.chart')
cilium_version=$(printf '%s' "$cilium_metadata" | jq -r '.version')
cilium_app=$(printf '%s' "$cilium_metadata" | jq -r '.appVersion // .app_version // .appversion // empty')
test "$cilium_chart|$cilium_version|$cilium_app" = 'cilium|1.19.7|1.19.7'
printf 'cilium-helm-identity=%s|%s|%s\n' "$cilium_chart" "$cilium_version" "$cilium_app"
sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml get values cilium -n kube-system --all -o yaml
cilium_config=$(sudo k3s kubectl get configmap cilium-config -n kube-system -o json)
printf '%s\n' "$cilium_config"
printf '%s' "$cilium_config" | jq -e '.data.ipam=="cluster-pool" and .data["cluster-pool-ipv4-cidr"]=="10.42.0.0/16" and .data["routing-mode"]=="tunnel" and .data["tunnel-protocol"]=="vxlan" and .data["kube-proxy-replacement"]=="false" and .data["enable-k8s-networkpolicy"]=="true" and .data["enable-ipv4"]=="true" and .data["enable-ipv6"]=="false"'
printf '%s\n' '=== Cilium readiness and authority ==='
sudo k3s kubectl get daemonset/cilium deployment/cilium-operator -n kube-system -o wide
sudo k3s kubectl get ciliumnodes.cilium.io,ciliumendpoints.cilium.io -A -o wide
cilium_pod=$(sudo k3s kubectl get pods -n kube-system -l k8s-app=cilium -o jsonpath='{.items[0].metadata.name}')
sudo k3s kubectl exec -n kube-system "$cilium_pod" -c cilium-agent -- cilium-dbg status --brief
cni_files=$(sudo find -L /etc/cni/net.d /var/lib/rancher/k3s/agent/etc/cni/net.d -maxdepth 1 -type f 2>/dev/null | sort -u)
test -n "$cni_files"
for file in $cni_files; do
  printf '%s\n' "--- $file ---"
  sudo cat "$file"
  sudo grep -Eq '"type"[[:space:]]*:[[:space:]]*"cilium-cni"' "$file"
done
printf '%s\n' '=== Cilium image identities ==='
sudo k3s kubectl get pods -n kube-system -l 'k8s-app in (cilium,cilium-operator)' -o json | jq -c '[.items[] | {name:.metadata.name,containers:[.spec.containers[] | {name,image}],runtime:[.status.containerStatuses[]? | {name,imageID}]}]'
`
}

func proveCiliumPolicyAuthority(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	manifest := fmt.Sprintf(`cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: v1
kind: Namespace
metadata: {name: hor537-cilium-authority}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: server, namespace: hor537-cilium-authority}
spec:
  replicas: 1
  selector: {matchLabels: {app: server}}
  template:
    metadata: {labels: {app: server}}
    spec:
      containers:
        - name: server
          image: %s
          command: [/bin/sh, -ceu]
          args: ['mkdir -p /www; printf cilium-policy-authority=pass > /www/index.html; exec httpd -f -p 8080 -h /www']
---
apiVersion: v1
kind: Pod
metadata: {name: allowed, namespace: hor537-cilium-authority, labels: {access: allowed}}
spec:
  containers: [{name: client, image: %s, command: [/bin/sh, -ceu], args: ['sleep 600']}]
---
apiVersion: v1
kind: Pod
metadata: {name: denied, namespace: hor537-cilium-authority, labels: {access: denied}}
spec:
  containers: [{name: client, image: %s, command: [/bin/sh, -ceu], args: ['sleep 600']}]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: server-ingress, namespace: hor537-cilium-authority}
spec:
  podSelector: {matchLabels: {app: server}}
  policyTypes: [Ingress]
  ingress:
    - from: [{podSelector: {matchLabels: {access: allowed}}}]
      ports: [{protocol: TCP, port: 8080}]
YAML`, ciliumPolicyProbeImage, ciliumPolicyProbeImage, ciliumPolicyProbeImage)
	mustSSHOutput(t, client, manifest)
	mustSSHOutput(t, client, `sudo k3s kubectl wait -n hor537-cilium-authority --for=condition=Available deployment/server --timeout=5m
sudo k3s kubectl wait -n hor537-cilium-authority --for=condition=Ready pod/allowed pod/denied --timeout=5m`)

	probe := `set -eu
server=$(sudo k3s kubectl get pod -n hor537-cilium-authority -l app=server -o jsonpath='{.items[0].metadata.name}')
server_ip=$(sudo k3s kubectl get pod "$server" -n hor537-cilium-authority -o jsonpath='{.status.podIP}')
for pod in "$server" allowed denied; do
  for attempt in $(seq 1 60); do
    state=$(sudo k3s kubectl get ciliumendpoint "$pod" -n hor537-cilium-authority -o json | jq -r '.status.state // ""')
    if test "$state" = ready; then break; fi
    test "$attempt" -lt 60
    sleep 1
  done
done
allowed=$(sudo k3s kubectl exec -n hor537-cilium-authority allowed -- wget -q -T 3 -O - "http://$server_ip:8080")
test "$allowed" = cilium-policy-authority=pass
if sudo k3s kubectl exec -n hor537-cilium-authority denied -- wget -q -T 3 -O - "http://$server_ip:8080"; then
  echo 'denied Cilium policy probe unexpectedly connected' >&2
  exit 1
fi
printf 'allowed=%s denied=blocked\n' "$allowed"
sudo k3s kubectl get networkpolicy,ciliumendpoints.cilium.io -n hor537-cilium-authority -o yaml
sudo k3s kubectl get pods -n hor537-cilium-authority -o json | jq -c '[.items[] | {name:.metadata.name,image:.spec.containers[0].image,imageID:.status.containerStatuses[0].imageID}]'
`
	evidence := mustSSHOutput(t, client, probe)
	if !strings.Contains(evidence, "allowed=cilium-policy-authority=pass denied=blocked") {
		t.Fatalf("Cilium authority proof incomplete:\n%s", evidence)
	}
	writeCiliumQualificationEvidence(t, state, "cilium-policy-authority.txt", evidence)
	mustSSHOutput(t, client, "sudo k3s kubectl delete namespace hor537-cilium-authority --wait=true --timeout=2m")
}

func installCiliumQualificationCandidate(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	prepareCandidateChart(t, state.ip, state.privKeyPath)
	importCiliumQualificationSourceImages(t, state)
	plan := prepareCiliumQualificationOverlay(t, state)
	cfg := writeForgeConfigSpec(t, forgeConfigSpec{
		Name: state.runID, Address: state.ip, SSHKeyPath: state.privKeyPath, RunLabel: true,
		DualStack: false, ChartVersion: state.chartVersion,
		ChartRepository: os.Getenv("FORGE_E2E_CHART_REPOSITORY"),
		OverlayRepo:     plan.repository, OverlayRef: plan.ref, Flux: plan.flux,
	})
	out := applyOnce(t, state.forgeBin, state.forgeHome, cfg)
	assertApplyMarkers(t, out,
		"action:     skip", "node ready: true", "certificate substrate applied: true",
		"rwx storage mode: managed-longhorn", "rwx storage prerequisites ready: true",
		"rwx storage substrate applied: true", "chart applied: true", "overlay applied: true",
		"flux installed: true", "gitrepository: ready=True")
	writeCiliumQualificationEvidence(t, state, "candidate-forge-apply.txt", out)

	cluster := remotecluster.Use(t, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
	waitForCandidateControlPlaneReady(t, cluster, "iterabase-system", candidateControlPlaneReadyTimeout)
	assertManagedLonghornInternalTLS(t, cluster, state.runID)
	assertCiliumQualificationCandidateIdentities(t, state, cluster)
	policy := assertStrictRecoveryBackendPolicy(t, state)
	writeCiliumQualificationEvidence(t, state, "strict-recovery-policy-before-fault.json", policy+"\n")
}

func importCiliumQualificationSourceImages(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	archive := os.Getenv(ciliumSourceImageArchiveEnv)
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	remote := filepath.Join("/tmp", filepath.Base(archive))
	transferRWXFixture(t, client, archive, remote)
	mustSSHOutput(t, client, "sudo k3s ctr images import "+candidateShellQuote(remote))
	images := mustSSHOutput(t, client, "sudo k3s ctr images list -q")
	for _, expected := range ciliumQualificationSourceImages() {
		if !strings.Contains("\n"+images+"\n", "\n"+expected+"\n") {
			t.Fatalf("exact PR #65 image %s was not imported:\n%s", expected, images)
		}
	}
	mustSSHOutput(t, client, "rm -f "+candidateShellQuote(remote))
	writeCiliumQualificationEvidence(t, state, "imported-candidate-images.txt", images)
}

func ciliumQualificationSourceImages() []string {
	return []string{
		"localhost/iterabase/control-plane:" + hor527CandidateSourceSHA,
		"localhost/iterabase/control-plane-tool-runner:" + hor527CandidateSourceSHA,
		"localhost/iterabase/control-plane-harness:" + hor527CandidateSourceSHA,
	}
}

func prepareCiliumQualificationOverlay(t *testing.T, state *ciliumQualificationState) candidateOverlayPlan {
	t.Helper()
	values := fmt.Sprintf(`
# DES-HOR-537-01 exact-candidate fixture; qualification only.
global:
  internalTLS:
    enabled: true
reloader:
  enabled: true
storage:
  rwx:
    mode: managed-longhorn
    storageClassName: iterabase-rwx
    managedLonghorn:
      topology: single-node
control-plane:
  dispatch:
    enabled: true
    defaultModel:
      id: storage-readiness-fixture
      api: openai-completions
  image:
    repository: %q
    tag: %q
  toolRunner:
    image:
      repository: %q
      tag: %q
`, "localhost/iterabase/control-plane", hor527CandidateSourceSHA,
		"localhost/iterabase/control-plane-tool-runner", hor527CandidateSourceSHA)
	plan := candidateOverlayPlan{repository: candidateOverlayRepository, ref: candidateOverlayRef, flux: true, values: values}
	prefix := "/tmp/iterabase-hor537-overlay-" + state.runID
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if output, setupErr := sshOutput(client, candidateOverlaySetupScript(values, prefix)); setupErr != nil {
		client.Close()
		t.Fatalf("prepare HOR-537 candidate overlay: %v\n%s", setupErr, output)
	}
	client.Close()
	t.Cleanup(func() {
		cleanupClient, dialErr := sshDial(state.ip, state.privKeyPath)
		if dialErr != nil {
			t.Logf("remove HOR-537 candidate overlay filter: %v", dialErr)
			return
		}
		defer cleanupClient.Close()
		command := fmt.Sprintf("git config --global --unset-all core.attributesFile || true; git config --global --remove-section filter.iterabase-release-candidates || true; rm -f %s %s %s",
			candidateShellQuote(prefix+"-values.yaml"), candidateShellQuote(prefix+"-smudge"), candidateShellQuote(prefix+"-attributes"))
		if output, cleanupErr := sshOutput(cleanupClient, command); cleanupErr != nil {
			t.Logf("remove HOR-537 candidate overlay filter: %v\n%s", cleanupErr, output)
		}
	})
	return plan
}

func assertCiliumQualificationCandidateIdentities(t *testing.T, state *ciliumQualificationState, cluster *remotecluster.Cluster) {
	t.Helper()
	raw := cluster.Kubectl(t, "get", "pods", "-A", "-o", "json")
	var pods struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Containers     []candidateContainerSpec `json:"containers"`
				InitContainers []candidateContainerSpec `json:"initContainers"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses     []candidateContainerStatus `json:"containerStatuses"`
				InitContainerStatuses []candidateContainerStatus `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &pods); err != nil {
		t.Fatalf("decode candidate pod identities: %v", err)
	}
	for _, expected := range ciliumQualificationSourceImages()[:2] {
		found := false
		for _, pod := range pods.Items {
			specs := append(pod.Spec.Containers, pod.Spec.InitContainers...)
			statuses := append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...)
			for _, spec := range specs {
				if spec.Image != expected {
					continue
				}
				for _, status := range statuses {
					if status.Name == spec.Name && strings.Contains(status.ImageID, "sha256:") {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("no running workload requested exact PR #65 image %s with immutable runtime identity", expected)
		}
	}

	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	evidence := mustSSHOutput(t, client, fmt.Sprintf(`set -eu
sudo k3s --version
sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml get metadata %s -n iterabase-system -o json | jq -e '.chart=="iterabase-platform" and .version=="0.3.23"'
sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml get metadata %s-cert-manager -n iterabase-system -o json | jq -e '.chart=="cert-manager-substrate" and .version=="0.3.23"'
sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml get metadata %s-rwx-storage -n longhorn-system -o json | jq -e '.chart=="rwx-storage-substrate" and .version=="0.3.23" and ((.appVersion // .app_version // .appversion)=="1.12.1")'
sudo k3s kubectl get daemonset longhorn-manager -n longhorn-system -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
sudo k3s kubectl get networkpolicy,ciliumendpoints.cilium.io -A -o wide
sudo k3s kubectl get pods -A -o json | jq -c '[.items[] | {namespace:.metadata.namespace,name:.metadata.name,images:[.spec.initContainers[]?.image,.spec.containers[]?.image],runtime:[.status.initContainerStatuses[]?.imageID,.status.containerStatuses[]?.imageID]}]'
%s
`, state.runID, state.runID, state.runID, ciliumBaselineEvidenceCommand()))
	for _, marker := range []string{
		`"chart": "iterabase-platform"`,
		`"chart": "cert-manager-substrate"`,
		`"chart": "rwx-storage-substrate"`,
		"longhorn-manager:v1.12.1@sha256:83b79f57043fe1405e68bc0d4c7987accbc6bb512def3e0db12b31966c070801",
		"cilium-helm-identity=cilium|1.19.7|1.19.7",
	} {
		if !strings.Contains(evidence, marker) {
			t.Fatalf("candidate identity evidence missing %q:\n%s", marker, evidence)
		}
	}
	writeCiliumQualificationEvidence(t, state, "candidate-identities.txt", evidence)
}

type recoveryBackendPolicy struct {
	Spec struct {
		PolicyTypes []string `json:"policyTypes"`
		PodSelector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"podSelector"`
		Ingress []struct {
			From []struct {
				PodSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"podSelector,omitempty"`
				NamespaceSelector any `json:"namespaceSelector,omitempty"`
				IPBlock           any `json:"ipBlock,omitempty"`
			} `json:"from"`
			Ports []struct {
				Protocol string `json:"protocol"`
				Port     any    `json:"port"`
			} `json:"ports"`
		} `json:"ingress"`
	} `json:"spec"`
}

func assertStrictRecoveryBackendPolicy(t *testing.T, state *ciliumQualificationState) string {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw := mustSSHOutput(t, client, "sudo k3s kubectl get networkpolicy longhorn-recovery-backend -n longhorn-system -o json")
	var policy recoveryBackendPolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		t.Fatalf("decode recovery-backend NetworkPolicy: %v", err)
	}
	spec := policy.Spec
	if len(spec.PolicyTypes) != 1 || spec.PolicyTypes[0] != "Ingress" ||
		len(spec.PodSelector.MatchLabels) != 1 || spec.PodSelector.MatchLabels["longhorn.io/recovery-backend"] != "longhorn-recovery-backend" ||
		len(spec.Ingress) != 1 || len(spec.Ingress[0].From) != 1 || len(spec.Ingress[0].Ports) != 1 {
		t.Fatalf("recovery-backend NetworkPolicy shape was widened: %s", raw)
	}
	from := spec.Ingress[0].From[0]
	if from.PodSelector == nil || len(from.PodSelector.MatchLabels) != 1 || from.PodSelector.MatchLabels["longhorn.io/component"] != "share-manager" ||
		from.NamespaceSelector != nil || from.IPBlock != nil {
		t.Fatalf("recovery-backend source selector was widened: %s", raw)
	}
	port := spec.Ingress[0].Ports[0]
	portNumber, ok := port.Port.(float64)
	if !ok || portNumber != 9503 || port.Protocol != "TCP" {
		t.Fatalf("recovery-backend port contract changed: %s", raw)
	}
	return strings.TrimSpace(raw)
}

func establishCiliumQualificationAgentPool(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	harness := "localhost/iterabase/control-plane-harness:" + hor527CandidateSourceSHA
	manifest := fmt.Sprintf(`cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: platform.iterabase.com/v1alpha1
kind: AgentPool
metadata:
  name: forge-storage-pool
  namespace: iterabase-system
spec:
  replicas: 2
  workerImage: %s
  podSecurity: baseline
  identity:
    trustDomain: iterabase.local
    caSecretRef: {name: %s-control-plane-gateway-ca}
  sandbox:
    storageClassName: iterabase-rwx
    accessMode: ReadWriteMany
    size: 2Gi
  gateways:
    controlPlane:
      url: https://%s-control-plane-dispatch.iterabase-system.svc:8091
      serverName: %s-control-plane-dispatch.iterabase-system.svc
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: control-plane, app.kubernetes.io/component: dispatch}}}
    toolGateway:
      url: https://%s-control-plane-gateway.iterabase-system.svc:8090
      serverName: %s-control-plane-gateway.iterabase-system.svc
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: control-plane, app.kubernetes.io/component: gateway}}}
    inferenceGateway:
      url: https://%s-gateway.iterabase-system.svc:8443
      serverName: %s-gateway.iterabase-system.svc
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: inference-gateway}}}
  networkPolicy: {egress: denied}
  workspaceTools: false
YAML`, harness, state.runID, state.runID, state.runID, state.runID, state.runID, state.runID, state.runID)
	mustSSHOutput(t, client, manifest)
	waitForManagedAgentPoolReady(t, client, 10*time.Minute)

	status := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.spec.replicas}|{.status.ready}|{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}|{.status.conditions[?(@.type=="OperationalReadinessReached")].status}'`))
	if status != "2|true|2|StorageReady|True" {
		t.Fatalf("managed AgentPool did not establish durable two-worker readiness: %q", status)
	}
	state.initialWorkerUIDs = qualificationWorkerUIDs(t, client)
	for pod, uid := range state.initialWorkerUIDs {
		identity := strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl get pod %s -n iterabase-system -o jsonpath='{.spec.containers[0].image}|{.status.containerStatuses[0].imageID}'`, candidateShellQuote(pod))))
		if !strings.HasPrefix(identity, harness+"|sha256:") || uid == "" {
			t.Fatalf("worker %s exact image/UID identity=%q uid=%q", pod, identity, uid)
		}
	}
	claim := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	parts := strings.Split(claim, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("managed AgentPool claim identity=%q", claim)
	}
	state.agentPoolPVCUID = parts[0]
	state.longhornVolume = strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl get pv %s -o jsonpath='{.spec.csi.volumeHandle}'`, candidateShellQuote(parts[1]))))
	if state.longhornVolume == "" {
		t.Fatal("managed AgentPool PV has no Longhorn volume identity")
	}

	state.priorPayload = "HOR-537-prior-data|" + hor527CandidateSourceSHA + "|" + state.runID
	state.priorChecksum = fmt.Sprintf("%x", sha256.Sum256([]byte(state.priorPayload+"\n")))
	writeScript := fmt.Sprintf(`set -eu
mkdir -p /data/sandboxes/hor537
printf '%%s\n' %s > /data/sandboxes/hor537/prior-data
printf '%%s  %%s\n' %s prior-data > /data/sandboxes/hor537/prior-data.sha256
cd /data/sandboxes/hor537
sha256sum -c prior-data.sha256
sync
`, candidateShellQuote(state.priorPayload), candidateShellQuote(state.priorChecksum))
	writeOutput := mustSSHOutput(t, client, fmt.Sprintf("sudo k3s kubectl exec -n iterabase-system forge-storage-pool-worker-0 -- /bin/sh -ceu %s", candidateShellQuote(writeScript)))
	readScript := `set -eu
cd /data/sandboxes/hor537
sha256sum -c prior-data.sha256
printf 'prior-payload=%s\n' "$(cat prior-data)"
`
	readOutput := mustSSHOutput(t, client, fmt.Sprintf("sudo k3s kubectl exec -n iterabase-system forge-storage-pool-worker-1 -- /bin/sh -ceu %s", candidateShellQuote(readScript)))
	if !strings.Contains(writeOutput, "prior-data: OK") || !strings.Contains(readOutput, "prior-data: OK") || !strings.Contains(readOutput, state.priorPayload) {
		t.Fatalf("pre-fault RWX checksum proof incomplete: writer=%q reader=%q", writeOutput, readOutput)
	}

	shareManager := "share-manager-" + state.longhornVolume
	shareIdentity := strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl get pod %s -n longhorn-system -o jsonpath='{.metadata.name}|{.metadata.uid}|{.status.conditions[?(@.type=="Ready")].status}'`, candidateShellQuote(shareManager))))
	shareParts := strings.Split(shareIdentity, "|")
	if len(shareParts) != 3 || shareParts[2] != "True" {
		t.Fatalf("established share-manager identity=%q", shareIdentity)
	}
	state.initialShareManager, state.initialShareManagerUID = shareParts[0], shareParts[1]
	policy := assertStrictRecoveryBackendPolicy(t, state)
	evidence := fmt.Sprintf("agentpool=%s\npvc_uid=%s\nvolume=%s\nworkers=%v\nshare_manager=%s\nprior_checksum=%s\nwriter=%sreader=%sstrict_policy=%s\n",
		status, state.agentPoolPVCUID, state.longhornVolume, state.initialWorkerUIDs,
		shareIdentity, state.priorChecksum, writeOutput, readOutput, policy)
	writeCiliumQualificationEvidence(t, state, "pre-fault-rwx-evidence.txt", evidence)
}

func qualificationWorkerUIDs(t *testing.T, client *ssh.Client) map[string]string {
	t.Helper()
	result := make(map[string]string, 2)
	for _, pod := range []string{"forge-storage-pool-worker-0", "forge-storage-pool-worker-1"} {
		identity := strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl get pod %s -n iterabase-system -o jsonpath='{.metadata.uid}|{.status.conditions[?(@.type=="Ready")].status}'`, candidateShellQuote(pod))))
		parts := strings.Split(identity, "|")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "True" {
			t.Fatalf("worker %s identity/readiness=%q", pod, identity)
		}
		result[pod] = parts[0]
	}
	if result["forge-storage-pool-worker-0"] == result["forge-storage-pool-worker-1"] {
		t.Fatalf("two desired workers share one UID: %v", result)
	}
	return result
}

type ciliumRecoveryObservation struct {
	ClaimUID string `json:"claimUID"`
	Agent    struct {
		Desired           int    `json:"desired"`
		Ready             bool   `json:"ready"`
		ReadyReplicas     int    `json:"readyReplicas"`
		StorageReason     string `json:"storageReason"`
		Operational       string `json:"operational"`
		ReplacementStatus string `json:"replacementStatus"`
		ReplacementReason string `json:"replacementReason"`
	} `json:"agent"`
	Workers []struct {
		Name  string `json:"name"`
		UID   string `json:"uid"`
		Ready string `json:"ready"`
	} `json:"workers"`
	Volume struct {
		Name       string `json:"name"`
		Robustness string `json:"robustness"`
		State      string `json:"state"`
		ShareState string `json:"shareState"`
	} `json:"volume"`
	ShareManagers []struct {
		Name  string `json:"name"`
		UID   string `json:"uid"`
		Phase string `json:"phase"`
		Ready string `json:"ready"`
	} `json:"shareManagers"`
}

func qualifyCiliumRecovery(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	watcher := startCiliumQualificationWatcher(t, state)
	defer watcher.stop(t)

	state.faultStartedAt = time.Now().UTC()
	deleteOutput, deleteErr := sshOutput(client, ciliumFaultInjectionCommand(state.initialShareManager))
	state.faultAcceptedAt = time.Now().UTC()
	if deleteErr != nil {
		t.Fatalf("share-manager fault injection was not accepted: %v\n%s", deleteErr, deleteOutput)
	}
	deadline := state.faultStartedAt.Add(ciliumRecoveryLimit)
	for time.Now().Before(deadline) {
		observation, observationErr := readCiliumRecoveryObservation(client, state.longhornVolume)
		if observationErr != nil {
			t.Fatalf("read recovery observation: %v", observationErr)
		}
		state.lastObservation = observation
		if ciliumRecoveryConverged(state, observation) {
			postEvidence := executeCiliumPostRecoveryIO(t, client, state)
			state.recoveredAt = time.Now().UTC()
			elapsed := state.recoveredAt.Sub(state.faultStartedAt)
			if elapsed > ciliumRecoveryLimit {
				t.Fatalf("post-recovery I/O completed after the 60-second deadline: %s", elapsed)
			}
			policy := assertStrictRecoveryBackendPolicy(t, state)
			finalNetwork := mustSSHOutput(t, client, `set -eu
sudo k3s kubectl get ciliumendpoints.cilium.io -A -o wide
sudo k3s kubectl get networkpolicy longhorn-recovery-backend -n longhorn-system -o yaml
cilium_pod=$(sudo k3s kubectl get pods -n kube-system -l k8s-app=cilium -o jsonpath='{.items[0].metadata.name}')
sudo k3s kubectl exec -n kube-system "$cilium_pod" -c cilium-agent -- cilium-dbg status --brief
sudo k3s kubectl get volumes.longhorn.io,engines.longhorn.io,replicas.longhorn.io,sharemanagers.longhorn.io -n longhorn-system -o wide
sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o yaml
sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o wide
printf 'desired=%s\n' "$(sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.spec.replicas}')"
`)
			observationJSON, _ := json.MarshalIndent(observation, "", "  ")
			evidence := fmt.Sprintf("fault_delete_started=%s\nfault_delete_accepted=%s\nrecovered=%s\nelapsed=%s\ndelete_output=%s\nobservation=%s\npost_io=%s\npost_checksum=%s\nstrict_policy=%s\n%s",
				state.faultStartedAt.Format(time.RFC3339Nano), state.faultAcceptedAt.Format(time.RFC3339Nano),
				state.recoveredAt.Format(time.RFC3339Nano), elapsed, deleteOutput, observationJSON,
				postEvidence, state.postChecksum, policy, finalNetwork)
			writeCiliumQualificationEvidence(t, state, "recovery-success.txt", evidence)
			t.Logf("HOR-537 Cilium qualification recovered exact prior data and fresh RWX I/O in %s", elapsed)
			return
		}
		time.Sleep(time.Second)
	}
	last, _ := json.MarshalIndent(state.lastObservation, "", "  ")
	t.Fatalf("HOR-537 qualification blocker remains: no fresh-worker RWX recovery within %s of accepted share-manager deletion; last observation=%s", ciliumRecoveryLimit, last)
}

func ciliumFaultInjectionCommand(shareManager string) string {
	return fmt.Sprintf("sudo k3s kubectl delete pod %s -n longhorn-system --grace-period=0 --force --wait=false", candidateShellQuote(shareManager))
}

func readCiliumRecoveryObservation(client *ssh.Client, volume string) (ciliumRecoveryObservation, error) {
	command := fmt.Sprintf(`set -eu
claim_uid=$(sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}')
agent=$(sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o json | jq -c '{desired:.spec.replicas,ready:(.status.ready // false),readyReplicas:(.status.readyReplicas // 0),storageReason:([.status.conditions[]? | select(.type=="StorageReady")][0].reason // ""),operational:([.status.conditions[]? | select(.type=="OperationalReadinessReached")][0].status // ""),replacementStatus:([.status.conditions[]? | select(.type=="StorageWorkerReplacementPending")][0].status // ""),replacementReason:([.status.conditions[]? | select(.type=="StorageWorkerReplacementPending")][0].reason // "")}')
workers=$(sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o json | jq -c '[.items[] | {name:.metadata.name,uid:.metadata.uid,ready:([.status.conditions[]? | select(.type=="Ready")][0].status // "")}] | sort_by(.name)')
volume=$(sudo k3s kubectl get volume.longhorn.io %s -n longhorn-system -o json | jq -c '{name:.metadata.name,robustness:(.status.robustness // ""),state:(.status.state // ""),shareState:(.status.shareState // "")}')
share_managers=$(sudo k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager -o json | jq -c '[.items[] | select(.metadata.name=="share-manager-%s") | {name:.metadata.name,uid:.metadata.uid,phase:(.status.phase // ""),ready:([.status.conditions[]? | select(.type=="Ready")][0].status // "")}]')
printf '{"claimUID":"%%s","agent":%%s,"workers":%%s,"volume":%%s,"shareManagers":%%s}\n' "$claim_uid" "$agent" "$workers" "$volume" "$share_managers"
`, candidateShellQuote(volume), volume)
	output, err := sshOutput(client, command)
	if err != nil {
		return ciliumRecoveryObservation{}, fmt.Errorf("query bounded product/storage state: %w: %s", err, output)
	}
	var observation ciliumRecoveryObservation
	if err := json.Unmarshal([]byte(output), &observation); err != nil {
		return observation, fmt.Errorf("decode recovery observation: %w: %s", err, output)
	}
	return observation, nil
}

func ciliumRecoveryConverged(state *ciliumQualificationState, observation ciliumRecoveryObservation) bool {
	if observation.ClaimUID != state.agentPoolPVCUID ||
		observation.Agent.Desired != 2 || !observation.Agent.Ready || observation.Agent.ReadyReplicas != 2 ||
		observation.Agent.StorageReason != "StorageReady" || observation.Agent.Operational != "True" ||
		observation.Agent.ReplacementStatus != "False" || observation.Agent.ReplacementReason != "FreshWorkersReady" ||
		observation.Volume.Name != state.longhornVolume || observation.Volume.Robustness != "healthy" || observation.Volume.State != "attached" || observation.Volume.ShareState != "running" ||
		len(observation.Workers) != 2 || len(observation.ShareManagers) != 1 {
		return false
	}
	for _, worker := range observation.Workers {
		if worker.Ready != "True" || worker.UID == "" || state.initialWorkerUIDs[worker.Name] == "" || state.initialWorkerUIDs[worker.Name] == worker.UID {
			return false
		}
	}
	shareManager := observation.ShareManagers[0]
	return shareManager.Ready == "True" && shareManager.Phase == "Running" && shareManager.UID != "" && shareManager.UID != state.initialShareManagerUID
}

func executeCiliumPostRecoveryIO(t *testing.T, client *ssh.Client, state *ciliumQualificationState) string {
	t.Helper()
	postPayload := "HOR-537-post-recovery|" + hor527CandidateSourceSHA + "|" + state.runID
	state.postChecksum = fmt.Sprintf("%x", sha256.Sum256([]byte(postPayload+"\n")))
	writeScript := fmt.Sprintf(`set -eu
cd /data/sandboxes/hor537
sha256sum -c prior-data.sha256
printf '%%s\n' %s > post-recovery-data
printf '%%s  %%s\n' %s post-recovery-data > post-recovery-data.sha256
sha256sum -c post-recovery-data.sha256
sync
`, candidateShellQuote(postPayload), candidateShellQuote(state.postChecksum))
	writerOutput, err := sshOutput(client, fmt.Sprintf("sudo k3s kubectl exec -n iterabase-system forge-storage-pool-worker-0 -- /bin/sh -ceu %s", candidateShellQuote(writeScript)))
	if err != nil {
		t.Fatalf("single post-recovery RWX writer attempt failed (no application retry is permitted): %v\n%s", err, writerOutput)
	}
	readScript := `set -eu
cd /data/sandboxes/hor537
sha256sum -c prior-data.sha256
sha256sum -c post-recovery-data.sha256
printf 'prior-payload=%s\npost-payload=%s\n' "$(cat prior-data)" "$(cat post-recovery-data)"
`
	readerOutput, err := sshOutput(client, fmt.Sprintf("sudo k3s kubectl exec -n iterabase-system forge-storage-pool-worker-1 -- /bin/sh -ceu %s", candidateShellQuote(readScript)))
	if err != nil {
		t.Fatalf("single post-recovery RWX reader attempt failed (no application retry is permitted): %v\n%s", err, readerOutput)
	}
	for _, marker := range []string{"prior-data: OK", "post-recovery-data: OK", state.priorPayload, postPayload} {
		if !strings.Contains(writerOutput+readerOutput, marker) {
			t.Fatalf("post-recovery checksum evidence missing %q: writer=%q reader=%q", marker, writerOutput, readerOutput)
		}
	}
	return "writer=" + writerOutput + "reader=" + readerOutput
}

type ciliumQualificationWatcher struct {
	state     *ciliumQualificationState
	pid       string
	remoteDir string
	stopped   bool
}

func startCiliumQualificationWatcher(t *testing.T, state *ciliumQualificationState) *ciliumQualificationWatcher {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("start Cilium recovery watcher: %v", err)
	}
	defer client.Close()
	remoteDir := fmt.Sprintf("/tmp/hor537-recovery-%d", time.Now().UnixNano())
	remoteScript := remoteDir + ".sh"
	encoded := base64.StdEncoding.EncodeToString([]byte(ciliumQualificationWatcherScript()))
	command := fmt.Sprintf("sudo mkdir -p %s; printf %%s %s | base64 --decode | sudo tee %s >/dev/null; sudo chmod 0700 %s; sudo sh -c 'nohup bash \"$1\" \"$2\" >\"$2/driver.log\" 2>&1 </dev/null & echo $!' _ %s %s",
		candidateShellQuote(remoteDir), candidateShellQuote(encoded), candidateShellQuote(remoteScript), candidateShellQuote(remoteScript),
		candidateShellQuote(remoteScript), candidateShellQuote(remoteDir))
	output, err := sshOutput(client, command)
	if err != nil {
		t.Fatalf("start Cilium recovery watcher: %v\n%s", err, output)
	}
	pid := strings.TrimSpace(output)
	if parsed, parseErr := strconv.Atoi(pid); parseErr != nil || parsed <= 0 {
		t.Fatalf("Cilium recovery watcher returned invalid PID %q", pid)
	}
	ready := fmt.Sprintf("for i in $(seq 1 100); do test -f %s/ready && exit 0; sleep 0.1; done; sudo cat %s/driver.log >&2; exit 1", candidateShellQuote(remoteDir), candidateShellQuote(remoteDir))
	if output, err := sshOutput(client, ready); err != nil {
		t.Fatalf("Cilium recovery watcher did not become ready: %v\n%s", err, output)
	}
	return &ciliumQualificationWatcher{state: state, pid: pid, remoteDir: remoteDir}
}

func ciliumQualificationWatcherScript() string {
	return `#!/usr/bin/env bash
set -uo pipefail
output_dir=${1:?output directory required}
child_pids="$output_dir/child-pids"
: >"$child_pids"

snapshot() {
  reason=${1:?snapshot reason required}
  {
    printf '\n===== %s snapshot=%s =====\n' "$(date -u +%FT%TZ.%N)" "$reason"
    echo '--- strict recovery policy ---'
    k3s kubectl get networkpolicy longhorn-recovery-backend -n longhorn-system -o yaml || true
    echo '--- Cilium endpoints and health ---'
    k3s kubectl get ciliumendpoints.cilium.io -A -o wide || true
    cilium_pod=$(k3s kubectl get pods -n kube-system -l k8s-app=cilium -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    [[ -z "$cilium_pod" ]] || k3s kubectl exec -n kube-system "$cilium_pod" -c cilium-agent -- cilium-dbg status --brief || true
    echo '--- Longhorn state ---'
    k3s kubectl get volumes.longhorn.io,engines.longhorn.io,replicas.longhorn.io,sharemanagers.longhorn.io,instancemanagers.longhorn.io -n longhorn-system -o wide || true
    echo '--- share-manager pods ---'
    k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager -o wide || true
    echo '--- AgentPool and workers ---'
    k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o yaml || true
    k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o wide || true
    echo '--- bounded events ---'
    k3s kubectl get events -n longhorn-system --sort-by=.metadata.creationTimestamp | tail -150 || true
    k3s kubectl get events -n iterabase-system --sort-by=.metadata.creationTimestamp | tail -150 || true
  } >>"$output_dir/snapshots.log" 2>&1
}

watch_table() {
  stream=${1:?stream required}
  shift
  while true; do
    k3s kubectl get "$@" --watch --output-watch-events --no-headers 2>>"$output_dir/driver.log" |
      while IFS= read -r event; do printf '%s|%s|%s\n' "$(date -u +%FT%TZ.%N)" "$stream" "$event"; done >>"$output_dir/$stream.log"
    printf '%s stream=%s closed; reconnecting\n' "$(date -u +%FT%TZ.%N)" "$stream" >>"$output_dir/driver.log"
    sleep 1
  done
}

follow_share_manager() {
  pod=${1:?pod required}
  uid=${2:?uid required}
  while [[ "$(k3s kubectl get pod "$pod" -n longhorn-system -o jsonpath='{.metadata.uid}' 2>/dev/null || true)" == "$uid" ]]; do
    {
      printf '\n===== %s pod=%s uid=%s =====\n' "$(date -u +%FT%TZ.%N)" "$pod" "$uid"
      if k3s kubectl logs -n longhorn-system "$pod" -c share-manager --follow --timestamps; then return; fi
    } >>"$output_dir/share-manager-$uid.log" 2>&1
    sleep 0.2
  done
}

watch_share_manager_pods() {
  while true; do
    k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager --watch --output-watch-events --no-headers \
      -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,UID:.object.metadata.uid,PHASE:.object.status.phase,POD_IP:.object.status.podIP,NODE:.object.spec.nodeName' 2>>"$output_dir/driver.log" |
      while IFS= read -r event; do
        read -r event_type pod uid _ <<<"$event"
        [[ -n "$event_type" && -n "$pod" && -n "$uid" ]] || continue
        printf '%s|%s\n' "$(date -u +%FT%TZ.%N)" "$event" >>"$output_dir/share-manager-pods.log"
        if [[ ! -e "$output_dir/seen-$uid" ]]; then
          : >"$output_dir/seen-$uid"
          follow_share_manager "$pod" "$uid" &
          printf '%s\n' "$!" >>"$child_pids"
        fi
      done
    sleep 1
  done
}

cleanup() {
  trap - TERM INT EXIT
  snapshot watcher-stop
  while IFS= read -r pid; do kill "$pid" >/dev/null 2>&1 || true; done <"$child_pids"
  for pid in $(jobs -pr); do kill "$pid" >/dev/null 2>&1 || true; done
  wait || true
}
trap cleanup TERM INT EXIT

watch_share_manager_pods &
watch_table agentpools agentpools -A -o 'custom-columns=TYPE:.type,NAMESPACE:.object.metadata.namespace,NAME:.object.metadata.name,READY:.object.status.ready,READY_REPLICAS:.object.status.readyReplicas,CONDITIONS:.object.status.conditions,MESSAGE:.object.status.message' &
watch_table workers pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,UID:.object.metadata.uid,PHASE:.object.status.phase,READY:.object.status.conditions[?(@.type=="Ready")].status' &
watch_table volumes volumes.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,ROBUSTNESS:.object.status.robustness,STATE:.object.status.state,SHARE_STATE:.object.status.shareState,ENDPOINT:.object.status.shareEndpoint' &
watch_table engines engines.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,VOLUME:.object.spec.volumeName,STATE:.object.status.currentState,REPLICAS:.object.status.replicaModeMap' &
watch_table replicas replicas.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,VOLUME:.object.spec.volumeName,STATE:.object.status.currentState,FAILED_AT:.object.spec.failedAt,HEALTHY_AT:.object.spec.healthyAt' &
watch_table sharemanagers sharemanagers.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,STATE:.object.status.state,ENDPOINT:.object.status.endpoint,OWNER:.object.status.ownerID' &
watch_table cilium-endpoints ciliumendpoints.cilium.io -A -o 'custom-columns=TYPE:.type,NAMESPACE:.object.metadata.namespace,NAME:.object.metadata.name,ID:.object.status.identity.id,STATE:.object.status.state,POLICY:.object.status.policy.spec.policy-enabled' &
snapshot watcher-start
: >"$output_dir/ready"
wait
`
}

func (watcher *ciliumQualificationWatcher) stop(t *testing.T) {
	t.Helper()
	if watcher.stopped {
		return
	}
	watcher.stopped = true
	client, err := sshDial(watcher.state.ip, watcher.state.privKeyPath)
	if err != nil {
		t.Logf("retain Cilium recovery watcher: %v", err)
		return
	}
	defer client.Close()
	command := fmt.Sprintf("sudo kill %s >/dev/null 2>&1 || true; for i in $(seq 1 40); do sudo kill -0 %s >/dev/null 2>&1 || break; sleep 0.25; done; sudo bash -c 'for file in \"$1\"/*; do test -f \"$file\" || continue; printf \"===== %%s =====\\n\" \"$file\"; cat \"$file\"; printf \"\\n\"; done' _ %s",
		candidateShellQuote(watcher.pid), candidateShellQuote(watcher.pid), candidateShellQuote(watcher.remoteDir))
	output, commandErr := sshOutput(client, command)
	if commandErr != nil {
		t.Logf("stop Cilium recovery watcher: %v", commandErr)
	}
	writeCiliumQualificationEvidence(t, watcher.state, "share-manager-attempts-and-transitions.txt", output)
}

func collectCiliumQualificationDiagnostics(t *testing.T, state *ciliumQualificationState) {
	t.Helper()
	state.diagnostics.recordDomain(t)
	state.diagnostics.collectSSH(t, state.ip, state.privKeyPath, map[string]string{
		"cilium-qualification-substrate": ciliumBaselineEvidenceCommand(),
		"cilium-qualification-storage": `sudo k3s kubectl get networkpolicy,ciliumendpoints.cilium.io -A -o yaml 2>&1 || true
sudo k3s kubectl get volumes.longhorn.io,engines.longhorn.io,replicas.longhorn.io,sharemanagers.longhorn.io,instancemanagers.longhorn.io -n longhorn-system -o yaml 2>&1 || true
sudo k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager -o yaml 2>&1 || true
sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o yaml 2>&1 || true
sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o yaml 2>&1 || true
sudo k3s kubectl get events -A --sort-by=.metadata.creationTimestamp 2>&1 | tail -400 || true`,
	})
	if !state.faultStartedAt.IsZero() {
		last, _ := json.MarshalIndent(state.lastObservation, "", "  ")
		evidence := fmt.Sprintf("fault_delete_started=%s\nfault_delete_accepted=%s\nlast_observation=%s\n",
			state.faultStartedAt.Format(time.RFC3339Nano), state.faultAcceptedAt.Format(time.RFC3339Nano), last)
		writeCiliumQualificationEvidence(t, state, "recovery-failure-boundary.txt", evidence)
	}
	kubeconfig := filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); err == nil && state.diagnostics.registerBootstrapSecrets(t, state.ip, state.privKeyPath) {
		state.diagnostics.collectSharedCluster(t, kubeconfig)
	}
}

func writeCiliumQualificationEvidence(t *testing.T, state *ciliumQualificationState, name, contents string) {
	t.Helper()
	path := filepath.Join(state.diagnostics.outputDir, name)
	if err := os.WriteFile(path, []byte(state.diagnostics.redactor.String(contents)), 0o600); err != nil {
		t.Fatalf("write HOR-537 evidence %s: %v", name, err)
	}
}

func TestCiliumQualificationWatcherIsBoundedAndEventDriven(t *testing.T) {
	t.Parallel()
	script := ciliumQualificationWatcherScript()
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Cilium qualification watcher syntax: %v\n%s", err, output)
	}
	for _, contract := range []string{
		"--watch --output-watch-events",
		"longhorn.io/component=share-manager",
		"ciliumendpoints.cilium.io",
		"longhorn-recovery-backend",
		"volumes.longhorn.io",
		"engines.longhorn.io",
		"replicas.longhorn.io",
		"sharemanagers.longhorn.io",
		"--follow --timestamps",
		"snapshot watcher-stop",
	} {
		if !strings.Contains(script, contract) {
			t.Fatalf("Cilium qualification watcher missing %q", contract)
		}
	}
}

func TestCiliumQualificationFaultHasNoRepairAction(t *testing.T) {
	t.Parallel()
	command := ciliumFaultInjectionCommand("share-manager-pvc-fixture")
	if !strings.Contains(command, "kubectl delete pod") || !strings.Contains(command, "--wait=false") {
		t.Fatalf("qualification fault is not the approved accepted deletion: %s", command)
	}
	for _, forbidden := range []string{"annotate", "patch", "scale", "rollout", "attachment", "retry"} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("qualification fault contains forbidden repair %q: %s", forbidden, command)
		}
	}
}

func TestCiliumQualificationBootstrapPinsCleanAuthority(t *testing.T) {
	t.Parallel()
	command := ciliumBaselineEvidenceCommand()
	for _, required := range []string{"--flannel-backend=none", "--disable-network-policy", "cilium-dbg status", "cilium-cni", "KUBE-ROUTER", "FLANNEL"} {
		if !strings.Contains(command, required) {
			t.Fatalf("Cilium baseline proof missing %q", required)
		}
	}
	if ciliumRecoveryLimit != 60*time.Second || ciliumQualificationChartHash == "" || hor527CandidateSourceSHA == "" {
		t.Fatal("qualification identities/deadline must remain exact")
	}
}
