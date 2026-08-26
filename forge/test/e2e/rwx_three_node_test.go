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
	"golang.org/x/crypto/ssh"
)

const threeNodeK3sVersion = "v1.34.10+k3s1"

type rwxThreeNodeState struct {
	ctx            context.Context
	client         *godo.Client
	runID          string
	keep           bool
	pubKey         string
	privKeyPath    string
	droplets       []*godo.Droplet
	volumes        []*godo.Volume
	volumeByNodeID map[int]*godo.Volume
	ips            []string
	removed        map[int]bool
	serverIP       string
	archive        string
	pvcUID         string
	pvName         string
	volume         string
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
		removed: make(map[int]bool), volumeByNodeID: make(map[int]*godo.Volume),
	}
}

func provisionRWXThreeNodesStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("%s-%d", state.runID, i)
		_, ip := provisionRWXNode(t, state, name)
		state.ips = append(state.ips, ip)
	}
	state.serverIP = state.ips[0]
}

func provisionRWXNode(t *testing.T, state *rwxThreeNodeState, name string) (*godo.Droplet, string) {
	t.Helper()
	droplet, err := createDroplet(state.ctx, state.client, name, state.pubKey)
	if err != nil {
		t.Fatalf("create RWX node %s: %v", name, err)
	}
	state.droplets = append(state.droplets, droplet)
	volume, _, err := state.client.Storage.CreateVolume(state.ctx, newRWXVolumeRequest(state.runID, name))
	if err != nil {
		t.Fatalf("create dedicated RWX SSD for %s: %v", name, err)
	}
	state.volumes = append(state.volumes, volume)
	state.volumeByNodeID[droplet.ID] = volume
	if _, _, err := state.client.StorageActions.Attach(state.ctx, volume.ID, droplet.ID); err != nil {
		t.Fatalf("attach dedicated RWX SSD %s to %s: %v", volume.ID, name, err)
	}
	ip, err := waitForIP(state.ctx, state.client, droplet.ID)
	if err != nil {
		t.Fatalf("wait for RWX node %s IP: %v", name, err)
	}
	if err := waitForHostReady(state.ctx, ip, state.privKeyPath); err != nil {
		t.Fatalf("wait for RWX node %s SSH: %v", name, err)
	}
	return droplet, ip
}

// DigitalOcean Volumes are the provider's SSD-backed block-storage product.
// The API identity plus guest block-device/mount/size identity is authoritative;
// a virtual device's Linux ROTA bit is hypervisor metadata, not media evidence.
func newRWXVolumeRequest(runID, nodeName string) *godo.VolumeCreateRequest {
	return &godo.VolumeCreateRequest{
		Region: region, Name: nodeName + "-ssd", Description: "HOR-469 dedicated Longhorn data path", SizeGigaBytes: 25,
		Tags: []string{"forge-e2e", runID},
	}
}

