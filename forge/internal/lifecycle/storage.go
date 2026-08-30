package lifecycle

import (
	"context"
	"fmt"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

func agentPoolWorkspaceSpec(cfg *config.Cluster) provisioner.AgentPoolWorkspaceSpec {
	return provisioner.AgentPoolWorkspaceSpec{
		InstallName: cfg.Metadata.Name,
		Device:      cfg.Spec.AgentPoolWorkspace.Device,
	}
}

func inspectAgentPoolWorkspace(ctx context.Context, cfg *config.Cluster, p provisioner.Provisioner) (*provisioner.AgentPoolWorkspaceState, error) {
	state, err := p.InspectAgentPoolWorkspace(ctx, agentPoolWorkspaceSpec(cfg))
	if err != nil {
		return nil, fmt.Errorf("AgentPool workspace preflight: %w", err)
	}
	return state, nil
}

func reconcileAgentPoolWorkspace(ctx context.Context, cfg *config.Cluster, p provisioner.Provisioner) (*provisioner.AgentPoolWorkspaceState, error) {
	state, err := p.ReconcileAgentPoolWorkspace(ctx, agentPoolWorkspaceSpec(cfg))
	if err != nil {
		auditFail(cfg, "apply-agentpool-workspace", err)
		return nil, fmt.Errorf("AgentPool workspace reconciliation: %w", err)
	}
	return state, nil
}
