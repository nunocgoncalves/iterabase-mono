package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

const threeNodeK3sVersion = "v1.34.10+k3s1"

type rwxThreeNodeState struct {
	ctx         context.Context
	client      *godo.Client
	runID       string
	keep        bool
	pubKey      string
	privKeyPath string
	droplets    []*godo.Droplet
	ips         []string
	removed     map[int]bool
	serverIP    string
	archive     string
	pvcUID      string
	pvName      string
	volume      string
}

func newRWXThreeNodeState(t *testing.T) *rwxThreeNodeState {
	t.Helper()
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		if os.Getenv("FORGE_E2E_REQUIRE_CAPACITY") == "true" {
			t.Fatal("mandatory three-node RWX release validation incomplete — DIGITALOCEAN_TOKEN not set")
		}
		t.Skip("DIGITALOCEAN_TOKEN not set; skipping three-node RWX scenario")
	}
	pubKey, privateKey := generateKey(t)
	return &rwxThreeNodeState{
		ctx: context.Background(), client: godo.NewFromToken(token),
		runID: fmt.Sprintf("rwx-three-node-%d", time.Now().Unix()),
		keep:  os.Getenv("FORGE_E2E_KEEP") != "", pubKey: pubKey, privKeyPath: privateKey,
		removed: make(map[int]bool),
	}
}

func provisionRWXThreeNodesStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("%s-%d", state.runID, i)
		droplet, err := createDroplet(state.ctx, state.client, name, state.pubKey)
		if err != nil {
			t.Fatalf("create RWX node %d: %v", i, err)
		}
		state.droplets = append(state.droplets, droplet)
		ip, err := waitForIP(state.ctx, state.client, droplet.ID)
		if err != nil {
			t.Fatalf("wait for RWX node %d IP: %v", i, err)
		}
		state.ips = append(state.ips, ip)
		if err := waitForHostReady(state.ctx, ip, state.privKeyPath); err != nil {
			t.Fatalf("wait for RWX node %d SSH: %v", i, err)
		}
	}
	state.serverIP = state.ips[0]
}

func bootstrapRWXThreeNodeK3sStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	for i, ip := range state.ips {
		client, err := sshDial(ip, state.privKeyPath)
		if err != nil {
			t.Fatalf("dial RWX node %d: %v", i, err)
		}
		setup := `sudo bash -ceu '
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y open-iscsi nfs-common jq
systemctl enable --now iscsid
modprobe iscsi_tcp
mount --make-rshared /
install -d -m 0750 /var/lib/longhorn
findmnt -n -o FSTYPE --target /var/lib/longhorn | grep -Eq "^(ext4|xfs)$"
findmnt -n -o PROPAGATION / | grep -Eq "(^|,)r?shared(,|$)"
'`
		if output, err := sshOutput(client, setup); err != nil {
			client.Close()
			t.Fatalf("prepare RWX node %d: %v\n%s", i, err, output)
		}
		client.Close()
	}

	token := "hor469-" + state.runID
	server, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	serverInstall := fmt.Sprintf("curl -sfL https://get.k3s.io | sudo env INSTALL_K3S_VERSION=%s K3S_TOKEN=%s sh -s - server --disable traefik --tls-san %s --write-kubeconfig-mode 0644",
		candidateShellQuote(threeNodeK3sVersion), candidateShellQuote(token), candidateShellQuote(state.serverIP))
	if output, err := sshOutput(server, serverInstall); err != nil {
		server.Close()
		t.Fatalf("install three-node K3s server: %v\n%s", err, output)
	}
	server.Close()

	for i := 1; i < len(state.ips); i++ {
		client, err := sshDial(state.ips[i], state.privKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		join := fmt.Sprintf("curl -sfL https://get.k3s.io | sudo env INSTALL_K3S_VERSION=%s K3S_URL=%s K3S_TOKEN=%s sh -s - agent",
			candidateShellQuote(threeNodeK3sVersion), candidateShellQuote("https://"+state.serverIP+":6443"), candidateShellQuote(token))
		if output, err := sshOutput(client, join); err != nil {
			client.Close()
			t.Fatalf("join three-node K3s agent %d: %v\n%s", i, err, output)
		}
		client.Close()
	}

	server, err = sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mustSSHOutput(t, server, "sudo k3s kubectl wait --for=condition=Ready nodes --all --timeout=10m")
	mustSSHOutput(t, server, "test \"$(sudo k3s kubectl get nodes --no-headers | wc -l)\" -eq 3")
	mustSSHOutput(t, server, "sudo k3s kubectl label nodes --all --overwrite node.longhorn.io/create-default-disk=true")
	version := strings.TrimSpace(mustSSHOutput(t, server, `sudo k3s kubectl version -o json | jq -r '.serverVersion.gitVersion'`))
	if version != threeNodeK3sVersion {
		t.Fatalf("three-node K3s version=%q want=%q", version, threeNodeK3sVersion)
	}
}