func prepareDedicatedRWXDisk(t *testing.T, client *ssh.Client, volumeName string) {
	t.Helper()
	device := "/dev/disk/by-id/scsi-0DO_Volume_" + volumeName
	command := fmt.Sprintf(`sudo bash -ceu '
device=%s
for i in $(seq 1 120); do test -b "$device" && break; sleep 1; done
test -b "$device"
if ! blkid "$device" >/dev/null 2>&1; then mkfs.ext4 -F "$device"; fi
install -d -m 0750 /var/lib/longhorn
uuid=$(blkid -s UUID -o value "$device")
grep -q "UUID=$uuid " /etc/fstab || printf "UUID=%%s /var/lib/longhorn ext4 defaults,nofail 0 2\\n" "$uuid" >> /etc/fstab
mountpoint -q /var/lib/longhorn || mount /var/lib/longhorn
data_source=$(readlink -f "$(findmnt -n -o SOURCE --target /var/lib/longhorn)")
root_source=$(readlink -f "$(findmnt -n -o SOURCE --target /)")
expected_source=$(readlink -f "$device")
data_bytes=$(blockdev --getsize64 "$device")
data_fstype=$(findmnt -n -o FSTYPE --target /var/lib/longhorn)
printf "HOR-469 dedicated DigitalOcean SSD: source=%%s root=%%s bytes=%%s fstype=%%s\\n" "$data_source" "$root_source" "$data_bytes" "$data_fstype"
test "$data_source" = "$expected_source"
test "$data_source" != "$root_source"
test "$data_bytes" -ge 25000000000
printf "%%s\\n" "$data_fstype" | grep -Eq "^(ext4|xfs)$"
'`, candidateShellQuote(device))
	mustSSHOutput(t, client, command)
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
findmnt -n -o PROPAGATION / | grep -Eq "(^|,)r?shared(,|$)"
'`
		if output, err := sshOutput(client, setup); err != nil {
			client.Close()
			t.Fatalf("prepare RWX node %d: %v\n%s", i, err, output)
		}
		prepareDedicatedRWXDisk(t, client, state.volumeByNodeID[state.droplets[i].ID].Name)
		if output, err := sshOutput(client, "findmnt -n -o SOURCE,FSTYPE --target /var/lib/longhorn"); err != nil {
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

func transferRWXFixture(t *testing.T, client *ssh.Client, localPath, remotePath string) {
	t.Helper()
	source, err := os.Open(localPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	session.Stdin = source
	output, transferErr := session.CombinedOutput("cat > " + candidateShellQuote(remotePath))
	session.Close()
	if transferErr != nil {
		t.Fatalf("transfer %s: %v\n%s", localPath, transferErr, output)
	}
}

func installThreeNodeRWXPredecessorStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	state.archive = packageRWXCompanion(t)
	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	transferRWXFixture(t, client, state.archive, "/tmp/"+filepath.Base(state.archive))
	installHelm := `set -e
if ! sudo helm version --short >/dev/null 2>&1; then
  p=$(mktemp)
  curl -fsSL -o "$p" https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-4
  test -s "$p"
  sudo bash "$p"
  rm -f "$p"
fi
sudo helm repo add longhorn https://charts.longhorn.io --force-update
sudo helm repo update longhorn
sudo k3s kubectl create namespace iterabase-system --dry-run=client -o yaml | sudo k3s kubectl apply -f -
`
	mustSSHOutput(t, client, installHelm)
	predecessor := `sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade --install three-rwx-storage longhorn/longhorn --version 1.11.3 -n longhorn-system --create-namespace --wait --timeout 45m \
  --set persistence.defaultClass=false \
  --set defaultSettings.createDefaultDiskLabeledNodes=true \
  --set defaultSettings.defaultDataPath=/var/lib/longhorn \
  --set defaultSettings.defaultReplicaCount=3 \
  --set defaultSettings.replicaSoftAntiAffinity=false \
  --set defaultSettings.storageOverProvisioningPercentage=100 \
  --set defaultSettings.storageMinimalAvailablePercentage=25 \
  --set defaultSettings.allowVolumeCreationWithDegradedAvailability=false \
  --set defaultSettings.v2DataEngine=false \
  --set networkPolicies.enabled=true \
  --set networkPolicies.restrictInternalTraffic=true \
  --set networkPolicies.type=k3s`
	mustSSHOutput(t, client, predecessor)
	classes := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: iterabase-rwx
  labels: {app.kubernetes.io/managed-by: Helm}
  annotations:
    meta.helm.sh/release-name: three-rwx-storage
    meta.helm.sh/release-namespace: longhorn-system
    storageclass.kubernetes.io/is-default-class: "false"
provisioner: driver.longhorn.io
allowVolumeExpansion: true
reclaimPolicy: Retain
volumeBindingMode: Immediate
parameters:
  dataEngine: v1
  fsType: ext4
  fromBackup: ""
  migratable: "false"
  numberOfReplicas: "3"
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: customer-reference-rwx
  annotations: {storageclass.kubernetes.io/is-default-class: "false"}
provisioner: driver.longhorn.io
allowVolumeExpansion: true
reclaimPolicy: Retain
volumeBindingMode: Immediate
parameters:
  dataEngine: v1
  fsType: ext4
  fromBackup: ""
  migratable: "false"
  numberOfReplicas: "3"
YAML`
	mustSSHOutput(t, client, classes)
	managerImage := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get daemonset longhorn-manager -n longhorn-system -o jsonpath='{.spec.template.spec.containers[0].image}'`))
	if !strings.Contains(managerImage, "v1.11.3") {
		t.Fatalf("predecessor manager image=%q want v1.11.3", managerImage)
	}
	for i, droplet := range state.droplets[:3] {
		volume := state.volumeByNodeID[droplet.ID]
		if volume == nil {
			t.Fatalf("storage node %d has no DigitalOcean SSD volume identity", i)
		}
		waitForDisk := fmt.Sprintf(`for i in $(seq 1 120); do path=$(sudo k3s kubectl get nodes.longhorn.io %s -n longhorn-system -o json 2>/dev/null | jq -r '.spec.disks | to_entries[] | select(.value.path=="/var/lib/longhorn") | .value.path'); if test "$path" = /var/lib/longhorn; then printf '%%s\n' "$path"; exit 0; fi; sleep 5; done; sudo k3s kubectl get nodes.longhorn.io %s -n longhorn-system -o yaml >&2; exit 1`, candidateShellQuote(droplet.Name), candidateShellQuote(droplet.Name))
		identity := strings.TrimSpace(mustSSHOutput(t, client, waitForDisk))
		if identity != "/var/lib/longhorn" {
			t.Fatalf("storage node %d lacks its dedicated SSD-backed Longhorn path: %q", i, identity)
		}
	}
}

