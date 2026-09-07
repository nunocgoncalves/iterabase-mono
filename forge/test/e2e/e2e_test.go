// Package e2e runs Forge's composed scenarios against fixed, pinned permanent
// fixtures. The separate module keeps client-go and harness-only dependencies
// out of Forge's production module.
package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	k3sPort                           = 6443
	permanentCPUScenarioName          = "permanent-fixture-cpu"
	permanentCPUWorkspaceScenarioName = "permanent-fixture-cpu-workspace"
	permanentGPUScenarioName          = "permanent-fixture-gpu"
)

type permanentCPUFixtureState struct {
	fixture             *permanentFixture
	runID               string
	privKeyPath         string
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
	runtimeImageDigests map[string]importedRuntimeIdentity
	diagnostics         forgeDiagnostics
}

func newPermanentCPUFixtureState(t *testing.T) *permanentCPUFixtureState {
	return newPermanentCPUFixtureStateForScenario(t, permanentCPUScenarioName)
}

func newPermanentCPUWorkspaceFixtureState(t *testing.T) *permanentCPUFixtureState {
	t.Setenv(workspaceBehaviorEnv, "true")
	state := newPermanentCPUFixtureStateForScenario(t, permanentCPUWorkspaceScenarioName)
	state.freshInstall = true
	return state
}

func newPermanentCPUFixtureStateForScenario(t *testing.T, scenario string) *permanentCPUFixtureState {
	fixture := requirePermanentFixture(t, "cpu")
	state := &permanentCPUFixtureState{
		fixture:             fixture,
		runID:               fixture.installName(),
		privKeyPath:         fixture.sshKeyPath,
		ip:                  fixture.address,
		forgeHome:           t.TempDir(),
		workspaceDevice:     fixture.workspaceDevice,
		runtimeImageDigests: make(map[string]importedRuntimeIdentity),
		diagnostics:         newForgeDiagnostics(t, scenario),
	}
	if githubToken := os.Getenv("GITHUB_TOKEN"); githubToken != "" {
		state.diagnostics.redactor.Add(githubToken)
	}
	state.forgeBin = buildForge(t)
	state.chartVersion = platformChartVersion(t, "")
	t.Logf("run %s on the permanent CPU fixture", state.runID)
	return state
}

func resetPermanentCPUFixtureStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	if err := state.fixture.reset(t, state.forgeBin, state.forgeHome); err != nil {
		t.Fatal(err)
	}
	rememberWorkspaceDevice(state.ip, state.workspaceDevice)
	t.Logf("permanent CPU fixture %s workspace=%s", state.ip, state.workspaceDevice)
}