func packageRWXCompanion(t *testing.T) string {
	t.Helper()
	if archive := os.Getenv(storageChartArchiveEnv); archive != "" {
		return archive
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(root, "charts", "charts", "rwx-storage-substrate")
	if output, err := exec.Command("helm", "dependency", "build", chart).CombinedOutput(); err != nil {
		t.Fatalf("build RWX companion dependency: %v\n%s", err, output)
	}
	destination := t.TempDir()
	if output, err := exec.Command("helm", "package", chart, "--destination", destination).CombinedOutput(); err != nil {
		t.Fatalf("package RWX companion: %v\n%s", err, output)
	}
	matches, err := filepath.Glob(filepath.Join(destination, "rwx-storage-substrate-*.tgz"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("resolve packaged RWX companion: matches=%v err=%v", matches, err)
	}
	return matches[0]
}

func installThreeNodeRWXCompanionStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	state.archive = packageRWXCompanion(t)
	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	remoteArchive := "/tmp/" + filepath.Base(state.archive)
	source, err := os.Open(state.archive)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession()
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	session.Stdin = source
	output, transferErr := session.CombinedOutput("cat > " + candidateShellQuote(remoteArchive))
	session.Close()
	source.Close()
	if transferErr != nil {
		t.Fatalf("transfer RWX companion: %v\n%s", transferErr, output)
	}
	installHelm := `set -e
if ! sudo helm version --short >/dev/null 2>&1; then
  p=$(mktemp)
  curl -fsSL -o "$p" https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-4
  test -s "$p"
  sudo bash "$p"
  rm -f "$p"
fi
sudo k3s kubectl create namespace iterabase-system --dry-run=client -o yaml | sudo k3s kubectl apply -f -
`
	mustSSHOutput(t, client, installHelm)
	command := fmt.Sprintf("sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade --install rwx-three %s -n longhorn-system --create-namespace --set storage.rwx.managedLonghorn.topology=three-node --set validation.attestationNamespace=iterabase-system --wait --timeout 35m",
		candidateShellQuote(remoteArchive))
	if result, err := sshOutput(client, command); err != nil {
		t.Fatalf("install three-node RWX companion: %v\n%s", err, result)
	}
	class := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get storageclass iterabase-rwx -o jsonpath='{.parameters.numberOfReplicas}|{.reclaimPolicy}|{.allowVolumeExpansion}'`))
	if class != "3|Retain|true" {
		t.Fatalf("three-node StorageClass=%q", class)
	}
	attestation := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get configmap -n iterabase-system -l platform.iterabase.com/storage-conformance=true -o jsonpath='{.items[0].data.result}|{.items[0].data.contractVersion}'`))
	if attestation != "pass|HOR-469/v1" {
		t.Fatalf("three-node conformance attestation=%q", attestation)
	}
}

func seedThreeNodeRWXVolumeStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	manifest := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: three-node-evidence, namespace: iterabase-system}
spec:
  accessModes: [ReadWriteMany]
  storageClassName: iterabase-rwx
  resources: {requests: {storage: 2Gi}}
