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
	permanentFixtureEnabledEnv         = "FORGE_E2E_PERMANENT_FIXTURE"
	permanentFixtureAddressEnv         = "FORGE_E2E_FIXTURE_ADDRESS"
	permanentFixtureSSHUserEnv         = "FORGE_E2E_FIXTURE_SSH_USER"
	permanentFixtureSSHKeyPathEnv      = "FORGE_E2E_FIXTURE_SSH_KEY_PATH"
	permanentFixtureHostKeyEnv         = "FORGE_E2E_FIXTURE_SSH_HOST_KEY"
	permanentFixtureWorkspaceDeviceEnv = "FORGE_E2E_FIXTURE_WORKSPACE_DEVICE"
	permanentFixtureModelDeviceEnv     = "FORGE_E2E_MODEL_CACHE_DEVICE"
	permanentFixtureModelUUIDEnv       = "FORGE_E2E_MODEL_CACHE_UUID"
	permanentFixtureModelMount         = "/data/hf-cache"
	permanentGPUVendorID               = "0x10de"
	permanentGPUDeviceID               = "0x24b0"
	permanentGPUClass                  = "0x030000"
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
	capacity        string
	address         string
	sshUser         string
	sshKeyPath      string
	sshHostKey      string
	workspaceDevice string
	modelDevice     string
	modelUUID       string
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
		permanentFixtureAddressEnv:         strings.TrimSpace(os.Getenv(permanentFixtureAddressEnv)),
		permanentFixtureSSHUserEnv:         strings.TrimSpace(os.Getenv(permanentFixtureSSHUserEnv)),
		permanentFixtureSSHKeyPathEnv:      strings.TrimSpace(os.Getenv(permanentFixtureSSHKeyPathEnv)),
		permanentFixtureHostKeyEnv:         strings.TrimSpace(os.Getenv(permanentFixtureHostKeyEnv)),
		permanentFixtureWorkspaceDeviceEnv: strings.TrimSpace(os.Getenv(permanentFixtureWorkspaceDeviceEnv)),
	}
	for name, value := range values {
		if value == "" {
			t.Fatalf("mandatory permanent %s fixture is incomplete — %s is empty", capacity, name)
		}
	}
	if !strings.HasPrefix(values[permanentFixtureWorkspaceDeviceEnv], "/dev/disk/by-id/") {
		t.Fatalf("%s must be a fixed /dev/disk/by-id identity", permanentFixtureWorkspaceDeviceEnv)
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
		workspaceDevice: values[permanentFixtureWorkspaceDeviceEnv],
	}
	if capacity == "gpu" {
		fixture.modelDevice = strings.TrimSpace(os.Getenv(permanentFixtureModelDeviceEnv))
		fixture.modelUUID = strings.TrimSpace(os.Getenv(permanentFixtureModelUUIDEnv))
		if err := validatePermanentGPUStorage(fixture.workspaceDevice, fixture.modelDevice, fixture.modelUUID); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func validatePermanentGPUStorage(workspaceDevice, modelDevice, modelUUID string) error {
	if !strings.HasPrefix(modelDevice, "/dev/disk/by-id/") || modelUUID == "" {
		return fmt.Errorf("permanent GPU model cache requires fixed %s and %s", permanentFixtureModelDeviceEnv, permanentFixtureModelUUIDEnv)
	}
	if modelDevice == workspaceDevice {
		return fmt.Errorf("GPU model-cache device must be distinct from the Forge AgentPool workspace device")
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
		SSHKeyPath: fixture.sshKeyPath, SSHHostKey: fixture.sshHostKey, WorkspaceDevice: fixture.workspaceDevice,
		GPU: fixture.capacity == "gpu",
	})
	output, err := runForgeE(forgeBin, forgeHome, "destroy", "--config", configPath, "--purge-workspace", "--reboot", "--yes")
	if err != nil {
		return fmt.Errorf("forge destroy --purge-workspace --reboot --yes failed: %w\n%s", err, output)
	}
	after, client, err := fixture.waitForReboot(before)
	if err != nil {
		return err
	}
	client.Close()
	// SSH can become available before cloud-final has completed after a reboot.
	// Preserve the lifecycle's readiness boundary so Forge preflight never races
	// provider boot configuration on a permanent fixture.
	if err := waitForHostReady(context.Background(), fixture.address, fixture.sshKeyPath); err != nil {
		return fmt.Errorf("wait for post-reboot host readiness: %w", err)
	}
	// The readiness probe closes its connection. Leave the same bounded quiet
	// window before opening the clean-baseline verification session; some public
	// SSH frontends reset an otherwise valid immediate follow-up handshake.
	time.Sleep(5 * time.Second)
	client, err = sshDial(fixture.address, fixture.sshKeyPath)
	if err != nil {
		return fmt.Errorf("reconnect after post-reboot host readiness: %w", err)
	}
	defer client.Close()
	if err := fixture.waitForWorkspaceDevice(client); err != nil {
		return err
	}
	if fixture.capacity == "gpu" {
		if err := fixture.waitForGPUDevice(client); err != nil {
			return err
		}
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
	// Close the readiness session and leave a short quiet window before handing
	// the fixture to Forge. Public SSH frontends can reset an immediate next
	// handshake even after cloud-final and strict pinned probes have succeeded.
	_ = client.Close()
	time.Sleep(5 * time.Second)
	t.Logf("permanent %s fixture reset: boot %s -> %s workspace=%s", fixture.capacity, before, after, fixture.workspaceDevice)
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

func (fixture *permanentFixture) waitForWorkspaceDevice(client *ssh.Client) error {
	deadline := time.Now().Add(2 * time.Minute)
	command := "test -L " + candidateShellQuote(fixture.workspaceDevice) + " && test -b \"$(readlink -f " + candidateShellQuote(fixture.workspaceDevice) + ")\""
	var lastErr error
	for time.Now().Before(deadline) {
		if _, lastErr = sshOutput(client, command); lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("dedicated workspace device %s did not appear on %s: %w", fixture.workspaceDevice, fixture.address, lastErr)
}

func (fixture *permanentFixture) waitForGPUDevice(client *ssh.Client) error {
	deadline := time.Now().Add(20 * time.Minute)
	command := `for device in /sys/bus/pci/devices/*; do
	vendor=$(cat "$device/vendor" 2>/dev/null) || continue
	test "$vendor" = 0x10de || continue
	printf '%s %s %s\n' "$vendor" "$(cat "$device/device")" "$(cat "$device/class")"
done`
	var lastOutput string
	var lastErr error
	for time.Now().Before(deadline) {
		lastOutput, lastErr = sshOutput(client, command)
		if lastErr == nil {
			lastErr = validatePermanentGPUDevice(lastOutput)
			if lastErr == nil {
				return nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("pinned permanent GPU PCI device did not become ready: observed=%q: %w", strings.TrimSpace(lastOutput), lastErr)
}

func validatePermanentGPUDevice(output string) error {
	want := strings.Join([]string{permanentGPUVendorID, permanentGPUDeviceID, permanentGPUClass}, " ")
	if strings.TrimSpace(output) != want {
		return fmt.Errorf("GPU PCI identity mismatch: got %q, want exactly %q", strings.TrimSpace(output), want)
	}
	return nil
}

func (fixture *permanentFixture) cleanHarnessState(client *ssh.Client) error {
	script := fmt.Sprintf(`
rm -rf -- /tmp/edge-overlay /tmp/forge-secrets-overlay /tmp/iterabase-release-overlay-* /tmp/forge-e2e-workspace-consumer.pid /tmp/forge-e2e-workspace-consumer.log %s
! command -v k3s >/dev/null 2>&1
test ! -e /var/lib/iterabase/agentpool-workspace.receipt
! findmnt --mountpoint /var/lib/iterabase/agentpool-workspaces >/dev/null 2>&1
test "$(awk '!/^#/ && NF && $2 == "/var/lib/iterabase/agentpool-workspaces" {n++} END {print n+0}' /etc/fstab)" = 0
workspace=$(readlink -f -- %s)
test -b "$workspace"
test -z "$(wipefs -n --noheadings --output TYPE -- "$workspace" | awk 'NF')"
test ! -e /var/lib/rancher/k3s
`, candidateShellQuote("/var/lib/forge/overlay/"+fixture.installName()), candidateShellQuote(fixture.workspaceDevice))
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
workspace=$(readlink -f -- %s)
cache=$(readlink -f -- %s)
test -b "$workspace" && test -b "$cache" && test "$workspace" != "$cache"
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
`, candidateShellQuote(fixture.workspaceDevice), candidateShellQuote(fixture.modelDevice), candidateShellQuote(permanentFixtureModelMount), candidateShellQuote(fixture.modelUUID), candidateShellQuote(weightPath), candidateShellQuote(permanentFixtureModelMount), candidateShellQuote(authority.SHA256))
	if output, err := sshOutput(client, "sudo bash -ceu "+candidateShellQuote(script)); err != nil {
		return authority, fmt.Errorf("GPU model-cache identity/revision/hash validation failed: %w\n%s", err, output)
	}
	return authority, nil
}

func (fixture *permanentFixture) recordEvidence(name, before, after string, authority modelCacheAuthority) error {
	hostKeyHash := sha256.Sum256([]byte(fixture.sshHostKey))
	evidence := sharede2e.FixtureEvidence{
		Name: name, Capacity: fixture.capacity, HostKeySHA256: hex.EncodeToString(hostKeyHash[:]),
		WorkspaceDevice: fixture.workspaceDevice, BootIDBefore: before, BootIDAfter: after,
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

func TestPermanentGPUFixturePinsExactlyOnePCIDevice(t *testing.T) {
	valid := permanentGPUVendorID + " " + permanentGPUDeviceID + " " + permanentGPUClass + "\n"
	if err := validatePermanentGPUDevice(valid); err != nil {
		t.Fatalf("pinned GPU identity rejected: %v", err)
	}
	for name, output := range map[string]string{
		"missing":  "",
		"wrong":    permanentGPUVendorID + " 0xffff " + permanentGPUClass,
		"multiple": valid + valid,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePermanentGPUDevice(output); err == nil {
				t.Fatal("unexpected GPU PCI identity passed")
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
