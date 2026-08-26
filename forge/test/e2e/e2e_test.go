// Package e2e runs the forge end-to-end test against an ephemeral
// DigitalOcean droplet. It is a separate module so godo/client-go stay out of
// the main module's dependency graph.
//
// Run: make test-e2e   (requires DIGITALOCEAN_TOKEN)
package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	region  = "fra1"
	size    = "s-2vcpu-4gb" // full stack + MetalLB needs headroom; s-1vcpu-2gb timed out on helm --wait
	image   = "ubuntu-24-04-x64"
	k3sPort = 6443
)

type cpuVMProvisioner interface {
	Create(context.Context, string, string) (*godo.Droplet, error)
	PublicIP(context.Context, int) (string, error)
	Destroy(context.Context, int) error
}

type doCPUVMProvisioner struct{ client *godo.Client }

func (provisioner *doCPUVMProvisioner) Create(ctx context.Context, runID, pubKey string) (*godo.Droplet, error) {
	return createDroplet(ctx, provisioner.client, runID, pubKey)
}

func (provisioner *doCPUVMProvisioner) PublicIP(ctx context.Context, id int) (string, error) {
	return waitForIP(ctx, provisioner.client, id)
}

func (provisioner *doCPUVMProvisioner) Destroy(ctx context.Context, id int) error {
	_, err := provisioner.client.Droplets.Delete(ctx, id)
	return err
}

type digitalOceanCPUState struct {
	ctx                 context.Context
	provisioner         cpuVMProvisioner
	ready               func(context.Context, string, string) error
	runID               string
	keep                bool
	pubKey              string
	privKeyPath         string
	droplet             *godo.Droplet
	ip                  string
	forgeBin            string
	forgeHome           string
	chartVersion        string
	managedRWX          bool
	storagePVCUID       string
	storagePV           string
	agentPoolPVCUID     string
	initialWorkerPodUID string
	diagnostics         forgeDiagnostics
}

func newDigitalOceanCPUState(t *testing.T) *digitalOceanCPUState {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		if os.Getenv("FORGE_E2E_REQUIRE_CAPACITY") == "true" {
			t.Fatal("mandatory CPU release validation incomplete — DIGITALOCEAN_TOKEN not set")
		}
		t.Skip("DIGITALOCEAN_TOKEN not set; skipping DigitalOcean CPU scenario")
	}

	state := &digitalOceanCPUState{
		ctx:         context.Background(),
		provisioner: &doCPUVMProvisioner{client: godo.NewFromToken(token)},
		ready:       waitForHostReady,
		runID:       fmt.Sprintf("forge-e2e-%d", time.Now().Unix()),
		keep:        os.Getenv("FORGE_E2E_KEEP") != "",
		forgeHome:   t.TempDir(),
		diagnostics: newForgeDiagnostics(t, "digitalocean-cpu"),
	}
	state.pubKey, state.privKeyPath = generateKey(t)
	state.forgeBin = buildForge(t)
	state.chartVersion = platformChartVersion(t, "")
	state.managedRWX = os.Getenv(storageChartArchiveEnv) != "" && os.Getenv(forceExternalStorageEnv) != "true"
	if os.Getenv(requireManagedTLSEnv) == "true" && !state.managedRWX {
		t.Fatalf("mandatory managed-storage/internal-TLS evidence requires %s and forbids %s=true", storageChartArchiveEnv, forceExternalStorageEnv)
	}
	t.Logf("run %s (keep=%v managedRWX=%v)", state.runID, state.keep, state.managedRWX)
	return state
}

func provisionCPUStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if err := provisionCPUHost(state); err != nil {
		t.Fatal(err)
	}
	t.Logf("droplet ip %s", state.ip)
}

func provisionCPUHost(state *digitalOceanCPUState) error {
	droplet, err := state.provisioner.Create(state.ctx, state.runID, state.pubKey)
	if err != nil {
		return fmt.Errorf("create droplet: %w", err)
	}
	// Retain the resource identity immediately so shared scenario cleanup owns
	// every failure after the cloud API accepts creation.
	state.droplet = droplet
	ip, err := state.provisioner.PublicIP(state.ctx, droplet.ID)
	if err != nil {
		return fmt.Errorf("wait for droplet IP: %w", err)
	}
	state.ip = ip
	if err := state.ready(state.ctx, ip, state.privKeyPath); err != nil {
		return fmt.Errorf("wait for host readiness: %w", err)
	}
	return nil
}

func rejectGPUOnCPUStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	cfgPath := writeForgeConfigGPU(t, state.runID, state.ip, state.privKeyPath)
	out, err := runForgeE(state.forgeBin, state.forgeHome, "apply", "--config", cfgPath)
	if err == nil {
		t.Fatalf("apply should fail preflight with no NVIDIA GPU:\n%s", out)
	}
	if !strings.Contains(out, "no NVIDIA GPU") {
		t.Fatalf("GPU preflight failure did not explain the missing NVIDIA GPU:\n%s", out)
	}
}

func applyBaselineStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	writeEdgeOverlayOnHost(t, state.ip, state.privKeyPath)
	out := applyOnce(t, state.forgeBin, state.forgeHome,
		writeForgeConfig(t, state.runID, state.ip, state.privKeyPath, certificateMigrationSourceVersion))
	assertApplyMarkers(t, out, "node ready: true", "chart applied: true", "overlay applied: true")
	t.Logf("apply output:\n%s", out)
}

func assertBaselineStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	kcPath := filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml")
	checkNodeViaKubeconfig(t, kcPath, state.runID)
	checkGatewayRunning(t, kcPath)
	checkGatewayHealth(t, state.ip)
}

// assertCurrentPlatformStage proves Forge handed the exact desired releases and
// source artifact to a minimally healthy dependent layer. Chart ownership,
// rollout, certificate, gateway, and tool-runner correctness remains in the
// chart/control-plane owner suites.
func assertCurrentPlatformStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()

	assertRemoteHelmChartVersion(t, sc, state.runID, "iterabase-system", state.chartVersion)
	assertRemoteHelmChartVersion(t, sc, state.runID+"-cert-manager", "iterabase-system", state.chartVersion)
	if state.managedRWX {
		assertRemoteHelmChartVersion(t, sc, state.runID+"-rwx-storage", "longhorn-system", state.chartVersion)
	}
	owner := strings.TrimSpace(mustSSHOutput(t, sc,
		`sudo k3s kubectl get crd certificates.cert-manager.io -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}'`))
	wantOwner := state.runID + "-cert-manager"
	if owner != wantOwner {
		t.Fatalf("certificate CRD release owner = %q, want %q", owner, wantOwner)
	}
	ready, revision, digest := pollFluxReady(t, sc, "gitrepository", "overlay", 2*time.Minute)
	if !ready || revision == "" || !isCanonicalSHA256Digest(digest) {
		t.Fatalf("current platform has no exact Ready Flux artifact: ready=%v revision=%q digest=%q", ready, revision, digest)
	}
	mustSSHOutput(t, sc, fmt.Sprintf("sudo k3s kubectl rollout status -n iterabase-system deployment/%s-tool-runner --timeout=300s", state.runID))
	if state.managedRWX {
		storageClass := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get storageclass iterabase-rwx -o jsonpath='{.provisioner}|{.reclaimPolicy}|{.allowVolumeExpansion}|{.parameters.dataEngine}|{.parameters.numberOfReplicas}'`))
		if storageClass != "driver.longhorn.io|Retain|true|v1|1" {
			t.Fatalf("managed RWX StorageClass contract = %q", storageClass)
		}
		attestations := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get configmap -n iterabase-system -l platform.iterabase.com/storage-conformance=true -o jsonpath='{range .items[*]}{.data.storageClassName}|{.data.contractVersion}|{.data.result}{"\n"}{end}'`))
		if !strings.Contains(attestations, "iterabase-rwx|HOR-469/v1|pass") {
			t.Fatalf("managed RWX conformance attestation missing: %q", attestations)
		}
	}

	kcPath := filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml")
	checkGatewayRunning(t, kcPath)
	checkGatewayNodePortHealth(t, kcPath, state.ip)
}

func seedManagedRWXReapplyStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if !state.managedRWX {
		t.Log("published platform baseline predates managed RWX; dedicated exact-candidate storage gates own this stage")
		return
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	manifest := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: forge-rwx-reapply
  namespace: iterabase-system
spec:
  accessModes: [ReadWriteMany]
  storageClassName: iterabase-rwx
  resources:
    requests: {storage: 1Gi}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: forge-rwx-writer
  namespace: iterabase-system
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: writer
          image: debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
          command: [bash, -ceu]
          args: ['printf HOR-469-reapply > /sessions/marker && sync -f /sessions/marker']
          volumeMounts: [{name: sessions, mountPath: /sessions}]
      volumes:
        - name: sessions
          persistentVolumeClaim: {claimName: forge-rwx-reapply}
YAML`
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/forge-rwx-writer --timeout=10m")
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-rwx-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	parts := strings.Split(identity, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("managed RWX seed claim has incomplete identity: %q", identity)
	}
	state.storagePVCUID, state.storagePV = parts[0], parts[1]
}

func assertManagedRWXReapplyStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if !state.managedRWX {
		return
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-rwx-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	if identity != state.storagePVCUID+"|"+state.storagePV {
		t.Fatalf("managed RWX reapply replaced the claim: before=%s|%s after=%s", state.storagePVCUID, state.storagePV, identity)
	}
	manifest := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: forge-rwx-replacement
  namespace: iterabase-system
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: reader
          image: debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
          command: [bash, -ceu]
          args: ['test "$(cat /sessions/marker)" = HOR-469-reapply && printf replacement-persistence=pass']
          volumeMounts: [{name: sessions, mountPath: /sessions}]
      volumes:
        - name: sessions
          persistentVolumeClaim: {claimName: forge-rwx-reapply}
YAML`
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/forge-rwx-replacement --timeout=10m")
	logs := mustSSHOutput(t, sc, "sudo k3s kubectl logs -n iterabase-system job/forge-rwx-replacement")
	if !strings.Contains(logs, "replacement-persistence=pass") {
		t.Fatalf("replacement worker did not preserve committed RWX bytes: %s", logs)
	}
}

func setupManagedAgentPoolStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if !state.managedRWX {
		return
	}
	repository, tag := os.Getenv("HARNESS_IMAGE_REPO"), os.Getenv("HARNESS_IMAGE_TAG")
	if repository == "" || tag == "" {
		t.Fatal("managed AgentPool readiness evidence requires an exact HARNESS_IMAGE_REPO/HARNESS_IMAGE_TAG fixture")
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	manifest := fmt.Sprintf(`cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: platform.iterabase.com/v1alpha1
kind: AgentPool
metadata:
  name: forge-storage-pool
  namespace: iterabase-system
spec:
  replicas: 2
  workerImage: %s:%s
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
YAML`, repository, tag, state.runID, state.runID, state.runID, state.runID, state.runID, state.runID, state.runID)
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=jsonpath='{.status.ready}'=true agentpool/forge-storage-pool --timeout=10m")
	status := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}'`))
	if status != "2|StorageReady" {
		t.Fatalf("managed AgentPool readiness = %q, want 2|StorageReady", status)
	}
	state.agentPoolPVCUID = strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	state.initialWorkerPodUID = strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'`))
	if state.agentPoolPVCUID == "" || len(strings.Fields(state.initialWorkerPodUID)) != 2 {
		t.Fatalf("managed AgentPool identities incomplete: pvc=%q workers=%q", state.agentPoolPVCUID, state.initialWorkerPodUID)
	}
}

func exerciseManagedShareManagerFailureStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if !state.managedRWX {
		return
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	volume := strings.TrimSpace(mustSSHOutput(t, sc, `pv=$(sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.spec.volumeName}'); sudo k3s kubectl get pv "$pv" -o jsonpath='{.spec.csi.volumeHandle}'`))
	if volume == "" {
		t.Fatal("managed AgentPool PV has no Longhorn volume handle")
	}
	shareManager := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager -o name | grep %s | head -1`, volume)))
	if shareManager == "" {
		t.Fatalf("no active share-manager for %s", volume)
	}
	mustSSHOutput(t, sc, fmt.Sprintf("sudo k3s kubectl delete -n longhorn-system %s --grace-period=0 --force --wait=false", shareManager))
	mustSSHOutput(t, sc, fmt.Sprintf("sudo k3s kubectl annotate agentpool forge-storage-pool -n iterabase-system --overwrite platform.iterabase.com/storage-fault-trigger=%d", time.Now().UnixNano()))

	deadline := time.Now().Add(3 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		out, commandErr := sshOutput(sc, `sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.status.ready}|{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}|{.status.message}'`)
		last = strings.TrimSpace(out)
		parts := strings.SplitN(last, "|", 4)
		if commandErr == nil && len(parts) == 4 && parts[0] == "false" && parts[1] == "0" && parts[2] == "StorageRecoveryPending" {
			break
		}
		time.Sleep(time.Second)
	}
	if !strings.HasPrefix(last, "false|0|StorageRecoveryPending|") {
		t.Fatalf("AgentPool did not fail closed on share-manager loss: %q", last)
	}
	if count := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool --no-headers 2>/dev/null | wc -l`)); count != "0" {
		t.Fatalf("storage-unready AgentPool retained %s worker pods/scheduling credits", count)
	}
}

func assertManagedStorageRecoveryStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if !state.managedRWX {
		return
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=jsonpath='{.status.ready}'=true agentpool/forge-storage-pool --timeout=10m")
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	if identity != state.agentPoolPVCUID {
		t.Fatalf("storage recovery replaced the AgentPool PVC: before=%s after=%s", state.agentPoolPVCUID, identity)
	}
	workers := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'`))
	if len(strings.Fields(workers)) != 2 {
		t.Fatalf("storage recovery has incomplete fresh worker set: %q", workers)
	}
	for _, oldUID := range strings.Fields(state.initialWorkerPodUID) {
		if strings.Contains(workers, oldUID) {
			t.Fatalf("storage recovery reused affected worker %s instead of a fresh worker set", oldUID)
		}
	}
	status := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.status.ready}|{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}'`))
	if status != "true|2|StorageReady" {
		t.Fatalf("managed storage recovery status=%q", status)
	}
}

func reapplyCurrentPlatformStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	prepareCandidateChart(t, state.ip, state.privKeyPath)
	plan := prepareCandidateOverlay(t, state.runID, state.ip, state.privKeyPath)
	cfgPath := writeCurrentOverlayForgeConfig(
		t, state.runID, state.ip, state.privKeyPath, state.chartVersion, plan,
	)
	out := applyOnce(t, state.forgeBin, state.forgeHome, cfgPath)
	markers := []string{"action:     skip", "node ready: true", "certificate substrate applied: true", "chart applied: true", "overlay applied: true"}
	if state.managedRWX {
		markers = append(markers, "rwx storage mode: managed-longhorn", "rwx storage prerequisites ready: true", "rwx storage substrate applied: true")
	} else {
		markers = append(markers, "rwx storage mode: external")
	}
	if plan.flux {
		markers = append(markers, "flux installed: true")
	}
	assertApplyMarkers(t, out, markers...)
}

func assertRemoteHelmChartVersion(t *testing.T, sc *ssh.Client, release, namespace, want string) {
	t.Helper()
	out := mustSSHOutput(t, sc, fmt.Sprintf("sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml helm get metadata %s -n %s -o json", release, namespace))
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &metadata); err != nil {
		t.Fatalf("decode Helm metadata for %s: %v\n%s", release, err, out)
	}
	if metadata.Version != want {
		t.Fatalf("Helm release %s chart version = %q, want %q", release, metadata.Version, want)
	}
}

func assertApplyMarkers(t *testing.T, out string, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if !strings.Contains(out, marker) {
			t.Fatalf("apply output missing %q:\n%s", marker, out)
		}
	}
}