---
apiVersion: batch/v1
kind: Job
metadata: {name: three-node-writer, namespace: iterabase-system}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: writer
          image: debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
          command: [bash, -ceu]
          args: ['printf three-node-committed > /data/marker && sync -f /data/marker']
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: three-node-evidence}}]
YAML`
	mustSSHOutput(t, client, manifest)
	mustSSHOutput(t, client, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/three-node-writer --timeout=15m")
	identity := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get pvc three-node-evidence -n iterabase-system -o jsonpath='{.metadata.uid}|{.spec.volumeName}'`))
	parts := strings.Split(identity, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("three-node PVC identity=%q", identity)
	}
	state.pvcUID, state.pvName = parts[0], parts[1]
	state.volume = strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf("sudo k3s kubectl get pv %s -o jsonpath='{.spec.csi.volumeHandle}'", state.pvName)))
	volume := strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl get volumes.longhorn.io %s -n longhorn-system -o jsonpath='{.spec.numberOfReplicas}|{.status.robustness}'`, state.volume)))
	if volume != "3|healthy" {
		t.Fatalf("three-node Longhorn volume=%q", volume)
	}
}

func replaceLostRWXNodeStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	lost := state.droplets[2]
	if _, err := state.client.Droplets.Delete(state.ctx, lost.ID); err != nil {
		t.Fatalf("delete one RWX storage node: %v", err)
	}
	state.removed[lost.ID] = true

	server, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	lostNode := lost.Name
	mustSSHOutput(t, server, "sudo k3s kubectl get node "+candidateShellQuote(lostNode))
	degradedDeadline := time.Now().Add(8 * time.Minute)
	degraded := ""
	for time.Now().Before(degradedDeadline) {
		out, commandErr := sshOutput(server, fmt.Sprintf(`sudo k3s kubectl get volumes.longhorn.io %s -n longhorn-system -o jsonpath='{.status.robustness}'`, state.volume))
		degraded = strings.TrimSpace(out)
		if commandErr == nil && degraded != "" && degraded != "healthy" {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if degraded == "" || degraded == "healthy" {
		server.Close()
		t.Fatalf("Longhorn did not expose degraded/unknown volume health after hard node loss: %q", degraded)
	}

	replacementName := state.runID + "-replacement"
	replacement, err := createDroplet(state.ctx, state.client, replacementName, state.pubKey)
	if err != nil {
		server.Close()
		t.Fatalf("create replacement RWX node: %v", err)
	}
	state.droplets = append(state.droplets, replacement)
	replacementIP, err := waitForIP(state.ctx, state.client, replacement.ID)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	state.ips = append(state.ips, replacementIP)
	if err := waitForHostReady(state.ctx, replacementIP, state.privKeyPath); err != nil {
		server.Close()
		t.Fatal(err)
	}
	replacementSSH, err := sshDial(replacementIP, state.privKeyPath)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	setup := `sudo bash -ceu '
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y open-iscsi nfs-common jq
systemctl enable --now iscsid
modprobe iscsi_tcp
mount --make-rshared /
install -d -m 0750 /var/lib/longhorn
'`
	mustSSHOutput(t, replacementSSH, setup)
	token := "hor469-" + state.runID
	join := fmt.Sprintf("curl -sfL https://get.k3s.io | sudo env INSTALL_K3S_VERSION=%s K3S_URL=%s K3S_TOKEN=%s sh -s - agent",
		candidateShellQuote(threeNodeK3sVersion), candidateShellQuote("https://"+state.serverIP+":6443"), candidateShellQuote(token))
	mustSSHOutput(t, replacementSSH, join)
	replacementSSH.Close()
	mustSSHOutput(t, server, "sudo k3s kubectl delete node "+candidateShellQuote(lostNode)+" --wait=false")
	mustSSHOutput(t, server, "sudo k3s kubectl wait --for=condition=Ready nodes --all --timeout=10m")
	mustSSHOutput(t, server, "sudo k3s kubectl label nodes --all --overwrite node.longhorn.io/create-default-disk=true")
	anchor := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: v1
kind: Pod
metadata: {name: three-node-rebuild-anchor, namespace: iterabase-system}
spec:
  restartPolicy: Never
  containers:
    - name: anchor
      image: debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
      command: [bash, -ceu]
      args: ['test "$(cat /data/marker)" = three-node-committed; sleep 1200']
      volumeMounts: [{name: data, mountPath: /data}]
  volumes: [{name: data, persistentVolumeClaim: {claimName: three-node-evidence}}]
