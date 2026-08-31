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
	"errors"
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
	region      = "fra1"
	size        = "s-2vcpu-4gb" // smallest published baseline fixture
	managedSize = "s-4vcpu-8gb" // full platform plus two concurrent workers needs independent headroom
	image       = "ubuntu-24-04-x64"
	k3sPort     = 6443
)

type cpuVMProvisioner interface {
	Create(context.Context, string, string) (*godo.Droplet, error)
	AttachWorkspace(context.Context, string, int) (string, error)
	PublicIP(context.Context, int) (string, error)
	Destroy(context.Context, int) error
}

type doCPUVMProvisioner struct {
	client          *godo.Client
	size            string
	workspaceVolume string
}

func (provisioner *doCPUVMProvisioner) Create(ctx context.Context, runID, pubKey string) (*godo.Droplet, error) {
	request := newCPUDropletRequest(runID, pubKey)
	if provisioner.size != "" {
		request.Size = provisioner.size
	}
	droplet, _, err := provisioner.client.Droplets.Create(ctx, request)
	return droplet, err
}

func (provisioner *doCPUVMProvisioner) AttachWorkspace(ctx context.Context, runID string, dropletID int) (string, error) {
	name := runID + "-workspaces"
	volume, _, err := provisioner.client.Storage.CreateVolume(ctx, newWorkspaceVolumeRequest(runID, name, region))
	if err != nil {
		return "", fmt.Errorf("create dedicated workspace volume: %w", err)
	}
	provisioner.workspaceVolume = volume.ID
	if _, _, err := provisioner.client.StorageActions.Attach(ctx, volume.ID, dropletID); err != nil {
		return "", fmt.Errorf("attach dedicated workspace volume: %w", err)
	}
	return "/dev/disk/by-id/scsi-0DO_Volume_" + name, nil
}

func newWorkspaceVolumeRequest(runID, name, targetRegion string) *godo.VolumeCreateRequest {
	return &godo.VolumeCreateRequest{
		Region: targetRegion, Name: name, Description: "HOR-538 dedicated AgentPool workspace filesystem", SizeGigaBytes: 25,
		Tags: []string{"forge-e2e", runID},
	}
}

func (provisioner *doCPUVMProvisioner) PublicIP(ctx context.Context, id int) (string, error) {
	return waitForIP(ctx, provisioner.client, id)
}

func (provisioner *doCPUVMProvisioner) Destroy(ctx context.Context, id int) error {
	_, dropletErr := provisioner.client.Droplets.Delete(ctx, id)
	return errors.Join(dropletErr, deleteWorkspaceVolume(ctx, provisioner.client, provisioner.workspaceVolume))
}