func packageCertManagerCompanion(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(root, "charts", "charts", "cert-manager-substrate")
	if output, err := exec.Command("helm", "dependency", "build", chart).CombinedOutput(); err != nil {
		t.Fatalf("build cert-manager companion dependency: %v\n%s", err, output)
	}
	destination := t.TempDir()
	if output, err := exec.Command("helm", "package", chart, "--destination", destination).CombinedOutput(); err != nil {
		t.Fatalf("package cert-manager companion: %v\n%s", err, output)
	}
	matches, err := filepath.Glob(filepath.Join(destination, "cert-manager-substrate-*.tgz"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("resolve packaged cert-manager companion: matches=%v err=%v", matches, err)
	}
	return matches[0]
}

func buildExternalAgentPoolFixtures(t *testing.T, state *rwxThreeNodeState) (string, string, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	controllerBinary := filepath.Join(t.TempDir(), "agentpool-storage-controller")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", controllerBinary, "./internal/controller/e2econtroller")
	build.Dir = filepath.Join(root, "control-plane")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build AgentPool storage controller fixture: %v\n%s", err, output)
	}

	workerDir := t.TempDir()
	workerScript := `import http.server
import os
import pathlib
import socketserver

root = pathlib.Path("/data/sandboxes")
os.chown(root, 0, 0)
os.chmod(root, 0o711)
slot = root / os.environ.get("HOSTNAME", "unknown-worker")
slot.mkdir(mode=0o700, exist_ok=True)
os.chmod(slot, 0o700)
tmp = slot / "transaction.tmp"
with tmp.open("w") as handle:
    handle.write("agentpool-mounted")
    handle.flush()
    os.fsync(handle.fileno())
os.replace(tmp, slot / "marker")
probe = slot / "unlink-probe"
probe.write_text("probe")
probe.unlink()
fd = os.open(slot, os.O_RDONLY)
os.fsync(fd)
os.close(fd)

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path not in ("/readyz", "/healthz"):
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self, *_):
        pass

with socketserver.TCPServer(("0.0.0.0", 8081), Handler) as server:
    server.serve_forever()
`
	requireNoError := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	requireNoError(os.WriteFile(filepath.Join(workerDir, "worker.py"), []byte(workerScript), 0o644))
	dockerfile := `FROM docker.io/library/python:3.13-alpine@sha256:540c7d91f98ff6880174c40e99067bf5941eb54d818a7a5e094d188b196a934d
COPY worker.py /worker.py
ENTRYPOINT ["python3", "/worker.py"]
`
	requireNoError(os.WriteFile(filepath.Join(workerDir, "Dockerfile"), []byte(dockerfile), 0o644))
	workerImage := "hor469-external-worker:" + state.runID
	workerBuild := exec.Command("docker", "build", "--platform", "linux/amd64", "-t", workerImage, workerDir)
	if output, err := workerBuild.CombinedOutput(); err != nil {
		t.Fatalf("build external AgentPool worker fixture: %v\n%s", err, output)
	}
	workerArchive := filepath.Join(t.TempDir(), "external-agentpool-worker.tar")
	if output, err := exec.Command("docker", "save", "-o", workerArchive, workerImage).CombinedOutput(); err != nil {
		t.Fatalf("save external AgentPool worker fixture: %v\n%s", err, output)
	}
	return controllerBinary, workerArchive, workerImage
}

func validateExternalRWXAgentPoolStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	controllerBinary, workerArchive, workerImage := buildExternalAgentPoolFixtures(t, state)
	certArchive := packageCertManagerCompanion(t)
	platformChart := filepath.Join(root, "charts", "charts", "iterabase-platform")
	if output, err := exec.Command("helm", "dependency", "build", platformChart).CombinedOutput(); err != nil {
		t.Fatalf("build external platform contract dependencies: %v\n%s", err, output)
	}
	externalContract := filepath.Join(t.TempDir(), "external-storage-contract.yaml")
	render := exec.Command("helm", "template", "external-reference-platform", platformChart,
		"--namespace", "iterabase-system",
		"-f", filepath.Join(root, "charts", "values-external-rwx.yaml"),
		"--set", "storage.rwx.storageClassName=customer-reference-rwx",
		"--show-only", "templates/rwx-storage-contract.yaml")
	rendered, err := render.CombinedOutput()
	if err != nil {
		t.Fatalf("render external platform storage contract: %v\n%s", err, rendered)
	}
	if err := os.WriteFile(externalContract, rendered, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ip := range state.ips[:3] {
		client, err := sshDial(ip, state.privKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		transferRWXFixture(t, client, workerArchive, "/tmp/external-agentpool-worker.tar")
		mustSSHOutput(t, client, "sudo k3s ctr images import /tmp/external-agentpool-worker.tar")
		client.Close()
	}

	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	transferRWXFixture(t, client, certArchive, "/tmp/"+filepath.Base(certArchive))
	transferRWXFixture(t, client, controllerBinary, "/tmp/agentpool-storage-controller")
	transferRWXFixture(t, client, filepath.Join(root, "control-plane", "config", "crd", "bases", "platform.iterabase.com_agentpools.yaml"), "/tmp/agentpool-crd.yaml")
	transferRWXFixture(t, client, filepath.Join(root, "docs", "architecture", "validation", "hor-424-rwx-conformance.sh"), "/tmp/hor-424-rwx-conformance.sh")
	transferRWXFixture(t, client, filepath.Join(root, "docs", "architecture", "validation", "hor-424-rwx-conformance.yaml"), "/tmp/hor-424-rwx-conformance.yaml")
	transferRWXFixture(t, client, externalContract, "/tmp/external-storage-contract.yaml")

	certInstall := fmt.Sprintf("sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade --install cert-e2e %s -n cert-manager --create-namespace --wait --timeout 20m", candidateShellQuote("/tmp/"+filepath.Base(certArchive)))
	mustSSHOutput(t, client, certInstall)
	issuers := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: platform-storage-e2e-selfsigned}
spec: {selfSigned: {}}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: platform-spiffe-ca, namespace: cert-manager}
spec:
  isCA: true
  commonName: platform-spiffe-ca
  secretName: platform-ca
  duration: 8760h
  renewBefore: 720h
  issuerRef: {name: platform-storage-e2e-selfsigned, kind: ClusterIssuer}
  usages: [cert sign, crl sign, digital signature]
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: platform-spiffe-ca}
spec:
  ca: {secretName: platform-ca}
