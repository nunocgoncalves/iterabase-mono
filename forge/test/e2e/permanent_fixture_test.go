package e2e

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	"golang.org/x/crypto/ssh"
)

const (
	permanentFixtureEnabledEnv           = "FORGE_E2E_PERMANENT_FIXTURE"
	permanentFixtureAddressEnv           = "FORGE_E2E_FIXTURE_ADDRESS"
	permanentFixtureSSHUserEnv           = "FORGE_E2E_FIXTURE_SSH_USER"
	permanentFixtureSSHKeyPathEnv        = "FORGE_E2E_FIXTURE_SSH_KEY_PATH"
	permanentFixtureHostKeyEnv           = "FORGE_E2E_FIXTURE_SSH_HOST_KEY"
	permanentFixtureDataStorageDeviceEnv = "FORGE_E2E_FIXTURE_DATA_STORAGE_DEVICES"
	permanentFixtureModelDeviceEnv       = "FORGE_E2E_MODEL_CACHE_DEVICE"
	permanentFixtureModelUUIDEnv         = "FORGE_E2E_MODEL_CACHE_UUID"
	permanentFixtureModelMount           = "/data/hf-cache"
	permanentFixtureHarnessStatePaths    = "/tmp/edge-overlay /tmp/forge-secrets-overlay /tmp/iterabase-release-overlay-* /tmp/iterabase-release-charts-* /tmp/control-plane-image.tar /tmp/harness-image.tar /tmp/tool-runner-image.tar /tmp/inference-gateway-image.tar /tmp/runtime-fixture-image.tar /tmp/forge-e2e-workspace-consumer.pid /tmp/forge-e2e-workspace-consumer.log"
)

var bootIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

//go:embed model-cache.json
var modelCacheAuthorityJSON []byte

type modelCacheAuthority struct {
	SchemaVersion int    `json:"schema_version"`
	ModelID       string `json:"model_id"`
	Revision      string `json:"revision"`
	WeightPath    string `json:"weight_path"`
	SHA256        string `json:"sha256"`
}

type permanentFixture struct {
	capacity          string
	address           string
	sshUser           string
	sshKeyPath        string
	sshHostKey        string
	dataStorageDevice string
	modelDevice       string
	modelUUID         string
}

func fixtureSSHUser() string {
	if user := strings.TrimSpace(os.Getenv(permanentFixtureSSHUserEnv)); user != "" {
		return user
	}
	return "forge"
}

