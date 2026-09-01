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

func permanentFixtureEnabled() bool {
	return os.Getenv(permanentFixtureEnabledEnv) == "true"
}

func fixtureSSHUser() string {
	if user := strings.TrimSpace(os.Getenv(permanentFixtureSSHUserEnv)); user != "" {
		return user
	}
	return "forge"
}

func requirePermanentFixture(t *testing.T, capacity string) *permanentFixture {
	t.Helper()
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
	after, err := fixture.waitForReboot(before)
	if err != nil {
		return err
	}
	if err := waitForWorkspaceDevice(context.Background(), fixture.address, fixture.sshKeyPath, fixture.workspaceDevice); err != nil {
		return err
	}
	if err := fixture.cleanHarnessState(); err != nil {
		return err
	}
	if err := fixture.recordEvidence("lifecycle", before, after, modelCacheAuthority{}); err != nil {
		return err
	}
	if fixture.capacity == "gpu" {
		authority, err := fixture.validateModelCache()
		if err != nil {
			return err
		}
		if err := fixture.recordEvidence("model-cache", before, after, authority); err != nil {
			return err
		}
	}
	t.Logf("permanent %s fixture reset: boot %s -> %s workspace=%s", fixture.capacity, before, after, fixture.workspaceDevice)
	return nil
}

func (fixture *permanentFixture) bootID() (string, error) {
	client, err := sshDial(fixture.address, fixture.sshKeyPath)
	if err != nil {
		return "", err
	}
	defer client.Close()
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

func (fixture *permanentFixture) waitForReboot(before string) (string, error) {
	deadline := time.Now().Add(8 * time.Minute)
	disconnected := false
	for time.Now().Before(deadline) {
		bootID, err := fixture.bootID()
		if err != nil {
			disconnected = true
			time.Sleep(3 * time.Second)
			continue
		}
		if disconnected && bootID != before {
			return bootID, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("permanent %s fixture did not prove SSH disconnect, reconnect, and a changed boot ID", fixture.capacity)
}

func (fixture *permanentFixture) cleanHarnessState() error {
	client, err := sshDial(fixture.address, fixture.sshKeyPath)
	if err != nil {
		return err
	}
	defer client.Close()
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

func (fixture *permanentFixture) validateModelCache() (modelCacheAuthority, error) {
	authority, err := loadModelCacheAuthority()
	if err != nil {
		return authority, err
	}
	client, err := sshDial(fixture.address, fixture.sshKeyPath)
	if err != nil {
		return authority, err
	}
	defer client.Close()
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
		authority.SHA256 != "f0140d845aced424f17b1c75ebc5a67ef75fe309c68d2f613acda2eb551db7dd" {
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
