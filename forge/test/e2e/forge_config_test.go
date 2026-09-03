package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// forgeConfigSpec is the shared config fixture for cloud E2E stages. Scenario
// writers vary only the fields relevant to the contract they exercise, so the
// common k3s/host recipe cannot drift across files.
const workspaceDeviceEnv = "FORGE_E2E_AGENTPOOL_WORKSPACE_DEVICE"

var workspaceDevicesByAddress sync.Map

func rememberWorkspaceDevice(address, device string) {
	if address != "" && device != "" {
		workspaceDevicesByAddress.Store(address, device)
	}
}

type forgeConfigSpec struct {
	Name             string
	Address          string
	SSHUser          string
	SSHKeyPath       string
	SSHHostKey       string
	WorkspaceDevice  string
	RunLabel         bool
	DualStack        bool
	GPU              bool
	GPUDriverVersion string
	GPUDriverSHA256  string
	ChartVersion     string
	ChartRepository  string
	ChartRelease     string
	ChartNamespace   string
	OverlayRepo      string
	OverlayRef       string
	Flux             bool
}

func e2eK3sVersion() string { return "v1.34.10+k3s1" }

func workspaceDevice(spec forgeConfigSpec) string {
	if spec.WorkspaceDevice != "" {
		return spec.WorkspaceDevice
	}
	if device := os.Getenv(workspaceDeviceEnv); device != "" {
		return device
	}
	if device, ok := workspaceDevicesByAddress.Load(spec.Address); ok {
		return device.(string)
	}
	return "/dev/disk/by-id/scsi-forge-e2e-workspaces"
}

func writeForgeConfigSpec(t *testing.T, spec forgeConfigSpec) string {
	t.Helper()
	if spec.SSHUser == "" {
		spec.SSHUser = fixtureSSHUser()
	}
	if spec.SSHHostKey == "" {
		spec.SSHHostKey = strings.TrimSpace(os.Getenv(permanentFixtureHostKeyEnv))
	}
	var cfg strings.Builder
	fmt.Fprintf(&cfg, `apiVersion: forge.horizonshift.io/v1alpha1
kind: Cluster
metadata:
  name: %s
spec:
  mode: single-node
  agentPoolWorkspace:
    device: %s
    filesystem: auto
  hosts:
    - address: %s
      sshUser: %s
      sshKeyPath: %s
`, spec.Name, workspaceDevice(spec), spec.Address, spec.SSHUser, spec.SSHKeyPath)
	if spec.SSHHostKey != "" {
		fmt.Fprintf(&cfg, "      sshHostKey: %q\n", spec.SSHHostKey)
	}
	cfg.WriteString("      role: control-plane+worker\n")
	if spec.RunLabel {
		fmt.Fprintf(&cfg, `      labels:
        e2e.horizonshift.io/run: %q
`, spec.Name)
	}
	fmt.Fprintf(&cfg, `  k3s:
    version: %s
    clusterCIDR: 10.42.0.0/16
    serviceCIDR: 10.43.0.0/16
    dualStack: %t
`, e2eK3sVersion(), spec.DualStack)
	if spec.DualStack {
		cfg.WriteString(`    clusterCIDRv6: fd42::/48
    serviceCIDRv6: fd43::/112
    disable: [traefik, servicelb]
`)
	}
	if spec.GPU {
		if spec.GPUDriverVersion == "" {
			spec.GPUDriverVersion = gpuUpgradeBaselineDriver
		}
		if spec.GPUDriverSHA256 == "" {
			spec.GPUDriverSHA256 = e2eGPUDriverSHA256(t, spec.GPUDriverVersion)
		}
		fmt.Fprintf(&cfg, `  gpu:
    enabled: true
    driver:
      version: %q
      sha256: %s
`, spec.GPUDriverVersion, spec.GPUDriverSHA256)
	}
	if spec.ChartVersion != "" {
		fmt.Fprintf(&cfg, `  chart:
    version: %s
`, spec.ChartVersion)
		if spec.ChartRepository != "" {
			fmt.Fprintf(&cfg, "    repository: %s\n", spec.ChartRepository)
		}
		if spec.ChartRelease != "" {
			fmt.Fprintf(&cfg, "    release: %s\n", spec.ChartRelease)
		}
		if spec.ChartNamespace != "" {
			fmt.Fprintf(&cfg, "    namespace: %s\n", spec.ChartNamespace)
		}
	}
	if spec.OverlayRepo != "" {
		fmt.Fprintf(&cfg, `  overlay:
    repo: %s
    ref: %s
`, spec.OverlayRepo, spec.OverlayRef)
	}
	if spec.Flux {
		cfg.WriteString(`  flux:
    enabled: true
    version: "v2.4.0"
`)
	}

	path := filepath.Join(t.TempDir(), "forge.yaml")
	if err := os.WriteFile(path, []byte(cfg.String()), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