func (state *digitalOceanCPUState) cleanup(t *testing.T) {
	t.Helper()
	if state.droplet == nil {
		return
	}
	if state.keep {
		t.Logf("keeping droplet %d (run %s) for debugging", state.droplet.ID, state.runID)
		return
	}
	if err := state.destroyCPUHost(); err != nil {
		t.Errorf("destroy CPU droplet %d: %v (tagged reaper remains the crash-safety fallback)", state.droplet.ID, err)
	}
}

func (state *digitalOceanCPUState) destroyCPUHost() error {
	state.diagnostics.setDomain(failureDomainCleanup)
	return state.provisioner.Destroy(state.ctx, state.droplet.ID)
}

func generateKey(t *testing.T) (pubKeyStr, privKeyPath string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubSSH, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	pubKeyStr = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubSSH)))

	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	privPEM := pem.EncodeToMemory(block)

	privKeyPath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(privKeyPath, privPEM, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return pubKeyStr, privKeyPath
}

func cloudInit(pubKeyStr string) string {
	return fmt.Sprintf(`#cloud-config
packages: [curl]
users:
  - name: forge
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
`, pubKeyStr)
}

func createDroplet(ctx context.Context, client *godo.Client, name, pubKeyStr string) (*godo.Droplet, error) {
	d, _, err := client.Droplets.Create(ctx, newCPUDropletRequest(name, pubKeyStr))
	return d, err
}

func newCPUDropletRequest(name, pubKeyStr string) *godo.DropletCreateRequest {
	return &godo.DropletCreateRequest{
		Name:     name,
		Region:   region,
		Size:     size,
		UserData: cloudInit(pubKeyStr),
		IPv6:     true,
		Tags:     []string{"forge-e2e", name},
		Image:    godo.DropletCreateImage{Slug: image},
	}
}

