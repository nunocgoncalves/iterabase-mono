// Package provisioner defines the host-level k3s operations interface.
//
// Provisioner is the testability seam: the lifecycle orchestrates against this
// interface, the real implementation lives in internal/sshprovisioner, and
// tests use fakes. Lifecycle logic must never talk to SSH directly.
package provisioner

import (
	"context"
	"fmt"
)

// Fixed Platform V2 workspace identities shared by Forge and its callers.
const (
	AgentPoolWorkspaceMount        = "/var/lib/iterabase/agentpool-workspaces"
	AgentPoolWorkspaceStorageClass = "iterabase-agentpool-local-path"
	AgentPoolWorkspaceProvisioner  = "rancher.io/local-path"
)

// WorkspaceDevice is a read-only stable whole-disk choice shown by interactive
// forge init before the operator selects the destructive workspace target.
type WorkspaceDevice struct {
	Path      string
	Model     string
	Serial    string
	Transport string
	SizeBytes uint64
}

// AgentPoolWorkspaceSpec binds host reconciliation to one install and one
// persisted stable by-id device. No method may discover a replacement.
type AgentPoolWorkspaceSpec struct {
	InstallName string
	Device      string
	Filesystem  string
}

// AgentPoolWorkspaceState is bounded, non-secret device/filesystem evidence.
type AgentPoolWorkspaceState struct {
	Device         string
	Resolved       string
	Model          string
	Serial         string
	WWN            string
	SizeBytes      uint64
	Transport      string
	Filesystem     string
	FilesystemUUID string
	State          string
}

// HostState is the actual host-level state of k3s, read for reconcile.
// Node-level state (labels/taints) is applied at install time via k3s flags in
// v1 and reconciled via the API in a later version.
type HostState struct {
	Installed   bool   // k3s is installed on the host
	Version     string // k3s version, e.g. "v1.31.5+k3s1"
	ClusterCIDR string // as stored in config.yaml (comma-joined for dual-stack)
	ServiceCIDR string
	DualStack   bool
}

// PreflightResult is the read-only host readiness check outcome.
type PreflightResult struct {
	OS                     string // e.g. "Ubuntu 24.04 LTS"
	HasSudo                bool   // passwordless sudo works
	HasCurl                bool   // curl present (install script dependency)
	HasSystemd             bool   // systemd present (k3s is a systemd unit)
	Installed              bool   // k3s already installed
	HasIPv6                bool   // host has IPv6 (relevant when dualStack)
	HasNVIDIAGPU           bool   // an NVIDIA GPU is on the PCI bus (GPU preflight; S11 passthrough precondition)
	KernelHeadersInstalled bool   // /lib/modules/$(uname -r)/build/Makefile exists (matching GPU driver build headers)
	HasDKMS                bool   // dkms executable present (GPU driver build dep)
	HasGCC                 bool   // gcc executable present (GPU driver build dep)
	HasMake                bool   // make executable present (GPU driver build dep)
}

// GPUReadiness is one coherent observation of the GPU operator and the
// single-node runtime it reconciles. Ready is true only when the operator's
// conditions and live node evidence agree on the requested driver. PolicyState
// remains evidence rather than the sole authority because GPU Operator v26.3.3
// writes the legacy state and conditions separately; a lost state update can
// leave state=notReady after successful reconciliation.
type GPUReadiness struct {
	Ready                  bool
	Terminal               bool
	Reason                 string
	RequestedDriverVersion string
	PolicyCount            int
	PolicyName             string
	PolicyState            string
	ReadyCondition         string
	ErrorCondition         string
	PolicyDriverVersion    string
	NodeCount              int
	NodeName               string
	NodeReady              bool
	NodeSchedulable        bool
	NodeDriverVersion      string
	NodeUpgradeState       string
}

// String returns actionable, field-by-field readiness evidence for apply and
// audit failures. Keep the legacy policy state visible even when stronger
// current conditions and node evidence make the aggregate ready.
func (r GPUReadiness) String() string {
	return fmt.Sprintf(
		"ready=%t terminal=%t reason=%q requestedDriver=%q policyCount=%d policyName=%q policyState=%q readyCondition=%q errorCondition=%q policyDriver=%q nodeCount=%d nodeName=%q nodeReady=%t nodeSchedulable=%t nodeDriver=%q nodeUpgradeState=%q",
		r.Ready, r.Terminal, r.Reason, r.RequestedDriverVersion,
		r.PolicyCount, r.PolicyName, r.PolicyState, r.ReadyCondition,
		r.ErrorCondition, r.PolicyDriverVersion, r.NodeCount, r.NodeName,
		r.NodeReady, r.NodeSchedulable, r.NodeDriverVersion, r.NodeUpgradeState,
	)
}