func rejectGPUOnCPUStage(t *testing.T, state *permanentCPUFixtureState) {
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

// assertCurrentPlatformStage proves Forge handed the exact desired releases and
// source artifact to a minimally healthy dependent layer. Chart ownership,
// rollout, certificate, gateway, and tool-runner correctness remains in the
// chart/control-plane owner suites.
func assertCurrentPlatformStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()

	assertRemoteHelmChartVersion(t, sc, state.runID, "iterabase-system", state.chartVersion)
	assertRemoteHelmChartVersion(t, sc, state.runID+"-cert-manager", "iterabase-system", state.chartVersion)
	assertRemoteHelmChartVersion(t, sc, state.runID+"-lvm-storage", "iterabase-system", state.chartVersion)
	for _, obsolete := range []string{"longhorn-system", "local-path-storage"} {
		if _, err := sshOutput(sc, "sudo k3s kubectl get namespace "+obsolete); err == nil {
			t.Fatalf("obsolete storage namespace %s exists in the OpenEBS LVM release", obsolete)
		}
	}
	if _, err := sshOutput(sc, "sudo k3s kubectl get deployment/local-path-provisioner -n kube-system"); err == nil {
		t.Fatal("K3s local-path provisioner exists despite local-storage disablement")
	}
	classes := strings.Fields(mustSSHOutput(t, sc, `sudo k3s kubectl get storageclass -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'`))
	if len(classes) != 2 || !slices.Contains(classes, "iterabase-lvm-xfs") || !slices.Contains(classes, "iterabase-agentpool-lvm-xfs") {
		t.Fatalf("managed StorageClass set = %v, want exactly the two OpenEBS LVM classes", classes)
	}
	for class, shared := range map[string]string{"iterabase-lvm-xfs": "no", "iterabase-agentpool-lvm-xfs": "yes"} {
		observed := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo k3s kubectl get storageclass %s -o jsonpath='{.provisioner}|{.reclaimPolicy}|{.volumeBindingMode}|{.allowVolumeExpansion}|{.parameters.storage}|{.parameters.vgpattern}|{.parameters.fsType}|{.parameters.thinProvision}|{.parameters.shared}|{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}'`, class)))
		want := "local.csi.openebs.io|Delete|WaitForFirstConsumer|false|lvm|^iterabase-data$|xfs|no|" + shared + "|false"
		if observed != want {
			t.Fatalf("StorageClass %s contract = %q, want %q", class, observed, want)
		}
	}
	lvm := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo bash -ceu '
receipt=/var/lib/iterabase/data-storage.receipt
test -f "$receipt" && test ! -L "$receipt" && test "$(stat -c "%%u:%%g:%%a" "$receipt")" = 0:0:600
selected=%s
device=$(readlink -f -- "$selected")
pv=$(pvs --noheadings --separator "|" -o pv_uuid,vg_name -- "$device" | awk -F"|" "{\$1=\$1;\$2=\$2;print \$1 \"|\" \$2}")
vg=$(vgs --noheadings --separator "|" --units b --nosuffix -o vg_uuid,vg_size,vg_free,pv_count,lv_count iterabase-data | awk -F"|" "{for(i=1;i<=NF;i++){gsub(/^ +| +$/,\"\",\$i)}; print}")
printf "%%s|%%s\n" "$pv" "$vg"
'`, candidateShellQuote(state.workspaceDevice))))
	parts := strings.Split(lvm, "|")
	if len(parts) != 7 || parts[0] == "" || parts[1] != "iterabase-data" || parts[2] == "" || parts[5] != "1" {
		t.Fatalf("receipt-bound PV/VG evidence is malformed: %q", lvm)
	}
	lvmNode := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get lvmnodes.local.openebs.io -n iterabase-system -o jsonpath='{range .items[*].volumeGroups[?(@.name=="iterabase-data")]}{.name}|{.uuid}|{.pvCount}|{.missingPvCount}|{.thinPools}{"\n"}{end}'`))
	if !strings.HasPrefix(lvmNode, "iterabase-data|"+parts[2]+"|1|0|") {
		t.Fatalf("OpenEBS LVMNode does not match receipt VG: %q receipt=%q", lvmNode, lvm)
	}

	pvcClasses := strings.Fields(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}={.spec.storageClassName}{"\n"}{end}'`))
	for _, claim := range pvcClasses {
		if !strings.HasSuffix(claim, "=iterabase-lvm-xfs") {
			t.Fatalf("chart-owned data claim did not use the general LVM class: %s", claim)
		}
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

func seedLVMReapplyStage(t *testing.T, state *permanentCPUFixtureState) {
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
  name: forge-lvm-reapply
  namespace: iterabase-system
spec:
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  storageClassName: iterabase-lvm-xfs
  resources:
    requests: {storage: 1Gi}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: forge-lvm-writer
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
          args: ['printf HOR-545-reapply > /sessions/marker && sync -f /sessions/marker']
          volumeMounts: [{name: sessions, mountPath: /sessions}]
      volumes:
        - name: sessions
          persistentVolumeClaim: {claimName: forge-lvm-reapply}
YAML`
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/forge-lvm-writer --timeout=10m")
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-lvm-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	parts := strings.Split(identity, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("LVM seed claim has incomplete identity: %q", identity)
	}
	state.storagePVCUID, state.storagePV = parts[0], parts[1]
	volume := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo k3s kubectl get pv %s -o jsonpath='{.spec.csi.driver}|{.spec.csi.fsType}|{.spec.csi.volumeAttributes.openebs\.io/volgroup}|{.spec.csi.volumeHandle}'`, state.storagePV)))
	volumeParts := strings.Split(volume, "|")
	if len(volumeParts) != 4 || volumeParts[0] != "local.csi.openebs.io" || volumeParts[1] != "xfs" || volumeParts[2] != "iterabase-data" || volumeParts[3] == "" {
		t.Fatalf("general LVM PV contract is invalid: %q", volume)
	}
	lvmVolume := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo k3s kubectl get lvmvolume.local.openebs.io %s -n iterabase-system -o jsonpath='{.status.state}|{.spec.volGroup}|{.spec.vgPattern}|{.spec.thinProvision}|{.spec.shared}'`, volumeParts[3])))
	if lvmVolume != "Ready|iterabase-data|^iterabase-data$|no|no" {
		t.Fatalf("general LVMVolume contract is invalid: %q", lvmVolume)
	}
}

func assertLVMReapplyStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-lvm-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	if identity != state.storagePVCUID+"|"+state.storagePV {
		t.Fatalf("Forge reapply replaced the LVM claim: before=%s|%s after=%s", state.storagePVCUID, state.storagePV, identity)
	}
	manifest := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: forge-lvm-replacement
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
          args: ['test "$(cat /sessions/marker)" = HOR-545-reapply && printf replacement-persistence=pass']
          volumeMounts: [{name: sessions, mountPath: /sessions}]
      volumes:
        - name: sessions
          persistentVolumeClaim: {claimName: forge-lvm-reapply}
YAML`
	mustSSHOutput(t, sc, manifest)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/forge-lvm-replacement --timeout=10m")
	logs := mustSSHOutput(t, sc, "sudo k3s kubectl logs -n iterabase-system job/forge-lvm-replacement")
	if !strings.Contains(logs, "replacement-persistence=pass") {
		t.Fatalf("replacement pod did not preserve committed LVM/XFS bytes: %s", logs)
	}
}

func deleteLVMClaimStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	handle := strings.TrimSpace(mustSSHOutput(t, sc, fmt.Sprintf(`sudo k3s kubectl get pv %s -o jsonpath='{.spec.csi.volumeHandle}'`, state.storagePV)))
	if handle == "" {
		t.Fatal("general LVM claim has no volume handle before deletion")
	}
	mustSSHOutput(t, sc, "sudo k3s kubectl delete job/forge-lvm-writer job/forge-lvm-replacement -n iterabase-system --ignore-not-found=true --wait=true --timeout=5m")
	mustSSHOutput(t, sc, "sudo k3s kubectl delete pvc/forge-lvm-reapply -n iterabase-system --wait=true --timeout=5m")
	command := fmt.Sprintf(`for i in $(seq 1 150); do
  ! sudo k3s kubectl get pv %s >/dev/null 2>&1 &&
  ! sudo k3s kubectl get lvmvolume.local.openebs.io %s -n iterabase-system >/dev/null 2>&1 &&
  ! sudo lvs --noheadings -o lv_name iterabase-data | awk '{$1=$1;if(NF)print}' | grep -Fxq %s && exit 0
  sleep 2
done
exit 1`, state.storagePV, handle, candidateShellQuote(handle))
	if output, err := sshOutput(sc, command); err != nil {
		t.Fatalf("Delete reclaim left PV/LVMVolume/LV %s: %v\n%s", handle, err, output)
	}
}

func destroyPreservesDataStorageStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	plan := prepareCandidateOverlay(t, state.runID, state.ip, state.privKeyPath)
	cfgPath := writeCurrentOverlayForgeConfig(t, state.runID, state.ip, state.privKeyPath, state.chartVersion, plan)
	out, err := runForgeE(state.forgeBin, state.forgeHome, "destroy", "--config", cfgPath, "--yes")
	if err != nil {
		t.Fatalf("ordinary Forge destroy failed: %v\n%s", err, out)
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh after ordinary destroy: %v", err)
	}
	defer sc.Close()
	preserved := mustSSHOutput(t, sc, fmt.Sprintf(`sudo bash -ceu '
test ! -e /etc/rancher/k3s/k3s.yaml
test -f /var/lib/iterabase/data-storage.receipt
test "$(vgs --noheadings -o vg_name iterabase-data | awk "{\$1=\$1;print}")" = iterabase-data
device=$(readlink -f -- %s)
test "$(pvs --noheadings -o vg_name "$device" | awk "{\$1=\$1;print}")" = iterabase-data
printf ordinary-destroy-vg-preserved=pass
'`, candidateShellQuote(state.workspaceDevice)))
	if !strings.Contains(preserved, "ordinary-destroy-vg-preserved=pass") {
		t.Fatalf("ordinary destroy did not preserve receipt-matching VG: %s", preserved)
	}
}

func setupLVMSharedAgentPoolStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	repository, tag := os.Getenv("HARNESS_IMAGE_REPO"), os.Getenv("HARNESS_IMAGE_TAG")
	if repository == "" || tag == "" {
		t.Fatal("composed CPU runtime requires the exact harness image")
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
    storageClassName: iterabase-agentpool-lvm-xfs
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
	waitForLVMSharedAgentPoolReady(t, sc, 10*time.Minute)
	status := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}'`))
	if status != "2|StorageReady" {
		t.Fatalf("OpenEBS shared-LVM AgentPool readiness = %q, want 2|StorageReady", status)
	}
	state.agentPoolPVCUID = strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	state.initialWorkerPodUID = strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'`))
	if state.agentPoolPVCUID == "" || len(strings.Fields(state.initialWorkerPodUID)) != 2 {
		t.Fatalf("OpenEBS shared-LVM AgentPool identities incomplete: pvc=%q workers=%q", state.agentPoolPVCUID, state.initialWorkerPodUID)
	}
	second := strings.ReplaceAll(manifest, "forge-storage-pool", "forge-storage-pool-b")
	second = strings.Replace(second, "replicas: 2", "replicas: 1", 1)
	mustSSHOutput(t, sc, second)
	mustSSHOutput(t, sc, "sudo k3s kubectl wait -n iterabase-system --for=jsonpath='{.status.readyReplicas}'=1 agentpool/forge-storage-pool-b --timeout=10m")
	claims := strings.Fields(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox forge-storage-pool-b-sandbox -n iterabase-system -o jsonpath='{range .items[*]}{.metadata.uid}|{.spec.volumeName}{"\n"}{end}'`))
	if len(claims) != 2 || claims[0] == claims[1] {
		t.Fatalf("separate AgentPools did not receive separate PVC/PV identities: %v", claims)
	}
}

func waitForLVMSharedAgentPoolReady(t *testing.T, client *ssh.Client, timeout time.Duration) {
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
	t.Fatalf("OpenEBS shared-LVM AgentPool did not become Ready within %s: %v\n%s\n%s", timeout, err, output, diagnostics)
}

func exerciseWorkspaceCapacityGateStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	if os.Getenv("HARNESS_IMAGE_REPO") == "" {
		t.Fatal("workspace capacity stage requires the composed harness image")
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	const filler = "/data/sandboxes/.forge-e2e-capacity-fill"
	pod := strings.Fields(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o name`))[0]
	fill := fmt.Sprintf(`sudo k3s kubectl exec -n iterabase-system %s -- bash -ceu '
mount=/data/sandboxes
read -r size avail < <(df -B1 --output=size,avail "$mount" | tail -1)
target=$((size * 19 / 100))
allocate=$((avail - target))
test "$allocate" -gt 0
fallocate -l "$allocate" %s
sync -f %s
'`, pod, filler, filler)
	mustSSHOutput(t, sc, fill)
	t.Cleanup(func() {
		if cleanup, dialErr := sshDial(state.ip, state.privKeyPath); dialErr == nil {
			_, _ = sshOutput(cleanup, fmt.Sprintf("sudo k3s kubectl exec -n iterabase-system %s -- rm -f %s", pod, filler))
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
	mustSSHOutput(t, sc, fmt.Sprintf("sudo k3s kubectl exec -n iterabase-system %s -- bash -ceu 'rm -f %s && sync'", pod, filler))
	waitMetrics("0")
}

func replaceWorkspaceWorkerStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	if os.Getenv("HARNESS_IMAGE_REPO") == "" {
		t.Fatal("worker replacement stage requires the composed harness image")
	}
	sc, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer sc.Close()
	pod := strings.Fields(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o name`))[0]
	mustSSHOutput(t, sc, "sudo k3s kubectl delete -n iterabase-system "+pod+" --wait=true --timeout=5m")
	waitForLVMSharedAgentPoolReady(t, sc, 10*time.Minute)
	identity := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pvc forge-storage-pool-sandbox -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	if identity != state.agentPoolPVCUID {
		t.Fatalf("worker replacement changed AgentPool PVC: before=%s after=%s", state.agentPoolPVCUID, identity)
	}
	workers := strings.TrimSpace(mustSSHOutput(t, sc, `sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'`))
	if len(strings.Fields(workers)) != 2 || workers == state.initialWorkerPodUID {
		t.Fatalf("worker replacement did not produce a fresh two-worker set: before=%q after=%q", state.initialWorkerPodUID, workers)
	}
}

func rebootPreservesLVMStorageStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	before, err := state.fixture.bootID()
	if err != nil {
		t.Fatalf("read boot ID before storage reboot: %v", err)
	}
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh before storage reboot: %v", err)
	}
	_, _ = sshOutput(client, "sudo systemctl reboot")
	client.Close()
	after, readyClient, err := state.fixture.waitForReboot(before)
	if err != nil {
		t.Fatalf("wait for storage reboot: %v", err)
	}
	readyClient.Close()
	client, err = waitForHostReady(context.Background(), state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("wait for host readiness after storage reboot: %v", err)
	}
	defer client.Close()
	command := fmt.Sprintf(`for i in $(seq 1 150); do
  test -f /var/lib/iterabase/data-storage.receipt &&
  test "$(vgs --noheadings -o vg_name iterabase-data | awk '{$1=$1;print}')" = iterabase-data &&
  test "$(k3s kubectl get --raw=/readyz 2>/dev/null)" = ok &&
  test "$(k3s kubectl get pvc forge-lvm-reapply -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}' 2>/dev/null)" = %s && exit 0
  sleep 2
done
exit 1`, candidateShellQuote(state.storagePVCUID+"|"+state.storagePV))
	if output, err := sshOutput(client, "sudo bash -ceu "+candidateShellQuote(command)); err != nil {
		t.Fatalf("reboot did not preserve receipt/VG/node/PVC/PV identity: %v\n%s", err, output)
	}
	t.Logf("storage reboot preserved identity: boot %s -> %s", before, after)
}

