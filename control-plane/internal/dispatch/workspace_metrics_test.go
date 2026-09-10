package dispatch

import (
	"testing"

	cpmetrics "github.com/nunocgoncalves/iterabase-mono/control-plane/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestDeletedPoolRemovesEveryWorkspaceMetricSeries(t *testing.T) {
	metrics := cpmetrics.New("dispatch", "test", "test")
	service := &Service{metrics: metrics}
	service.observeWorkspaceMetrics(WorkspaceCapacityState{
		PoolID: "pool-deleted", Observed: true, FreeBytes: 20, CapacityBytes: 100,
		FreeRatio: 0.20, Warning: true, CreditGated: true,
	})
	for _, name := range workspaceMetricNames() {
		require.True(t, metricHasPool(t, metrics, name, "pool-deleted"), "%s must expose the active pool", name)
	}
	service.deleteWorkspaceMetrics("pool-deleted")
	for _, name := range workspaceMetricNames() {
		require.False(t, metricHasPool(t, metrics, name, "pool-deleted"), "%s retained a deleted pool label", name)
	}
}

func workspaceMetricNames() []string {
	return []string{
		"control_plane_dispatch_workspace_free_bytes",
		"control_plane_dispatch_workspace_capacity_bytes",
		"control_plane_dispatch_workspace_free_ratio",
		"control_plane_dispatch_workspace_capacity_warning",
		"control_plane_dispatch_workspace_credit_gated",
	}
}

func metricHasPool(t *testing.T, metrics *cpmetrics.Metrics, name, pool string) bool {
	t.Helper()
	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "pool" && label.GetValue() == pool {
					return true
				}
			}
		}
	}
	return false
}