// WorkspacePurger is the explicit destructive extension used only by
// `forge destroy --purge-workspace`. Keeping it separate from Provisioner makes
// ordinary lifecycle callers incapable of implying a workspace wipe.
type WorkspacePurger interface {
	// PurgeAgentPoolWorkspace revalidates the configured device and its Forge
	// receipt, then removes only that filesystem, mount, fstab, and receipt state.
	PurgeAgentPoolWorkspace(ctx context.Context, spec AgentPoolWorkspaceSpec) error
}

// Rebooter is the explicit host-reboot extension used by `forge destroy
// --reboot`. Reboot is never implied by destroy or workspace purge.
type Rebooter interface {
	Reboot(ctx context.Context) error
}

// Provisioner abstracts host-level k3s operations. One instance is bound to a
// single host at construction time (the SSH user/key/address).
type Provisioner interface {
	// Preflight runs read-only readiness checks against the host.
	Preflight(ctx context.Context) (*PreflightResult, error)
	// Install verifies and installs the reviewed k3s executable and airgap image
	// archive before the pinned service installer may start the given version.
	Install(ctx context.Context, version string, serverArgs []string) error
	// Upgrade upgrades k3s in-place through the same reviewed content path.
	Upgrade(ctx context.Context, version string, serverArgs []string) error
	// Uninstall runs k3s-uninstall.sh on the host.
	Uninstall(ctx context.Context) error
	// FetchKubeconfig reads /etc/rancher/k3s/k3s.yaml from the host.
	FetchKubeconfig(ctx context.Context) ([]byte, error)
	// ReadState reads the actual host-level k3s state for reconcile.
	ReadState(ctx context.Context) (*HostState, error)
	// NodeReady reports whether the cluster node is Ready (via remote k3s kubectl).
	NodeReady(ctx context.Context) (bool, error)
	// EnsureDriverBuildDeps ensures the host can compile the NVIDIA kernel module
	// via the GPU operator's driver container (installs matching linux-headers,
	// build-essential, and dkms on Ubuntu). Idempotent. Only called when GPU is enabled.
	EnsureDriverBuildDeps(ctx context.Context) error
	// ListAgentPoolWorkspaceDevices returns stable non-removable whole-disk
	// identities for the interactive init selection. It is strictly read-only.
	ListAgentPoolWorkspaceDevices(ctx context.Context) ([]WorkspaceDevice, error)
	// InspectAgentPoolWorkspace runs the complete read-only identity/topology/
	// in-use/partition/signature preflight used by forge apply --dry-run.
	InspectAgentPoolWorkspace(ctx context.Context, spec AgentPoolWorkspaceSpec) (*AgentPoolWorkspaceState, error)
	// EnsureAgentPoolWorkspaceTools installs/checks the formatter tooling needed
	// by the already-resolved ext4/XFS choice. It never touches the selected disk.
	EnsureAgentPoolWorkspaceTools(ctx context.Context, filesystem string) error
	// ReconcileAgentPoolWorkspace repeats every required probe immediately before
	// the first format, then crash-resumably reconciles type, UUID, label, fstab,
	// mount, and filesystem marker. It never adopts or selects another device.
	ReconcileAgentPoolWorkspace(ctx context.Context, spec AgentPoolWorkspaceSpec) (*AgentPoolWorkspaceState, error)
	// EnsureAgentPoolLocalPathStorage configures K3s's bundled provisioner with
	// separate fixed default and AgentPool class paths and validates both classes.
	EnsureAgentPoolLocalPathStorage(ctx context.Context) error
	// ReadGPUReadiness returns one coherent ClusterPolicy/node observation,
	// evaluated against the requested driver. Missing resources and transitional
	// states return Ready=false; query/parse failures return an error. Polled as
	// the GPU readiness gate.
	ReadGPUReadiness(ctx context.Context, requestedDriverVersion string) (*GPUReadiness, error)
}
