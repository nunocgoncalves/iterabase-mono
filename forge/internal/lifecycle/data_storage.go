package lifecycle

import (
	"context"
	"fmt"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

func dataStorageSpec(cfg *config.Cluster) provisioner.DataStorageSpec {
	return provisioner.DataStorageSpec{
		InstallName: cfg.Metadata.Name,
		Devices:     append([]string(nil), cfg.Spec.DataStorage.Devices...),
	}
}

func inspectDataStorage(ctx context.Context, cfg *config.Cluster, p provisioner.Provisioner) (*provisioner.DataStorageState, error) {
	state, err := p.InspectDataStorage(ctx, dataStorageSpec(cfg))
	if err != nil {
		return nil, fmt.Errorf("data-storage preflight: %w", err)
	}
	return state, nil
}

func reconcileDataStorage(ctx context.Context, cfg *config.Cluster, p provisioner.Provisioner) (*provisioner.DataStorageState, error) {
	if err := p.EnsureDataStorageTools(ctx); err != nil {
		auditFail(cfg, "apply-data-storage-tools", err)
		return nil, fmt.Errorf("data-storage tooling: %w", err)
	}
	state, err := p.ReconcileDataStorage(ctx, dataStorageSpec(cfg))
	if err != nil {
		auditFail(cfg, "apply-data-storage", err)
		return nil, fmt.Errorf("data-storage reconciliation: %w", err)
	}
	return state, nil
}