func requirePermanentFixture(t *testing.T, capacity string) *permanentFixture {
	t.Helper()
	if os.Getenv(permanentFixtureEnabledEnv) != "true" {
		t.Fatalf("mandatory permanent %s fixture is disabled — %s must be true", capacity, permanentFixtureEnabledEnv)
	}
	values := map[string]string{
		permanentFixtureAddressEnv:           strings.TrimSpace(os.Getenv(permanentFixtureAddressEnv)),
		permanentFixtureSSHUserEnv:           strings.TrimSpace(os.Getenv(permanentFixtureSSHUserEnv)),
		permanentFixtureSSHKeyPathEnv:        strings.TrimSpace(os.Getenv(permanentFixtureSSHKeyPathEnv)),
		permanentFixtureHostKeyEnv:           strings.TrimSpace(os.Getenv(permanentFixtureHostKeyEnv)),
		permanentFixtureDataStorageDeviceEnv: strings.TrimSpace(os.Getenv(permanentFixtureDataStorageDeviceEnv)),
	}
	for name, value := range values {
		if value == "" {
			t.Fatalf("mandatory permanent %s fixture is incomplete — %s is empty", capacity, name)
		}
	}
	if !strings.HasPrefix(values[permanentFixtureDataStorageDeviceEnv], "/dev/disk/by-id/") {
		t.Fatalf("%s must be a fixed /dev/disk/by-id data-storage identity", permanentFixtureDataStorageDeviceEnv)
	}
	if _, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(values[permanentFixtureHostKeyEnv] + "\n")); err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		t.Fatalf("%s is not exactly one pinned OpenSSH host public key", permanentFixtureHostKeyEnv)
	}
	if info, err := os.Stat(values[permanentFixtureSSHKeyPathEnv]); err != nil {
		t.Fatalf("fixture-scoped SSH private key is unavailable: %v", err)
	} else if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("fixture-scoped SSH private key mode is %o, want 0600", info.Mode().Perm())
	}
	fixture := &permanentFixture{
		capacity: capacity, address: values[permanentFixtureAddressEnv], sshUser: values[permanentFixtureSSHUserEnv],
		sshKeyPath: values[permanentFixtureSSHKeyPathEnv], sshHostKey: values[permanentFixtureHostKeyEnv],
		dataStorageDevice: values[permanentFixtureDataStorageDeviceEnv],
	}
	if capacity == "gpu" {
		fixture.modelDevice = strings.TrimSpace(os.Getenv(permanentFixtureModelDeviceEnv))
		fixture.modelUUID = strings.TrimSpace(os.Getenv(permanentFixtureModelUUIDEnv))
		if err := validatePermanentGPUStorage(fixture.dataStorageDevice, fixture.modelDevice, fixture.modelUUID); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func validatePermanentGPUStorage(dataStorageDevice, modelDevice, modelUUID string) error {
	if !strings.HasPrefix(modelDevice, "/dev/disk/by-id/") || modelUUID == "" {
		return fmt.Errorf("permanent GPU model cache requires fixed %s and %s", permanentFixtureModelDeviceEnv, permanentFixtureModelUUIDEnv)
	}
	if modelDevice == dataStorageDevice {
		return fmt.Errorf("GPU model-cache device must be distinct from the Forge AgentPool data-storage device")
	}
	return nil
}

func (fixture *permanentFixture) installName() string {
	return "forge-e2e-" + fixture.capacity
}

func (fixture *permanentFixture) reset(t *testing.T, forgeBin, forgeHome string) error {
	t.Helper()
	before, err := fixture.bootID()
	if err != nil {
		return fmt.Errorf("read pre-cleanup boot ID: %w", err)
	}
	configPath := writeForgeConfigSpec(t, forgeConfigSpec{
		Name: fixture.installName(), Address: fixture.address, SSHUser: fixture.sshUser,
		SSHKeyPath: fixture.sshKeyPath, SSHHostKey: fixture.sshHostKey, DataStorageDevice: fixture.dataStorageDevice,
		GPU: fixture.capacity == "gpu",
	})
	if err := fixture.releaseDataStorageConsumers(); err != nil {
		return err
	}
	output, err := runForgeE(forgeBin, forgeHome, "destroy", "--config", configPath, "--purge-data-storage", "--reboot", "--yes")
	if err != nil {
		return fmt.Errorf("forge destroy --purge-data-storage --reboot --yes failed: %w\n%s", err, output)
	}
	after, client, err := fixture.waitForReboot(before)
	if err != nil {
		return err
	}
	client.Close()
	// SSH can become available before cloud-final has completed after a reboot.
	// Preserve the lifecycle's readiness boundary so Forge preflight never races
	// provider boot configuration on a permanent fixture. Continue on the exact
	// connection that proved readiness instead of opening a race-prone follow-up
	// handshake while the public SSH frontend is still converging.
	client, err = waitForHostReady(context.Background(), fixture.address, fixture.sshKeyPath)
	if err != nil {
		return fmt.Errorf("wait for post-reboot host readiness: %w", err)
	}
	defer client.Close()
	if err := fixture.waitForDataStorageDevice(client); err != nil {
		return err
	}
	if err := fixture.cleanHarnessState(client); err != nil {
		return err
	}
	if err := fixture.recordEvidence("lifecycle", before, after, modelCacheAuthority{}); err != nil {
		return err
	}
	if fixture.capacity == "gpu" {
		authority, err := fixture.validateModelCache(client)
		if err != nil {
			return err
		}
		if err := fixture.recordEvidence("model-cache", before, after, authority); err != nil {
			return err
		}
	}
	// Close the readiness session and hand the fixture to Forge only once the
	// post-reboot public SSH frontend has demonstrably settled. A single
	// successful readiness session is not a reliable handoff: the very next
	// handshake can still be reset while the LB frontend converges after reboot
	// (seen as apply-gpu-substrate failing with `ssh handshake failed ... reset`
	// immediately after a passing reset). Establish bounded stability evidence
	// instead of a fixed sleep before returning control to Forge.
	_ = client.Close()
	if err := fixture.waitForSSHStable(); err != nil {
		return err
	}
	t.Logf("permanent %s fixture reset: boot %s -> %s data-storage=%s", fixture.capacity, before, after, fixture.dataStorageDevice)
	return nil
}

// waitForSSHStable proves bounded post-reboot SSH stability before handing the
// fixture to Forge. It requires several consecutive fresh read-only handshakes
// (each servable end to end) spread over a short window to all succeed; any
// reset while the frontend is still converging resets the counter and re-probes
// until stability is observed or a bounded deadline expires. This is
// evidence-based (not a fixed sleep) and read-only (opens/closes idle sessions),
// so it can never double-apply a mutation.
func (fixture *permanentFixture) waitForSSHStable() error {
	const (
		window     = 90 * time.Second
		interval   = 3 * time.Second
		minSuccess = 3
	)
	deadline := time.Now().Add(window)
	consecutive := 0
	for {
		client, err := sshDial(fixture.address, fixture.sshKeyPath)
		if err == nil {
			if _, rerr := sshOutput(client, "true"); rerr == nil {
				consecutive++
				client.Close()
				if consecutive >= minSuccess {
					return nil
				}
				time.Sleep(interval)
				continue
			}
			client.Close()
		}
		consecutive = 0
		if time.Now().After(deadline) {
			return fmt.Errorf("post-reboot SSH frontend did not reach %d consecutive stable handshakes within %s (last dial err=%v)", minSuccess, window, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func (fixture *permanentFixture) releaseDataStorageConsumers() error {
	client, err := sshDial(fixture.address, fixture.sshKeyPath)
	if err != nil {
		return fmt.Errorf("connect for pre-purge claim release: %w", err)
	}
	defer client.Close()
	script := fmt.Sprintf(`sudo bash -ceu '
data_storage_device=%s
if ! command -v k3s >/dev/null 2>&1 || ! k3s kubectl get --raw=/readyz >/dev/null 2>&1; then exit 0; fi
k3s kubectl delete kustomizations.kustomize.toolkit.fluxcd.io --all -A --ignore-not-found=true --wait=true --timeout=2m || true
if k3s kubectl get crd agentpools.platform.iterabase.com >/dev/null 2>&1; then
  k3s kubectl delete agentpools.platform.iterabase.com --all -A --ignore-not-found=true --wait=true --timeout=5m
fi
namespaced_resources=$(k3s kubectl api-resources --api-group=platform.iterabase.com --namespaced=true --verbs=list,delete -o name)
while IFS= read -r resource; do
  test -n "$resource" || continue
  test "$resource" = agentpools.platform.iterabase.com && continue
  k3s kubectl delete "$resource" --all -A --ignore-not-found=true --wait=true --timeout=5m
done <<<"$namespaced_resources"
if command -v helm >/dev/null 2>&1; then
  while IFS= read -r release; do
    test -n "$release" || continue
    case "$release" in *-cert-manager|*-lvm-storage) continue ;; esac
    KUBECONFIG=/etc/rancher/k3s/k3s.yaml helm uninstall "$release" -n iterabase-system --wait --timeout 5m
  done < <(KUBECONFIG=/etc/rancher/k3s/k3s.yaml helm list -n iterabase-system -q)
fi
k3s kubectl delete jobs --all -n iterabase-system --ignore-not-found=true --wait=true --timeout=5m
while read -r namespace pod; do
  test -n "$namespace" && test -n "$pod" || continue
  k3s kubectl delete pod "$pod" -n "$namespace" --ignore-not-found=true --wait=true --timeout=5m
done < <(k3s kubectl get pods -A -o go-template="{{range .items}}{{\$namespace := .metadata.namespace}}{{\$pod := .metadata.name}}{{range .spec.volumes}}{{if .persistentVolumeClaim}}{{\$namespace}} {{\$pod}}{{\"\\n\"}}{{end}}{{end}}{{end}}" | sort -u)
if k3s kubectl get crd volumesnapshots.snapshot.storage.k8s.io >/dev/null 2>&1; then
  k3s kubectl delete volumesnapshots.snapshot.storage.k8s.io --all -A --ignore-not-found=true --wait=true --timeout=5m
fi
k3s kubectl delete pvc --all -A --ignore-not-found=true --wait=true --timeout=5m
for i in $(seq 1 150); do
  volumes=0
  if k3s kubectl get crd lvmvolumes.local.openebs.io >/dev/null 2>&1; then volumes=$((volumes + $(k3s kubectl get lvmvolumes.local.openebs.io -A --no-headers | awk "NF {n++} END {print n+0}"))); fi
  if k3s kubectl get crd lvmsnapshots.local.openebs.io >/dev/null 2>&1; then volumes=$((volumes + $(k3s kubectl get lvmsnapshots.local.openebs.io -A --no-headers | awk "NF {n++} END {print n+0}"))); fi
  lvs_count=0
  if vgs iterabase-data >/dev/null 2>&1; then lvs_count=$(lvs --noheadings --select "vg_name=iterabase-data" -o lv_name | awk "NF {n++} END {print n+0}"); fi
  data_device=$(readlink -f -- "$data_storage_device")
  kernel=$(lsblk -dnro KNAME -- "$data_device")
  holders=0
  if test -d "/sys/class/block/$kernel/holders"; then holders=$(find "/sys/class/block/$kernel/holders" -mindepth 1 -maxdepth 1 | awk "NF {n++} END {print n+0}"); fi
  test "$volumes" = 0 && test "$lvs_count" = 0 && test "$holders" = 0 && exit 0
  sleep 2
done
exit 42
'`, candidateShellQuote(fixture.dataStorageDevice))
	if output, err := sshOutput(client, script); err != nil {
		return fmt.Errorf("release platform consumers/claims before explicit data-storage purge: %w\n%s", err, output)
	}
	return nil
}

func (fixture *permanentFixture) bootID() (string, error) {
	client, err := sshDial(fixture.address, fixture.sshKeyPath)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return bootIDFromClient(client)
}

func (fixture *permanentFixture) waitForReboot(before string) (string, *ssh.Client, error) {
	deadline := time.Now().Add(8 * time.Minute)
	disconnected := false
	for time.Now().Before(deadline) {
		client, err := sshDial(fixture.address, fixture.sshKeyPath)
		if err != nil {
			disconnected = true
			time.Sleep(3 * time.Second)
			continue
		}
		bootID, bootErr := bootIDFromClient(client)
		if bootErr == nil && disconnected && bootID != before {
			return bootID, client, nil
		}
		client.Close()
		time.Sleep(2 * time.Second)
	}
	return "", nil, fmt.Errorf("permanent %s fixture did not prove SSH disconnect, reconnect, and a changed boot ID", fixture.capacity)
}

func bootIDFromClient(client *ssh.Client) (string, error) {
	output, err := sshOutput(client, "cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	bootID := strings.TrimSpace(output)
	if !bootIDPattern.MatchString(bootID) {
		return "", fmt.Errorf("host returned invalid boot ID %q", bootID)
	}
	return bootID, nil
}

func (fixture *permanentFixture) waitForDataStorageDevice(client *ssh.Client) error {
	deadline := time.Now().Add(2 * time.Minute)
	command := "test -L " + candidateShellQuote(fixture.dataStorageDevice) + " && test -b \"$(readlink -f " + candidateShellQuote(fixture.dataStorageDevice) + ")\""
	var lastErr error
	for time.Now().Before(deadline) {
		if _, lastErr = sshOutput(client, command); lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("dedicated data-storage device %s did not appear on %s: %w", fixture.dataStorageDevice, fixture.address, lastErr)
}

func (fixture *permanentFixture) cleanHarnessState(client *ssh.Client) error {
	script := fmt.Sprintf(`
rm -rf -- %s %s
! command -v k3s >/dev/null 2>&1
test ! -e /var/lib/iterabase/data-storage.receipt
! vgs iterabase-data >/dev/null 2>&1
! pvs "$(readlink -f -- %s)" >/dev/null 2>&1
data_device=$(readlink -f -- %s)
test -b "$data_device"
test -z "$(wipefs -n --noheadings --output TYPE -- "$data_device" | awk 'NF')"
test ! -e /var/lib/rancher/k3s
`, permanentFixtureHarnessStatePaths, candidateShellQuote("/var/lib/forge/overlay/"+fixture.installName()), candidateShellQuote(fixture.dataStorageDevice), candidateShellQuote(fixture.dataStorageDevice))
	if output, err := sshOutput(client, "sudo bash -ceu "+candidateShellQuote(script)); err != nil {
		return fmt.Errorf("permanent fixture clean-baseline assertion failed: %w\n%s", err, output)
	}
	return nil
}

func loadModelCacheAuthority() (modelCacheAuthority, error) {
	return decodeModelCacheAuthority(modelCacheAuthorityJSON)
}

func decodeModelCacheAuthority(data []byte) (modelCacheAuthority, error) {
	var authority modelCacheAuthority
	if err := json.Unmarshal(data, &authority); err != nil {
		return authority, fmt.Errorf("decode model-cache authority: %w", err)
	}
	if authority.SchemaVersion != 1 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(authority.Revision) ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(authority.SHA256) || authority.ModelID == "" ||
		authority.WeightPath == "" || filepath.IsAbs(authority.WeightPath) || strings.Contains(authority.WeightPath, "..") {
		return authority, fmt.Errorf("model-cache authority is incomplete")
	}
	return authority, nil
}

func (fixture *permanentFixture) validateModelCache(client *ssh.Client) (modelCacheAuthority, error) {
	authority, err := loadModelCacheAuthority()
	if err != nil {
		return authority, err
	}
	weightPath := filepath.Join(permanentFixtureModelMount, authority.WeightPath)
	script := fmt.Sprintf(`
data_device=$(readlink -f -- %s)
cache=$(readlink -f -- %s)
test -b "$data_device" && test -b "$cache" && test "$data_device" != "$cache"
source=$(findmnt -n -o SOURCE --mountpoint %s)
source=${source%%%%[*}
test "$(readlink -f -- "$source")" = "$cache"
test "$(blkid -p -s UUID -o value -- "$cache")" = %s
weight=$(readlink -f -- %s)
case "$weight" in %s/*) ;; *) exit 42 ;; esac
weight_source=$(findmnt -n -o SOURCE --target "$weight")
weight_source=${weight_source%%%%[*}
test "$(readlink -f -- "$weight_source")" = "$cache"
test "$(sha256sum -- "$weight" | awk '{print $1}')" = %s
`, candidateShellQuote(fixture.dataStorageDevice), candidateShellQuote(fixture.modelDevice), candidateShellQuote(permanentFixtureModelMount), candidateShellQuote(fixture.modelUUID), candidateShellQuote(weightPath), candidateShellQuote(permanentFixtureModelMount), candidateShellQuote(authority.SHA256))
	if output, err := sshOutput(client, "sudo bash -ceu "+candidateShellQuote(script)); err != nil {
		return authority, fmt.Errorf("GPU model-cache identity/revision/hash validation failed: %w\n%s", err, output)
	}
	return authority, nil
}

func (fixture *permanentFixture) recordEvidence(name, before, after string, authority modelCacheAuthority) error {
	hostKeyHash := sha256.Sum256([]byte(fixture.sshHostKey))
	evidence := sharede2e.FixtureEvidence{
		Name: name, Capacity: fixture.capacity, HostKeySHA256: hex.EncodeToString(hostKeyHash[:]),
		DataStorageDevice: fixture.dataStorageDevice, BootIDBefore: before, BootIDAfter: after,
	}
	if name == "model-cache" {
		evidence.ModelCacheDevice = fixture.modelDevice
		evidence.ModelCacheMount = permanentFixtureModelMount
		evidence.ModelCacheUUID = fixture.modelUUID
		evidence.ModelID = authority.ModelID
		evidence.ModelRevision = authority.Revision
		evidence.ModelContentSHA256 = authority.SHA256
	}
	return sharede2e.RecordFixtureEvidence(evidence)
}

func TestPermanentFixtureCleanupCoversTransferredRunState(t *testing.T) {
	for _, path := range []string{
		"/tmp/iterabase-release-overlay-*",
		"/tmp/iterabase-release-charts-*",
		"/tmp/control-plane-image.tar",
		"/tmp/runtime-fixture-image.tar",
	} {
		if !strings.Contains(permanentFixtureHarnessStatePaths, path) {
			t.Fatalf("permanent fixture cleanup does not cover %s", path)
		}
	}
}

func TestModelCacheAuthorityPinsImmutablePublicWeight(t *testing.T) {
	authority, err := loadModelCacheAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if authority.ModelID != "Qwen/Qwen3.5-0.8B" || authority.Revision != "2fc06364715b967f1860aea9cf38778875588b17" ||
		authority.SHA256 != "04b1c301231dd422b8860db31311ab2721511346a32cb1e079c4c4e5f1fe4696" {
		t.Fatalf("model-cache authority drifted: %+v", authority)
	}
}

func TestModelCacheAuthorityRejectsFloatingCorruptAndEscapingRecords(t *testing.T) {
	valid, err := loadModelCacheAuthority()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*modelCacheAuthority){
		"floating revision": func(authority *modelCacheAuthority) { authority.Revision = "main" },
		"corrupt hash":      func(authority *modelCacheAuthority) { authority.SHA256 = strings.Repeat("g", 64) },
		"escaping path":     func(authority *modelCacheAuthority) { authority.WeightPath = "../workspace/marker" },
	} {
		t.Run(name, func(t *testing.T) {
			authority := valid
			mutate(&authority)
			data, err := json.Marshal(authority)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeModelCacheAuthority(data); err == nil {
				t.Fatalf("invalid model-cache authority unexpectedly passed: %+v", authority)
			}
		})
	}
}

func TestPermanentGPUFixtureRejectsWorkspaceCacheSubstitution(t *testing.T) {
	workspace := "/dev/disk/by-id/workspace"
	if err := validatePermanentGPUStorage(workspace, workspace, "cache-uuid"); err == nil {
		t.Fatal("AgentPool workspace unexpectedly passed as the model-cache device")
	}
	if err := validatePermanentGPUStorage(workspace, "/dev/sdc", "cache-uuid"); err == nil {
		t.Fatal("volatile model-cache device unexpectedly passed")
	}
	if err := validatePermanentGPUStorage(workspace, "/dev/disk/by-id/model-cache", ""); err == nil {
		t.Fatal("missing model-cache UUID unexpectedly passed")
	}
}