YAML`
	mustSSHOutput(t, client, issuers)
	mustSSHOutput(t, client, "sudo k3s kubectl wait --for=condition=Ready certificate/platform-spiffe-ca -n cert-manager --timeout=5m")
	mustSSHOutput(t, client, `sudo k3s kubectl get secret platform-ca -n cert-manager -o json | jq 'del(.metadata.creationTimestamp,.metadata.resourceVersion,.metadata.uid,.metadata.ownerReferences) | .metadata.namespace="iterabase-system"' | sudo k3s kubectl apply -f -`)

	conformance := `env HOR424_STORAGE_CLASS=customer-reference-rwx HOR424_NAMESPACE=hor469-external-reference HOR424_ATTEST_NAMESPACE=iterabase-system HOR424_CLEANUP=true HOR424_CONTEXT=external-reference/longhorn-1.11.3 KUBECTL="sudo k3s kubectl" /bin/bash /tmp/hor-424-rwx-conformance.sh`
	mustSSHOutput(t, client, conformance)
	attestation := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get configmap -n iterabase-system -l platform.iterabase.com/storage-conformance=true -o json | jq -r '.items[] | select(.data.storageClassName=="customer-reference-rwx") | "\(.data.result)|\(.data.contractVersion)|\(.data.context)"'`))
	if attestation != "pass|HOR-469/v1|external-reference/longhorn-1.11.3" {
		t.Fatalf("external reference conformance attestation=%q", attestation)
	}

	mustSSHOutput(t, client, "sudo k3s kubectl apply -f /tmp/agentpool-crd.yaml")
	mustSSHOutput(t, client, "sudo install -m 0755 /tmp/agentpool-storage-controller /usr/local/bin/agentpool-storage-controller")
	mustSSHOutput(t, client, `sudo sh -c 'KUBECONFIG=/etc/rancher/k3s/k3s.yaml nohup /usr/local/bin/agentpool-storage-controller >/var/log/agentpool-storage-controller.log 2>&1 & echo $! >/run/agentpool-storage-controller.pid'`)
	mustSSHOutput(t, client, "sudo k3s kubectl apply -n iterabase-system -f /tmp/external-storage-contract.yaml")
	contractAndPool := fmt.Sprintf(`cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: platform.iterabase.com/v1alpha1
kind: AgentPool
metadata: {name: external-reference-pool, namespace: iterabase-system}
spec:
  replicas: 2
  workerImage: %s
  podSecurity: baseline
  identity:
    trustDomain: iterabase.local
    caSecretRef: {name: platform-ca}
    certMountPath: /etc/harness/tls
  sandbox:
    storageClassName: customer-reference-rwx
    accessMode: ReadWriteMany
    size: 2Gi
    mountPath: /data/sandboxes
  gateways:
    controlPlane:
      url: https://control-plane.iterabase-system.svc:8443
      serverName: control-plane
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: control-plane}}}
    toolGateway:
      url: https://gateway.iterabase-system.svc:8443
      serverName: tool-gateway
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: tool-gateway}}}
    inferenceGateway:
      url: https://inference-gateway.iterabase-system.svc:8443
      serverName: inference-gateway
      selector: {podSelector: {matchLabels: {app.kubernetes.io/name: inference-gateway}}}
  networkPolicy: {egress: denied}
  workspaceTools: false
  gatewayGrants: []
  credentialBindings: []
  piDirs: [/pi/product, /pi/client]
  walDir: /var/harness/wal
  probe: {port: 8081}
YAML`, workerImage)
	mustSSHOutput(t, client, contractAndPool)
	agentPoolDiagnostics := func() string {
		output, _ := sshOutput(client, `{ echo '=== AgentPool ==='; sudo k3s kubectl get agentpool external-reference-pool -n iterabase-system -o yaml || true; echo '=== controller ==='; sudo tail -n 300 /var/log/agentpool-storage-controller.log || true; echo '=== owned resources ==='; sudo k3s kubectl get pvc,pod,configmap,networkpolicy -n iterabase-system -l platform.iterabase.com/agentpool=external-reference-pool -o wide || true; echo '=== recent events ==='; sudo k3s kubectl get events -n iterabase-system --sort-by=.lastTimestamp | tail -100 || true; } 2>&1`)
		return output
	}
	waitForWorkers := `for i in $(seq 1 120); do test "$(sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=external-reference-pool --no-headers 2>/dev/null | wc -l)" -eq 2 && exit 0; sleep 2; done; exit 1`
	if output, err := sshOutput(client, waitForWorkers); err != nil {
		t.Fatalf("external AgentPool did not create two workers: %v\n%s\n%s", err, output, agentPoolDiagnostics())
	}
	if output, err := sshOutput(client, "sudo k3s kubectl wait -n iterabase-system --for=condition=Ready pod -l platform.iterabase.com/agentpool=external-reference-pool --timeout=15m"); err != nil {
		t.Fatalf("external AgentPool workers did not become Ready: %v\n%s\n%s", err, output, agentPoolDiagnostics())
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		status := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get agentpool external-reference-pool -n iterabase-system -o jsonpath='{.status.ready}|{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}'`))
		if status == "true|2|StorageReady" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	status := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get agentpool external-reference-pool -n iterabase-system -o jsonpath='{.status.ready}|{.status.readyReplicas}|{.status.conditions[?(@.type=="StorageReady")].reason}'`))
	if status != "true|2|StorageReady" {
		t.Fatalf("external AgentPool did not become storage ready: %q", status)
	}
	verifier := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata: {name: external-agentpool-verifier, namespace: iterabase-system}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: verifier
          image: debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
          command: [bash, -ceu]
          args: ['test "$(find /data -mindepth 2 -maxdepth 2 -name marker -exec grep -l agentpool-mounted {} + | wc -l)" -eq 2']
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: external-reference-pool-sandbox}}]
YAML`
	mustSSHOutput(t, client, verifier)
	mustSSHOutput(t, client, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/external-agentpool-verifier --timeout=10m")
	identity := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get pvc external-reference-pool-sandbox -n iterabase-system -o jsonpath='{.spec.volumeName}'`))
	volumeHandle := strings.TrimSpace(mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl get pv %s -o jsonpath='{.spec.csi.volumeHandle}'`, candidateShellQuote(identity))))
	mustSSHOutput(t, client, "sudo k3s kubectl delete job external-agentpool-verifier -n iterabase-system --wait=true")
	mustSSHOutput(t, client, `sudo k3s kubectl patch agentpool external-reference-pool -n iterabase-system --type=merge -p '{"spec":{"replicas":0}}'`)
	mustSSHOutput(t, client, `for i in $(seq 1 120); do test "$(sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=external-reference-pool --no-headers 2>/dev/null | wc -l)" -eq 0 && break; sleep 2; done`)
	mustSSHOutput(t, client, fmt.Sprintf(`sudo k3s kubectl patch pv %s --type=merge -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'`, candidateShellQuote(identity)))
	mustSSHOutput(t, client, "sudo k3s kubectl delete agentpool external-reference-pool -n iterabase-system --wait=true")
	cleanupDeadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(cleanupDeadline) {
		_, pvErr := sshOutput(client, "sudo k3s kubectl get pv "+candidateShellQuote(identity))
		_, volumeErr := sshOutput(client, "sudo k3s kubectl get volumes.longhorn.io "+candidateShellQuote(volumeHandle)+" -n longhorn-system")
		if pvErr != nil && volumeErr != nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if _, err := sshOutput(client, "sudo k3s kubectl get volumes.longhorn.io "+candidateShellQuote(volumeHandle)+" -n longhorn-system"); err == nil {
		t.Fatalf("external AgentPool disposable volume %s was not reclaimed", volumeHandle)
	}
	mustSSHOutput(t, client, `sudo sh -c 'test ! -f /run/agentpool-storage-controller.pid || kill $(cat /run/agentpool-storage-controller.pid) || true'`)
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

func upgradeThreeNodeRWXCompanionStage(t *testing.T, state *rwxThreeNodeState) {
	t.Helper()
	client, err := sshDial(state.serverIP, state.privKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	backupTarget := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: v1
kind: Secret
metadata: {name: hor469-backup-credentials, namespace: longhorn-system}
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: hor469
  AWS_SECRET_ACCESS_KEY: hor469-system-backup
  AWS_ENDPOINTS: http://hor469-minio.longhorn-system.svc:9000
---
apiVersion: v1
kind: Pod
metadata: {name: hor469-minio, namespace: longhorn-system, labels: {app: hor469-minio}}
spec:
  containers:
    - name: minio
      image: docker.io/minio/minio:RELEASE.2025-09-07T16-13-09Z@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e
      args: [server, /data]
      env:
        - {name: MINIO_ROOT_USER, value: hor469}
        - {name: MINIO_ROOT_PASSWORD, value: hor469-system-backup}
      ports: [{name: s3, containerPort: 9000}]
      readinessProbe: {httpGet: {path: /minio/health/ready, port: s3}}
      volumeMounts: [{name: data, mountPath: /data}]
  volumes: [{name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: hor469-minio, namespace: longhorn-system}
spec:
  selector: {app: hor469-minio}
  ports: [{name: s3, port: 9000, targetPort: s3}]
---
apiVersion: batch/v1
kind: Job
metadata: {name: hor469-create-backup-bucket, namespace: longhorn-system}
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: docker.io/minio/mc:RELEASE.2025-08-13T08-35-41Z@sha256:a7fe349ef4bd8521fb8497f55c6042871b2ae640607cf99d9bede5e9bdf11727
          command: [/bin/sh, -ceu]
          args: ['mc alias set local http://hor469-minio.longhorn-system.svc:9000 hor469 hor469-system-backup && mc mb --ignore-existing local/hor469']
YAML`
	mustSSHOutput(t, client, backupTarget)
	mustSSHOutput(t, client, "sudo k3s kubectl wait -n longhorn-system --for=condition=Ready pod/hor469-minio --timeout=5m")
	mustSSHOutput(t, client, "sudo k3s kubectl wait -n longhorn-system --for=condition=complete job/hor469-create-backup-bucket --timeout=5m")
	mustSSHOutput(t, client, `sudo k3s kubectl patch settings.longhorn.io backup-target -n longhorn-system --type=merge -p '{"value":"s3://hor469@us-east-1/"}'`)
	mustSSHOutput(t, client, `sudo k3s kubectl patch settings.longhorn.io backup-target-credential-secret -n longhorn-system --type=merge -p '{"value":"hor469-backup-credentials"}'`)
	systemBackup := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: longhorn.io/v1beta2
kind: SystemBackup
metadata: {name: hor469-pre-1-12-upgrade, namespace: longhorn-system}
spec: {volumeBackupPolicy: disabled}
YAML`
	mustSSHOutput(t, client, systemBackup)
	deadline := time.Now().Add(10 * time.Minute)
	backupState := ""
	for time.Now().Before(deadline) {
		backupState = strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get systembackup hor469-pre-1-12-upgrade -n longhorn-system -o jsonpath='{.status.state}|{.status.version}|{.status.managerImage}'`))
		if strings.HasPrefix(backupState, "Ready|") && strings.Contains(backupState, "1.11.3") {
			break
		}
		if strings.HasPrefix(backupState, "Error|") {
			t.Fatalf("Longhorn pre-upgrade system backup failed: %s", backupState)
		}
		time.Sleep(5 * time.Second)
	}
	if !strings.HasPrefix(backupState, "Ready|") || !strings.Contains(backupState, "1.11.3") {
		t.Fatalf("Longhorn pre-upgrade system backup not Ready: %s", backupState)
	}

	remoteArchive := "/tmp/" + filepath.Base(state.archive)
	upgrade := fmt.Sprintf("sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade three-rwx-storage %s -n longhorn-system --reset-values --set storage.rwx.managedLonghorn.topology=three-node --set validation.attestationNamespace=iterabase-system --wait --timeout 65m", candidateShellQuote(remoteArchive))
	mustSSHOutput(t, client, upgrade)
	managerImage := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get daemonset longhorn-manager -n longhorn-system -o jsonpath='{.spec.template.spec.containers[0].image}'`))
	if !strings.Contains(managerImage, "v1.12.1@sha256:") {
		t.Fatalf("upgraded manager image=%q want pinned v1.12.1 digest", managerImage)
	}
	uid := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get pvc three-node-evidence -n iterabase-system -o jsonpath='{.metadata.uid}'`))
	if uid != state.pvcUID {
		t.Fatalf("Longhorn 1.11.3 to 1.12.1 upgrade replaced PVC: before=%s after=%s", state.pvcUID, uid)
	}
	reader := `cat <<'YAML' | sudo k3s kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata: {name: three-node-upgrade-reader, namespace: iterabase-system}
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
	mustSSHOutput(t, client, "sudo k3s kubectl wait -n iterabase-system --for=condition=complete job/three-node-upgrade-reader --timeout=15m")
	class := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get storageclass iterabase-rwx -o jsonpath='{.parameters.numberOfReplicas}|{.reclaimPolicy}|{.allowVolumeExpansion}'`))
	if class != "3|Retain|true" {
		t.Fatalf("upgraded three-node StorageClass=%q", class)
	}
	attestation := strings.TrimSpace(mustSSHOutput(t, client, `sudo k3s kubectl get configmap -n iterabase-system -l platform.iterabase.com/storage-conformance=true -o json | jq -r '.items[] | select(.data.storageClassName=="iterabase-rwx") | "\(.data.result)|\(.data.contractVersion)"'`))
	if !strings.Contains(attestation, "pass|HOR-469/v1") {
		t.Fatalf("post-upgrade managed conformance attestation=%q", attestation)
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
	replacement, replacementIP := provisionRWXNode(t, state, replacementName)
	state.ips = append(state.ips, replacementIP)
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
'`
	mustSSHOutput(t, replacementSSH, setup)
	prepareDedicatedRWXDisk(t, replacementSSH, state.volumeByNodeID[replacement.ID].Name)
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
	reapply := fmt.Sprintf("sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml upgrade three-rwx-storage %s -n longhorn-system --reset-values --set storage.rwx.managedLonghorn.topology=three-node --set validation.attestationNamespace=iterabase-system --wait --timeout 65m", candidateShellQuote(remoteArchive))
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

	uninstall := "sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml uninstall three-rwx-storage -n longhorn-system --wait"
	forgeBinary := buildForge(t)
	forgeConfig := writeForgeConfigSpec(t, forgeConfigSpec{
		Name: "three", Address: state.serverIP, SSHKeyPath: state.privKeyPath,
		ChartVersion: "0.3.23", ChartRelease: "three", ChartNamespace: "iterabase-system",
	})
	output, destroyErr := runForgeE(forgeBinary, t.TempDir(), "destroy", "--yes", "--config", forgeConfig)
	if destroyErr == nil {
		t.Fatalf("Forge destroy unexpectedly accepted retained managed storage:\n%s", output)
	}
	if !strings.Contains(output, "managed RWX storage uninstall refused") || !strings.Contains(output, "cluster preserved before platform teardown") {
		t.Fatalf("Forge destroy did not propagate the managed storage refusal:\n%s", output)
	}
	mustSSHOutput(t, client, "sudo k3s kubectl get node "+candidateShellQuote(state.droplets[0].Name))
	mustSSHOutput(t, client, "sudo helm --kubeconfig /etc/rancher/k3s/k3s.yaml status three-rwx-storage -n longhorn-system")
	guardLogs := mustSSHOutput(t, client, "sudo k3s kubectl logs -n longhorn-system -l app.kubernetes.io/component=storage-uninstall-guard --all-containers=true")
	if !strings.Contains(guardLogs, "refusing managed RWX uninstall") {
		t.Fatalf("managed uninstall failed without the required retained-consumer diagnosis: %s", guardLogs)
	}

	mustSSHOutput(t, client, "sudo k3s kubectl delete job three-node-writer three-node-upgrade-reader three-node-replacement-reader -n iterabase-system --ignore-not-found")
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
		for _, volume := range state.volumes {
			t.Logf("keeping dedicated three-node RWX SSD %s (%s)", volume.ID, volume.Name)
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
	for _, volume := range state.volumes {
		deadline := time.Now().Add(3 * time.Minute)
		var deleteErr error
		for time.Now().Before(deadline) {
			if _, deleteErr = state.client.Storage.DeleteVolume(state.ctx, volume.ID); deleteErr == nil {
				break
			}
			time.Sleep(5 * time.Second)
		}
		if deleteErr != nil {
			t.Errorf("delete dedicated three-node RWX SSD %s: %v", volume.ID, deleteErr)
		}
	}
}