func waitForIP(ctx context.Context, client *godo.Client, id int) (string, error) {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		d, _, err := client.Droplets.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if d.Status == "active" {
			for _, n := range d.Networks.V4 {
				if n.Type == "public" {
					return n.IPAddress, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("droplet %d never became active with a public IP", id)
		}
		time.Sleep(5 * time.Second)
	}
}

// waitForHostReady waits until the droplet accepts SSH AND cloud-init has
// finished applying its user-data (the forge user, passwordless sudo, curl).
// Returning only once cloud-init reports "done" prevents forge's preflight from
// racing cloud-init — e.g. `sudo -n true` failing with "passwordless sudo
// required" before the sudoers rule is applied, or curl being absent before
// `packages: [curl]` completes. The forge user is created by cloud-init, so SSH
// can only succeed once cloud-init has at least started.
func waitForHostReady(ctx context.Context, ip, keyPath string) error {
	const deadline = 5 * time.Minute
	end := time.Now().Add(deadline)
	var lastStatus string
	var lastErr error
	for time.Now().Before(end) {
		client, err := sshDial(ip, keyPath)
		if err == nil {
			out, statusErr := sshOutput(client, "cloud-init status")
			client.Close()
			lastStatus, lastErr = strings.TrimSpace(out), statusErr
			switch {
			case strings.Contains(out, "status: done"):
				return nil
			case strings.Contains(out, "status: error"):
				return fmt.Errorf("cloud-init failed on %s: %s", ip, lastStatus)
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("host %s never became ready (SSH up + cloud-init done) within %s (last status %q, last error %v)", ip, deadline, lastStatus, lastErr)
}

// sshOutput runs a command over an SSH client and returns its combined output.
func sshOutput(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

func sshDial(ip, keyPath string) (*ssh.Client, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            "forge",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // ephemeral test droplet
		Timeout:         10 * time.Second,
	}
	return ssh.Dial("tcp", ip+":22", cfg)
}

func buildForge(t *testing.T) string {
	t.Helper()
	breakEmptyDirPolicy := os.Getenv("FORGE_E2E_BREAK_DELETE_EMPTYDIR") == "true"
	if candidate := os.Getenv("FORGE_E2E_BINARY"); candidate != "" {
		if breakEmptyDirPolicy {
			t.Fatal("FORGE_E2E_BREAK_DELETE_EMPTYDIR cannot mutate an exact candidate Forge binary")
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			t.Fatalf("resolve FORGE_E2E_BINARY: %v", err)
		}
		if info, err := os.Stat(absolute); err != nil {
			t.Fatalf("candidate Forge binary unavailable: %v", err)
		} else if info.Mode()&0o111 == 0 {
			t.Fatalf("candidate Forge binary %s is not executable", absolute)
		}
		t.Logf("using exact candidate Forge binary %s", absolute)
		return absolute
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..")
	bin := filepath.Join(t.TempDir(), "forge")
	args := []string{"build"}
	if breakEmptyDirPolicy {
		sourcePath, err := filepath.Abs(filepath.Join(repoRoot, "internal", "lifecycle", "lifecycle.go"))
		if err != nil {
			t.Fatalf("resolve lifecycle source for mutation: %v", err)
		}
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read lifecycle source for mutation: %v", err)
		}
		const enabled = `"driver.upgradePolicy.gpuPodDeletion.deleteEmptyDir=true",`
		const disabled = `"driver.upgradePolicy.gpuPodDeletion.deleteEmptyDir=false",`
		if strings.Count(string(source), enabled) != 1 {
			t.Fatalf("expected one deleteEmptyDir policy value to mutate")
		}
		mutatedPath := filepath.Join(t.TempDir(), "lifecycle.go")
		if err := os.WriteFile(mutatedPath, []byte(strings.Replace(string(source), enabled, disabled, 1)), 0o600); err != nil {
			t.Fatalf("write mutated lifecycle source: %v", err)
		}
		overlayPath := filepath.Join(t.TempDir(), "overlay.json")
		overlay, err := json.Marshal(map[string]map[string]string{"Replace": {sourcePath: mutatedPath}})
		if err != nil {
			t.Fatalf("encode Go build overlay: %v", err)
		}
		if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
			t.Fatalf("write Go build overlay: %v", err)
		}
		args = append(args, "-overlay", overlayPath)
		t.Log("building intentional HOR-411 mutation with deleteEmptyDir=false")
	}
	args = append(args, "-o", bin, "./cmd/forge")
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build forge: %v\n%s", err, out)
	}
	return bin
}

func writeForgeConfig(t *testing.T, name, ip, keyPath, chartVersion string) string {
	return writeForgeConfigSpec(t, forgeConfigSpec{
		Name: name, Address: ip, SSHKeyPath: keyPath, RunLabel: true, DualStack: true,
		ChartVersion: chartVersion, OverlayRepo: "file:///tmp/edge-overlay", OverlayRef: "master",
	})
}

// runForgeE runs forge and returns its combined output and error (no t.Fatalf).
func runForgeE(bin, forgeHome string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "FORGE_HOME="+forgeHome)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// applyOnceArgs runs one required Forge apply. Required E2E gates fail on the
// first non-zero result; a later success must not mask a deterministic defect.
func applyOnceArgs(t *testing.T, bin, forgeHome, cfgPath string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{"apply", "--config", cfgPath}, extraArgs...)
	out, err := runForgeE(bin, forgeHome, args...)
	if err != nil {
		t.Fatalf("forge apply failed: %v\n%s", err, out)
	}
	return out
}

func applyOnce(t *testing.T, bin, forgeHome, cfgPath string) string {
	t.Helper()
	return applyOnceArgs(t, bin, forgeHome, cfgPath)
}

func checkNodeViaKubeconfig(t *testing.T, kcPath, wantLabelValue string) {
	t.Helper()
	restCfg, err := clientcmd.BuildConfigFromFlags("", kcPath)
	if err != nil {
		t.Fatalf("build kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("new clientset: %v", err)
	}

	// Poll briefly: the node and its pod CIDR assignment can lag "Ready" slightly.
	var node corev1.Node
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		nodes, lerr := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if lerr == nil && len(nodes.Items) == 1 {
			node = nodes.Items[0]
			if len(node.Spec.PodCIDRs) > 0 {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if node.Name == "" {
		t.Fatalf("no node found via kubeconfig")
	}

	ready := false
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Errorf("node %s is not Ready", node.Name)
	}
	if got := node.Labels["e2e.horizonshift.io/run"]; got != wantLabelValue {
		t.Errorf("node label e2e.horizonshift.io/run = %q, want %q", got, wantLabelValue)
	}
	// Dual-stack proof: the node must have an IPv6 pod CIDR.
	hasV6 := false
	for _, c := range node.Spec.PodCIDRs {
		ip := net.ParseIP(strings.SplitN(c, "/", 2)[0])
		if ip != nil && ip.To4() == nil {
			hasV6 = true
		}
	}
	if !hasV6 {
		t.Errorf("node has no IPv6 pod CIDR (dual-stack not active): %v", node.Spec.PodCIDRs)
	}
}

func checkGatewayRunning(t *testing.T, kcPath string) {
	t.Helper()
	restCfg, err := clientcmd.BuildConfigFromFlags("", kcPath)
	if err != nil {
		t.Fatalf("build kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("new clientset: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		pods, lerr := cs.CoreV1().Pods("iterabase-system").List(context.Background(), metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=inference-gateway",
		})
		if lerr == nil && len(pods.Items) > 0 && pods.Items[0].Status.Phase == corev1.PodRunning {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("inference-gateway pod not Running in iterabase-system")
}

func checkGatewayHealth(t *testing.T, ip string) {
	t.Helper()
	// The migration source proves the real MetalLB edge on 443.
	checkGatewayHealthOnPort(t, ip, 443)
}

func checkGatewayNodePortHealth(t *testing.T, kcPath, ip string) {
	t.Helper()
	restCfg, err := clientcmd.BuildConfigFromFlags("", kcPath)
	if err != nil {
		t.Fatalf("build kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("new clientset: %v", err)
	}
	services, err := cs.CoreV1().Services("iterabase-system").List(context.Background(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=ingress-nginx,app.kubernetes.io/component=controller",
	})
	if err != nil {
		t.Fatalf("resolve ingress controller Services: %v", err)
	}
	for _, service := range services.Items {
		for _, port := range service.Spec.Ports {
			if port.Port == 443 && port.NodePort > 0 {
				checkGatewayHealthOnPort(t, ip, int(port.NodePort))
				return
			}
		}
	}
	t.Fatalf("ingress controller Services have no HTTPS NodePort: %+v", services.Items)
}

func checkGatewayHealthOnPort(t *testing.T, ip string, port int) {
	t.Helper()
	// Reach the gateway over the real HTTPS ingress with the chart's default Host
	// and SNI. The E2E edge uses a self-signed issuer.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed e2e cert
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
		},
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}
	url := "https://gateway.iterabase.local/health"
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("gateway /health not 200 via %s (ip %s port %d)", url, ip, port)
}

// writeEdgeOverlayOnHost creates a file:// overlay git repo on the droplet with
// the MetalLB L2 edge values (IPAddressPool = the droplet's public IP). forge
// apply clones it (file://, tokenless) and feeds values.yaml to the platform
// chart. git is pre-installed by cloud-init. The scaffold matches what forge
// validates: values.yaml + values.client.yaml + crds/client/kustomization.yaml.
func writeEdgeOverlayOnHost(t *testing.T, ip, keyPath string) {
	t.Helper()
	sc, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", ip, err)
	}
	defer sc.Close()
	script := fmt.Sprintf(`set -e
# git is needed to init the overlay repo; install it if absent (mirrors forge's
# EnsureGit). cloud-init only installs curl, so git may not be present yet.
if ! command -v git >/dev/null 2>&1; then
  sudo apt-get update -qq && sudo apt-get install -y git
fi
d=/tmp/edge-overlay
rm -rf "$d"
mkdir -p "$d/crds/client"
cat > "$d/values.yaml" <<'YAML'
metallb:
  enabled: true
metallb-config:
  enabled: true
  addresses:
    - %s-%s
YAML
cat > "$d/values.client.yaml" <<'YAML'
# client-specific overrides (none for e2e)
YAML
cat > "$d/crds/client/kustomization.yaml" <<'YAML'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
YAML
cd "$d"
git init -q -b master
git add .
git -c user.email=forge@e2e -c user.name=forge commit -qm init
`, ip, ip)
	if out, err := sshOutput(sc, script); err != nil {
		t.Fatalf("write edge overlay on host: %v\n%s", err, out)
	}
}
