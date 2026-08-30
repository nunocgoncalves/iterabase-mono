package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/sshprovisioner"
)

const agentPoolWorkspaceDeviceEnv = "FORGE_AGENTPOOL_WORKSPACE_DEVICE"

var discoverAgentPoolWorkspaceDevices = func(ctx context.Context, host config.Host) ([]provisioner.WorkspaceDevice, error) {
	p, err := sshprovisioner.New(host)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	return p.ListAgentPoolWorkspaceDevices(ctx)
}

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a forge.yaml config",
		Long:  "Generate a forge.yaml substrate config, interactively or from flags (--non-interactive).",
		RunE:  runInit,
	}
	cmd.Flags().Bool("non-interactive", false, "generate without prompts using flags")
	cmd.Flags().String("path", "forge.yaml", "output path for the generated config")
	cmd.Flags().String("name", "opo1", "install name")
	cmd.Flags().String("address", "", "target host address")
	cmd.Flags().String("ssh-user", "forge", "SSH user (must have passwordless sudo)")
	cmd.Flags().String("ssh-key", "~/.ssh/forge_ed25519", "SSH key path")
	cmd.Flags().String("k3s-version", "v1.34.10+k3s1", "K3s version (full tag, e.g. v1.34.10+k3s1)")
	cmd.Flags().Bool("dual-stack", true, "enable dual-stack IPv4+IPv6")
	cmd.Flags().String("overlay", "", "overlay repo URL (client fork; https:// or file://; empty => no overlay)")
	cmd.Flags().String("overlay-ref", "master", "overlay ref (branch or tag)")
	cmd.Flags().String("agentpool-workspace-device", "", "stable whole disk /dev/disk/by-id/... selected for AgentPool workspaces")
	cmd.Flags().Bool("overwrite", false, "overwrite an existing config file (does not authorize disk changes)")
	return cmd
}

func runInit(cmd *cobra.Command, _ []string) error {
	nonInteractive, _ := cmd.Flags().GetBool("non-interactive")
	path, _ := cmd.Flags().GetString("path")
	overwrite, _ := cmd.Flags().GetBool("overwrite")

	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; use --overwrite to replace the config file", path)
		}
	}

	name, _ := cmd.Flags().GetString("name")
	address, _ := cmd.Flags().GetString("address")
	sshUser, _ := cmd.Flags().GetString("ssh-user")
	sshKey, _ := cmd.Flags().GetString("ssh-key")
	k3sVersion, _ := cmd.Flags().GetString("k3s-version")
	dualStack, _ := cmd.Flags().GetBool("dual-stack")
	overlay, _ := cmd.Flags().GetString("overlay")
	overlayRef, _ := cmd.Flags().GetString("overlay-ref")
	flagDevice, _ := cmd.Flags().GetString("agentpool-workspace-device")
	envDevice := strings.TrimSpace(os.Getenv(agentPoolWorkspaceDeviceEnv))
	workspaceDevice, err := resolveWorkspaceDeviceSources(strings.TrimSpace(flagDevice), envDevice)
	if err != nil {
		return err
	}

	in := bufio.NewReader(cmd.InOrStdin())
	if !nonInteractive {
		name = prompt(in, "Install name", name)
		address = prompt(in, "Target host address", address)
		sshUser = prompt(in, "SSH user", sshUser)
		sshKey = prompt(in, "SSH key path", sshKey)
		k3sVersion = prompt(in, "K3s version", k3sVersion)
		overlay = prompt(in, "Overlay repo URL (optional)", overlay)
	}
	if address == "" {
		return fmt.Errorf("address is required")
	}

	host := config.Host{
		Address: address, SSHUser: sshUser, SSHKeyPath: sshKey,
		Role: config.RoleControlPlaneWorker, Labels: map[string]string{}, Taints: []config.Taint{},
	}
	if workspaceDevice == "" {
		if nonInteractive {
			return fmt.Errorf("--agentpool-workspace-device or %s is required in non-interactive mode", agentPoolWorkspaceDeviceEnv)
		}
		devices, err := discoverAgentPoolWorkspaceDevices(context.Background(), host)
		if err != nil {
			return fmt.Errorf("discover AgentPool workspace disks on %s: %w", address, err)
		}
		workspaceDevice, err = selectAgentPoolWorkspaceDevice(in, cmd.ErrOrStderr(), devices)
		if err != nil {
			return err
		}
	}

	cfg := &config.Cluster{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		Metadata:   config.Metadata{Name: name},
		Spec: config.Spec{
			Mode:               config.ModeSingleNode,
			Hosts:              []config.Host{host},
			AgentPoolWorkspace: config.AgentPoolWorkspace{Device: workspaceDevice},
			K3s: config.K3s{
				Version:       k3sVersion,
				ClusterCIDR:   "10.42.0.0/16",
				ServiceCIDR:   "10.43.0.0/16",
				DualStack:     dualStack,
				ClusterCIDRv6: "fd42::/48",
				ServiceCIDRv6: "fd43::/112",
				Disable:       []string{"traefik", "servicelb"},
			},
		},
	}
	if overlay != "" {
		if overlayRef == "" {
			overlayRef = config.DefaultOverlayRef
		}
		cfg.Spec.Overlay = config.Overlay{Repo: overlay, Ref: overlayRef}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
	return nil
}

func resolveWorkspaceDeviceSources(flagDevice, envDevice string) (string, error) {
	if flagDevice != "" && envDevice != "" && flagDevice != envDevice {
		return "", fmt.Errorf("conflicting AgentPool workspace devices: --agentpool-workspace-device=%q and %s=%q", flagDevice, agentPoolWorkspaceDeviceEnv, envDevice)
	}
	if flagDevice != "" {
		return flagDevice, nil
	}
	return envDevice, nil
}

func selectAgentPoolWorkspaceDevice(in *bufio.Reader, out io.Writer, devices []provisioner.WorkspaceDevice) (string, error) {
	if len(devices) == 0 {
		return "", fmt.Errorf("no stable non-removable whole disks were discovered; Forge never falls back to the root disk")
	}
	fmt.Fprintln(out, "Select exactly one dedicated AgentPool workspace disk.")
	fmt.Fprintf(out, "Forge will format the selected whole disk as ext4 and mount it at %s after fail-closed safety checks.\n", provisioner.AgentPoolWorkspaceMount)
	fmt.Fprintln(out, "This selection is the sole destructive authorization; there is no later confirmation or override.")
	for i, device := range devices {
		fmt.Fprintf(out, "  %d) %s  model=%q serial=%q size=%s\n", i+1, device.Path, device.Model, device.Serial, formatDeviceSize(device.SizeBytes))
	}
	choice := prompt(in, "Workspace disk number", "")
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(devices) {
		return "", fmt.Errorf("workspace disk selection %q is invalid; choose one displayed number", choice)
	}
	return devices[index-1].Path, nil
}

func formatDeviceSize(size uint64) string {
	const gib = uint64(1024 * 1024 * 1024)
	if size >= gib {
		return fmt.Sprintf("%.1f GiB", float64(size)/float64(gib))
	}
	return fmt.Sprintf("%d bytes", size)
}

func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}