YAML`
	mustSSHOutput(t, server, anchor)
	mustSSHOutput(t, server, "sudo k3s kubectl wait -n iterabase-system --for=condition=Ready pod/three-node-rebuild-anchor --timeout=10m")

	// Remove only replicas tied to the confirmed lost node after the replacement
	// is Ready. Longhorn then rebuilds the detached committed volume onto the new
	// third failure domain; no workload/turn is retried.
	deleteReplicas := fmt.Sprintf(`for replica in $(sudo k3s kubectl get replicas.longhorn.io -n longhorn-system -o json | jq -r --arg volume %s --arg node %s '.items[] | select(.spec.volumeName==$volume and .spec.nodeID==$node) | .metadata.name'); do sudo k3s kubectl delete replica.longhorn.io "$replica" -n longhorn-system; done`, candidateShellQuote(state.volume), candidateShellQuote(lostNode))
	mustSSHOutput(t, server, deleteReplicas)

	deadline := time.Now().Add(20 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		out, commandErr := sshOutput(server, fmt.Sprintf(`sudo k3s kubectl get volumes.longhorn.io %s -n longhorn-system -o jsonpath='{.status.robustness}'`, state.volume))
		last = strings.TrimSpace(out)
		if commandErr == nil && last == "healthy" {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if last != "healthy" {
		server.Close()
		t.Fatalf("three-node volume did not rebuild healthy: %q", last)
	}
	replicaNodes := strings.TrimSpace(mustSSHOutput(t, server, fmt.Sprintf(`sudo k3s kubectl get replicas.longhorn.io -n longhorn-system -o json | jq -r --arg volume %s '[.items[] | select(.spec.volumeName==$volume and (.spec.failedAt // "")=="") | .spec.nodeID] | unique | length'`, candidateShellQuote(state.volume))))
	if replicaNodes != "3" {
		server.Close()
		t.Fatalf("rebuilt Longhorn volume spans %s healthy replica nodes, want 3", replicaNodes)
	}
	mustSSHOutput(t, server, "sudo k3s kubectl delete pod three-node-rebuild-anchor -n iterabase-system --wait=true")
	server.Close()
}

func assertThreeNodePersistenceReapplyAndUninstallStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	remoteArchive := "/tmp/" + filepath.Base(state.archive)
	reapply := fmt.Sprintf("sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade rwx-three %s -n longhorn-system --set storage.rwx.managedLonghorn.topology=three-node --set validation.attestationNamespace=iterabase-system --wait --timeout 35m", candidateShellQuote(remoteArchive))
	mustSSHOutput(t, client, reapply)
	uid := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get pvc three-node-evidence -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	if uid != state.pvcUID {
		t.Fatalf("three-node reapply replaced PVC: before=%s after=%s", state.pvcUID, uid)
	}
	reader := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata: {name: three-node-replacement-reader, namespace: iterabase-system}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: reader
          image: debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
          command: [bash, -ceu]
          args: ['test "$(cat /data/marker)" = three-node-committed']
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: three-node-evidence}}]
YAML`
	mustSSHOutput(t, client, reader)
	mustSSHOutput(t, client, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/three-node-replacement-reader --timeout=15m")

	uninstall := "sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml uninstall rwx-three -n longhorn-system --wait"
	if output, err := sshOutput(client, uninstall); err == nil || !strings.Contains(output+err.Error(), "refusing managed RWX uninstall") {
		t.Fatalf("managed uninstall did not refuse retained consumers: err=%v output=%s", err, output)
	}

	mustSSHOutput(t, client, "sudo k3s kubectl delete job three-node-writer three-node-replacement-reader -n iterabase-system --ignore-not-found")
	mustSSHOutput(t, client, fmt.Sprintf("sudo k3s kubectl patch pv %s --type=merge -p '{\"spec\":{\"persistentVolumeReclaimPolicy\":\"Delete\"}}'", state.pvName))
	mustSSHOutput(t, client, "sudo k3s kubectl delete pvc three-node-evidence -n iterabase-system --wait=true")
	deadline := time.Now().Add(10 * time.Minute)
	removed := false
	for time.Now().Before(deadline) {
		_, pvErr := sshOutput(client, "sudo k3s kubectl get pv "+candidateShellQuote(state.pvName))
		_, volumeErr := sshOutput(client, "sudo k3s kubectl get volumes.longhorn.io "+candidateShellQuote(state.volume)+" -n longhorn-system")
		if pvErr != nil && volumeErr != nil {
			removed = true
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !removed {
		t.Fatal("disposed PV/Longhorn volume did not disappear before uninstall")
	}
	mustSSHOutput(t, client, uninstall)
	if _, err := sshOutput(client, "sudo k3s kubectl get daemonset longhorn-manager -n longhorn-system"); err == nil {
		t.Fatal("Longhorn manager remained after deletion-confirmed uninstall")
	}
}

func (state *rwxThreeNodeState) diagnostics(t *testing.T) {
	t.Helper()
	if state.serverIP == "" {
		return
	}
	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Logf("three-node diagnostics dial: %v", err)
		return
	}
	defer client.Close()
	for _, command := range []string{
		"sudo k3s kubectl logs -n longhorn-system -l app.kubernetes.io/component=storage-validation --all-containers --tail=500",
		"sudo k3s kubectl get nodes -o wide",
		"sudo k3s kubectl get storageclass,pv -o wide",
		"sudo k3s kubectl get pvc,pod,job -A -o wide",
		"sudo k3s kubectl get nodes.longhorn.io,volumes.longhorn.io,replicas.longhorn.io -n longhorn-system -o wide",
		"sudo k3s kubectl get events -A --sort-by=.lastTimestamp | tail -200",
	} {
		if output, err := sshOutput(client, command); err == nil {
			t.Logf("three-node diagnostics %s:\n%s", command, output)
		}
	}
}

func (state *rwxThreeNodeState) cleanup(t *testing.T) {
	t.Helper()
	if state.keep {
		for _, droplet := range state.droplets {
			t.Logf("keeping three-node RWX droplet %d (%s)", droplet.ID, droplet.Name)
		}
		return
	}
	for _, droplet := range state.droplets {
		if state.removed[droplet.ID] {
			continue
		}
		if _, err := state.client.Droplets.Delete(state.ctx, droplet.ID); err != nil {
			t.Errorf("delete three-node RWX droplet %d: %v", droplet.ID, err)
		}
	}
}