func deleteWorkspaceVolume(ctx context.Context, client *godo.Client, volumeID string) error {
	if volumeID == "" {
		return nil
	}
	deadline := time.Now().Add(2 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := client.Storage.DeleteVolume(ctx, volumeID); err == nil {
			return nil
		} else {
			lastErr = err
			if !strings.Contains(strings.ToLower(err.Error()), "attached volume") {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return errors.Join(lastErr, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("workspace volume %s remained attached after droplet deletion: %w", volumeID, lastErr)
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
	workspaceDevice     string
	freshInstall        bool
	storagePVCUID       string
	storagePV           string
	agentPoolPVCUID     string
	initialWorkerPodUID string
	workspaceAdminKey   string
	workspaceWorkKey    string
	diagnostics         forgeDiagnostics
}

func newDigitalOceanCPUState(t *testing.T) *digitalOceanCPUState {
	return newDigitalOceanCPUStateForScenario(t, "digitalocean-cpu")
}

func newDigitalOceanWorkspaceState(t *testing.T) *digitalOceanCPUState {
	t.Setenv(workspaceBehaviorEnv, "true")
	state := newDigitalOceanCPUStateForScenario(t, "digitalocean-workspace")
	state.freshInstall = true
	return state
}

func newDigitalOceanCPUStateForScenario(t *testing.T, scenario string) *digitalOceanCPUState {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		if os.Getenv("FORGE_E2E_REQUIRE_CAPACITY") == "true" {
			t.Fatal("mandatory CPU release validation incomplete — DIGITALOCEAN_TOKEN not set")
		}
		t.Skip("DIGITALOCEAN_TOKEN not set; skipping DigitalOcean CPU scenario")
	}

	state := &digitalOceanCPUState{
		ctx:         context.Background(),
		provisioner: &doCPUVMProvisioner{client: godo.NewFromToken(token), size: managedSize},
		ready:       waitForHostReady,
		runID:       fmt.Sprintf("forge-e2e-%d", time.Now().Unix()),
		keep:        os.Getenv("FORGE_E2E_KEEP") != "",
		forgeHome:   t.TempDir(),
		diagnostics: newForgeDiagnostics(t, scenario),
	}
	if githubToken := os.Getenv("GITHUB_TOKEN"); githubToken != "" {
		state.diagnostics.redactor.Add(githubToken)
	}
	state.pubKey, state.privKeyPath = generateKey(t)
	state.forgeBin = buildForge(t)
	state.chartVersion = platformChartVersion(t, "")
	t.Logf("run %s (keep=%v)", state.runID, state.keep)
	return state
}

func provisionCPUStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if err := provisionCPUHost(state); err != nil {
		t.Fatal(err)
	}
	t.Logf("droplet ip %s workspace=%s", state.ip, state.workspaceDevice)
}

func provisionCPUHost(state *digitalOceanCPUState) error {
	droplet, err := state.provisioner.Create(state.ctx, state.runID, state.pubKey)
	if err != nil {
		return fmt.Errorf("create droplet: %w", err)
	}
	// Retain the resource identity immediately so shared scenario cleanup owns
	// every failure after the cloud API accepts creation.
	state.droplet = droplet
	workspaceDevice, err := state.provisioner.AttachWorkspace(state.ctx, state.runID, droplet.ID)
	if err != nil {
		return err
	}
	state.workspaceDevice = workspaceDevice
	ip, err := state.provisioner.PublicIP(state.ctx, droplet.ID)
	if err != nil {
		return fmt.Errorf("wait for droplet IP: %w", err)
	}
	state.ip = ip
	if err := state.ready(state.ctx, ip, state.privKeyPath); err != nil {
		return fmt.Errorf("wait for host readiness: %w", err)
	}
	if err := waitForWorkspaceDevice(state.ctx, ip, state.privKeyPath, workspaceDevice); err != nil {
		return err
	}
	rememberWorkspaceDevice(ip, workspaceDevice)
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
	if _, err := sshOutput(sc, "sudo k3s kubectl get namespace longhorn-system"); err == nil {
		t.Fatal("obsolete Longhorn namespace exists in the dedicated local-path release")
	}
	class := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get storageclass iterabase-agentpool-local-path -o jsonpath='{.provisioner}|{.reclaimPolicy}|{.volumeBindingMode}|{.allowVolumeExpansion}|{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}'`))
	if class != "rancher.io/local-path|Delete|WaitForFirstConsumer|false|false" {
		t.Fatalf("AgentPool local-path StorageClass contract = %q", class)
	}
	defaultClass := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get storageclass local-path -o jsonpath='{.provisioner}|{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}'`))
	if defaultClass != "rancher.io/local-path|true" {
		t.Fatalf("default local-path class drifted: %q", defaultClass)
	}
	config := mustSSHOutput(t, sc, `sudo k3s kubectl get configmap local-path-config -n kube-system -o jsonpath='{.data.config\.json}'`)
	for _, path := range []string{"/var/lib/rancher/k3s/storage", "/var/lib/iterabase/agentpool-workspaces", "iterabase-agentpool-local-path"} {
		if !strings.Contains(config, path) {
			t.Fatalf("local-path class-isolation config is missing %q: %s", path, config)
		}
	}
	mount := strings.TrimSpace(mustSSHOutput(t, sc, `sudo bash -ceu '
source=$(findmnt -n -o SOURCE --target /var/lib/iterabase/agentpool-workspaces)
source=${source%[*}
device=$(readlink -f -- "$source")
transport=$(lsblk -dnro TRAN -- "$device" | tr "[:upper:]" "[:lower:]" | xargs)
printf "%s|%s|%s|%s\n" "${transport:-unknown}" "$(findmnt -n -o FSTYPE --target /var/lib/iterabase/agentpool-workspaces)" "$(blkid -p -s LABEL -o value -- "$device")" "$(findmnt -n -o OPTIONS --target /var/lib/iterabase/agentpool-workspaces)"
'`))
	parts := strings.SplitN(mount, "|", 4)
	if len(parts) != 4 {
		t.Fatalf("dedicated workspace mount evidence is malformed: %q", mount)
	}
	wantFilesystem := "ext4"
	if parts[0] == "nvme" {
		wantFilesystem = "xfs"
	}
	if parts[1] != wantFilesystem || parts[2] != "iterabase-ws" || !strings.Contains(parts[3], "nodev") || !strings.Contains(parts[3], "nosuid") {
		t.Fatalf("dedicated workspace mount contract transport/type/label/options = %q", mount)
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

	kcPath := filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml")
	checkGatewayRunning(t, kcPath)
	checkGatewayNodePortHealth(t, kcPath, state.ip)
}

func seedLocalPathReapplyStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	manifest := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: forge-local-path-reapply
  namespace: iterabase-system
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: iterabase-agentpool-local-path
  resources:
    requests: {storage: 1Gi}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: forge-local-path-writer
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
          args: ['printf HOR-538-reapply > /sessions/marker && sync -f /sessions/marker']
          volumeMounts: [{name: sessions, mountPath: /sessions}]
      volumes:
        - name: sessions
          persistentVolumeClaim: {claimName: forge-local-path-reapply}
YAML`
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/forge-local-path-writer --timeout=10m")
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-local-path-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	parts := strings.Split(identity, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("local-path seed claim has incomplete identity: %q", identity)
	}
	state.storagePVCUID, state.storagePV = parts[0], parts[1]
	path := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo k3s kubectl get pv %s -o jsonpath='{.spec.hostPath.path}'`, state.storagePV)))
	if !strings.HasPrefix(path, "/var/lib/iterabase/agentpool-workspaces/") {
		t.Fatalf("dedicated-class PV escaped workspace mount: %q", path)
	}
}

func assertLocalPathReapplyStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-local-path-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	if identity != state.storagePVCUID+"|"+state.storagePV {
		t.Fatalf("Forge reapply replaced the local-path claim: before=%s|%s after=%s", state.storagePVCUID, state.storagePV, identity)
	}
	manifest := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: forge-local-path-replacement
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
          args: ['test "$(cat /sessions/marker)" = HOR-538-reapply && printf replacement-persistence=pass']
          volumeMounts: [{name: sessions, mountPath: /sessions}]
      volumes:
        - name: sessions
          persistentVolumeClaim: {claimName: forge-local-path-reapply}
YAML`
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/forge-local-path-replacement --timeout=10m")
	logs := mustSSHOutput(t, sc, "sudo k3s kubectl logs -n iterabase-system job/forge-local-path-replacement")
	if !strings.Contains(logs, "replacement-persistence=pass") {
		t.Fatalf("replacement pod did not preserve committed local-path bytes: %s", logs)
	}
}

func setupLocalPathAgentPoolStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	repository, tag := os.Getenv("HARNESS_IMAGE_REPO"), os.Getenv("HARNESS_IMAGE_TAG")
	if repository == "" || tag == "" {
		t.Log("exact harness image is unavailable in the baseline CPU fixture; dedicated workspace scenario owns AgentPool runtime evidence")
		return
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
    storageClassName: iterabase-agentpool-local-path
    accessMode: ReadWriteOnce
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
  workspaceTools: true
YAML`, repository, tag, state.runID, state.runID, state.runID, state.runID, state.runID, state.runID, state.runID)
	mustSSHOutput(t, sc, manifest)
	waitForLocalPathAgentPoolReady(t, sc, 10*time.Minute)
	status := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}'`))
	if status != "2|StorageReady" {
		t.Fatalf("local-path AgentPool readiness = %q, want 2|StorageReady", status)
	}
	state.agentPoolPVCUID = strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	state.initialWorkerPodUID = strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'`))
	if state.agentPoolPVCUID == "" || len(strings.Fields(state.initialWorkerPodUID)) != 2 {
		t.Fatalf("local-path AgentPool identities incomplete: pvc=%q workers=%q", state.agentPoolPVCUID, state.initialWorkerPodUID)
	}
}

func waitForLocalPathAgentPoolReady(t *testing.T, client *ssh.Client, timeout time.Duration) {
	t.Helper()
	command := fmt.Sprintf("sudo k3s kubectl wait -n iterabase-system --for=jsonpath='{.status.readyReplicas}'=2 agentpool/forge-storage-pool --timeout=%s", timeout)
	output, err := sshOutput(client, command)
	if err == nil {
		return
	}
	diagnostics, diagnosticsErr := sshOutput(client, `{
  sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o yaml || true
  sudo k3s kubectl get pods,pvc -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o wide || true
  pv=$(sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
  test -z "$pv" || sudo k3s kubectl get pv "$pv" -o yaml || true
  sudo k3s kubectl get events -n iterabase-system --sort-by=.metadata.creationTimestamp | tail -100 || true
} 2>&1`)
	if diagnosticsErr != nil {
		diagnostics += "\ncollect AgentPool timeout diagnostics: " + diagnosticsErr.Error()
	}
	t.Fatalf("local-path AgentPool did not become Ready within %s: %v\n%s\n%s", timeout, err, output, diagnostics)
}

func exerciseWorkspaceCapacityGateStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if os.Getenv("HARNESS_IMAGE_REPO") == "" {
		return
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	const filler = "/var/lib/iterabase/agentpool-workspaces/.forge-e2e-capacity-fill"
	mustSSHOutput(t, sc, `sudo bash -ceu '
mount=/var/lib/iterabase/agentpool-workspaces
read -r size avail < <(df -B1 --output=size,avail "$mount" | tail -1)
target=$((size * 19 / 100))
allocate=$((avail - target))
test "$allocate" -gt 0
fallocate -l "$allocate" `+filler+`
sync -f `+filler+`
'`)
	t.Cleanup(func() {
		if cleanup, dialErr := sshDial(state.ip, state.privKeyPath); dialErr == nil {
			_, _ = sshOutput(cleanup, "sudo rm -f "+filler+" && sync")
			cleanup.Close()
		}
	})
	waitMetrics := func(want string) {
		t.Helper()
		command := fmt.Sprintf(`for i in $(seq 1 60); do
  ok=0; total=0
  for pod in $(sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o name | cut -d/ -f2); do
    total=$((total+1))
    metrics=$(sudo k3s kubectl get --raw "/api/v1/namespaces/iterabase-system/pods/$pod:8081/proxy/metrics" 2>/dev/null || true)
    if printf '%%s\n' "$metrics" | grep -Eq '^control_plane_harness_workspace_credit_gated %s(\\.0+)?$'; then ok=$((ok+1)); fi
  done
  test "$total" -ge 2 && test "$ok" = "$total" && exit 0
  sleep 2
done
exit 1`, want)
		if output, commandErr := sshOutput(sc, command); commandErr != nil {
			t.Fatalf("workspace capacity gate did not reach %s on every worker: %v\n%s", want, commandErr, output)
		}
	}
	waitMetrics("1")
	metrics := mustSSHOutput(t, sc, `pod=$(sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{.items[0].metadata.name}'); sudo k3s kubectl get --raw "/api/v1/namespaces/iterabase-system/pods/$pod:8081/proxy/metrics"`)
	if !strings.Contains(metrics, "control_plane_harness_workspace_capacity_warning 1") {
		t.Fatalf("controlled fill gated credit without the 25%% warning metric:\n%s", metrics)
	}
	mustSSHOutput(t, sc, "sudo rm -f "+filler+" && sync")
	waitMetrics("0")
}

func replaceWorkspaceWorkerStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	if os.Getenv("HARNESS_IMAGE_REPO") == "" {
		return
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	pod := strings.Fields(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o name`))[0]
	mustSSHOutput(t, sc, "sudo k3s kubectl delete -n iterabase-system "+pod+" --wait=true --timeout=5m")
	waitForLocalPathAgentPoolReady(t, sc, 10*time.Minute)
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	if identity != state.agentPoolPVCUID {
		t.Fatalf("worker replacement changed AgentPool PVC: before=%s after=%s", state.agentPoolPVCUID, identity)
	}
	workers := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'`))
	if len(strings.Fields(workers)) != 2 || workers == state.initialWorkerPodUID {
		t.Fatalf("worker replacement did not produce a fresh two-worker set: before=%q after=%q", state.initialWorkerPodUID, workers)
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
	markers := []string{"action:     skip", "node ready: true", "AgentPool workspace:", "AgentPool local-path ready: true", "certificate substrate applied: true", "chart applied: true", "overlay applied: true"}
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
	workspaceDevicesByAddress.Delete(state.ip)
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

func waitForWorkspaceDevice(ctx context.Context, ip, keyPath, device string) error {
	end := time.Now().Add(2 * time.Minute)
	for time.Now().Before(end) {
		client, err := sshDial(ip, keyPath)
		if err == nil {
			_, probeErr := sshOutput(client, "test -L "+candidateShellQuote(device)+" && test -b \"$(readlink -f "+candidateShellQuote(device)+")\"")
			client.Close()
			if probeErr == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("dedicated workspace device %s did not appear on %s", device, ip)
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
	// Required cloud E2E uses the workflow's ephemeral GitHub token for the
	// public exact-overlay clone when no explicit Forge token was supplied. This
	// avoids anonymous smart-HTTP edge failures from short-lived cloud IPs while
	// exercising Forge's credential-helper path without persisting the token.
	if os.Getenv("FORGE_OVERLAY_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") != "" {
		cmd.Env = append(cmd.Env, "FORGE_OVERLAY_TOKEN="+os.Getenv("GITHUB_TOKEN"))
	}
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