func reapplyCurrentPlatformStage(t *testing.T, state *permanentCPUFixtureState) {
	t.Helper()
	prepareCandidateChart(t, state.ip, state.privKeyPath)
	plan := prepareCandidateOverlay(t, state.runID, state.ip, state.privKeyPath)
	cfgPath := writeCurrentOverlayForgeConfig(
		t, state.runID, state.ip, state.privKeyPath, state.chartVersion, plan,
	)
	out := applyOnce(t, state.forgeBin, state.forgeHome, cfgPath)
	markers := []string{"action:     skip", "node ready: true", "data storage: iterabase-data", "LVM storage ready: true", "certificate substrate applied: true", "LVM storage substrate applied: true", "chart applied: true", "overlay applied: true"}
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

func (state *permanentCPUFixtureState) resetAfterScenario(t *testing.T) {
	t.Helper()
	state.diagnostics.setDomain(failureDomainFixtureReset)
	workspaceDevicesByAddress.Delete(state.ip)
	if err := state.fixture.reset(t, state.forgeBin, state.forgeHome); err != nil {
		t.Errorf("reset permanent CPU fixture after diagnostics: %v", err)
	}
}

// waitForHostReady waits until the permanent fixture accepts SSH AND cloud-init
// has finished applying its baseline (the forge user, passwordless sudo, curl).
// Returning only once cloud-init reports "done" prevents forge's preflight from
// racing cloud-init — e.g. `sudo -n true` failing with "passwordless sudo
// required" before the sudoers rule is applied, or curl being absent before
// `packages: [curl]` completes. The forge user is created by cloud-init, so SSH
// can only succeed once cloud-init has at least started.
func waitForHostReady(ctx context.Context, ip, keyPath string) (*ssh.Client, error) {
	const deadline = 5 * time.Minute
	end := time.Now().Add(deadline)
	var lastStatus string
	var lastErr error
	for time.Now().Before(end) {
		client, err := sshDial(ip, keyPath)
		if err == nil {
			out, statusErr := sshOutput(client, "cloud-init status")
			lastStatus, lastErr = strings.TrimSpace(out), statusErr
			switch {
			case strings.Contains(out, "status: done"):
				return client, nil
			case strings.Contains(out, "status: error"):
				client.Close()
				return nil, fmt.Errorf("cloud-init failed on %s: %s", ip, lastStatus)
			default:
				client.Close()
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return nil, fmt.Errorf("host %s never became ready (SSH up + cloud-init done) within %s (last status %q, last error %v)", ip, deadline, lastStatus, lastErr)
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
	hostKeyCallback := ssh.InsecureIgnoreHostKey() //nolint:gosec // legacy non-fixture E2E paths only
	var hostKeyAlgorithms []string
	if pin := strings.TrimSpace(os.Getenv(permanentFixtureHostKeyEnv)); pin != "" {
		publicKey, _, _, rest, parseErr := ssh.ParseAuthorizedKey([]byte(pin + "\n"))
		if parseErr != nil || len(strings.TrimSpace(string(rest))) != 0 {
			return nil, fmt.Errorf("parse pinned fixture SSH host key: %w", parseErr)
		}
		hostKeyCallback = ssh.FixedHostKey(publicKey)
		hostKeyAlgorithms = []string{publicKey.Type()}
	}
	cfg := &ssh.ClientConfig{
		User:              fixtureSSHUser(),
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           10 * time.Second,
	}
	return ssh.Dial("tcp", ip+":22", cfg)
}

func buildForge(t *testing.T) string {
	t.Helper()
	if candidate := os.Getenv("FORGE_E2E_BINARY"); candidate != "" {
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
	if os.Getenv("ITERABASE_E2E_REQUIRED") == "true" {
		t.Fatal("required real-machine execution has no composer-supplied Forge binary")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..")
	bin := filepath.Join(t.TempDir(), "forge")
	args := []string{"build", "-o", bin, "./cmd/forge"}
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

// writeEdgeOverlayOnHost creates a file:// overlay git repo on the fixture host
// with the MetalLB L2 edge values (IPAddressPool = the fixture's public IP). Forge
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
