package e2e

import (
	"os"
	"testing"
)

const (
	// PR-gating tests use a supplied monorepo-local chart when available and
	// otherwise fall back to pinned released baselines. Explicit version overrides
	// select a published chart. Baseline upgrades are intentional changes to this
	// file, not an unrelated release silently changing another PR's test matrix.
	pinnedPlatformChartVersion     = "0.3.1"
	pinnedControlPlaneChartVersion = "0.4.1"

	// The CPU cloud scenario starts here only to prove the real ownership handoff
	// into pinnedPlatformChartVersion. It is not the scenario's desired version.
	certificateMigrationSourceVersion = "0.2.2"
)

func platformChartVersion(t *testing.T, localChart string) string {
	t.Helper()
	if version := os.Getenv("ITERABASE_CHART_VERSION"); version != "" {
		return version
	}
	if localChart != "" {
		return ""
	}
	return pinnedPlatformChartVersion
}

func controlPlaneChartVersion(t *testing.T, localChart string) string {
	t.Helper()
	if version := os.Getenv("CONTROL_PLANE_CHART_VERSION"); version != "" {
		return version
	}
	if localChart != "" {
		return ""
	}
	return pinnedControlPlaneChartVersion
}
