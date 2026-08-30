package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/lifecycle"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/sshprovisioner"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cluster health and drift against forge.yaml",
		RunE:  runStatus,
	}
}

func runStatus(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	p, err := sshprovisioner.New(cfg.Spec.Hosts[0])
	if err != nil {
		return err
	}
	defer p.Close()

	ctx := context.Background()
	plan, err := lifecycle.Plan(ctx, cfg, p)
	if err != nil {
		return err
	}
	ready, _ := p.NodeReady(ctx)

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "install:    %s\n", cfg.Metadata.Name)
	fmt.Fprintf(out, "installed:  %v\n", plan.Preflight.Installed)
	fmt.Fprintf(out, "action:     %s\n", plan.Action)
	if plan.Reason != "" {
		fmt.Fprintf(out, "reason:     %s\n", plan.Reason)
	}
	if plan.HaveVersion != "" {
		fmt.Fprintf(out, "have:       %s\n", plan.HaveVersion)
	}
	fmt.Fprintf(out, "want:       %s\n", plan.WantVersion)
	fmt.Fprintf(out, "node ready: %v\n", ready)
	printWorkspaceStatus(out, plan.AgentPoolWorkspace)
	if cfg.Spec.Chart.Version != "" {
		cs, _ := p.Status(ctx, cfg.Spec.Chart.Release, cfg.Spec.Chart.Namespace)
		if cs != nil && cs.Installed {
			fmt.Fprintf(out, "chart:      %s (%s)\n", cs.Version, cs.Status)
		} else {
			fmt.Fprintf(out, "chart:      not installed (want %s)\n", cfg.Spec.Chart.Version)
		}
	}
	if cfg.Spec.GPU.Enabled {
		g := cfg.Spec.GPU.Operator
		fmt.Fprintln(out, "gpu:")
		fmt.Fprintf(out, "  enabled:       true\n")
		fmt.Fprintf(out, "  pci present:   %v\n", plan.Preflight.HasNVIDIAGPU)
		fmt.Fprintf(out, "  headers:       %s\n", boolLabel(plan.Preflight.KernelHeadersInstalled, "installed", "absent"))
		if cs, _ := p.Status(ctx, g.Release, g.Namespace); cs != nil && cs.Installed {
			fmt.Fprintf(out, "  operator:      %s (%s)\n", g.Version, cs.Status)
		} else {
			fmt.Fprintf(out, "  operator:      not installed (want %s)\n", g.Version)
		}
		if v := cfg.Spec.GPU.Driver.Version; v != "" {
			fmt.Fprintf(out, "  driver:        %s\n", v)
		} else {
			fmt.Fprintf(out, "  driver:        (chart default)\n")
		}
		readiness, readinessErr := p.ReadGPUReadiness(ctx, cfg.Spec.GPU.Driver.Version)
		if readinessErr != nil {
			fmt.Fprintln(out, "  clusterpolicy: unavailable")
			fmt.Fprintf(out, "  evidence:      %v\n", readinessErr)
		} else {
			fmt.Fprintf(out, "  clusterpolicy: %s\n", boolLabel(readiness.Ready, "ready", "notReady"))
			fmt.Fprintf(out, "  evidence:      %s\n", readiness)
		}
	}
	return nil
}

func printWorkspaceStatus(out io.Writer, workspace *provisioner.AgentPoolWorkspaceState) {
	if workspace == nil {
		return
	}
	fmt.Fprintf(out, "workspace:  %s (%s)\n", workspace.Device, workspace.State)
	fmt.Fprintf(out, "  resolved: %s\n", workspace.Resolved)
	fmt.Fprintf(out, "  model:    %s\n", workspace.Model)
	fmt.Fprintf(out, "  serial:   %s\n", workspace.Serial)
	fmt.Fprintf(out, "  size:     %d\n", workspace.SizeBytes)
	if workspace.FilesystemUUID != "" {
		fmt.Fprintf(out, "  uuid:     %s\n", workspace.FilesystemUUID)
	}
}

func boolLabel(b bool, on, off string) string {
	if b {
		return on
	}
	return off
}
