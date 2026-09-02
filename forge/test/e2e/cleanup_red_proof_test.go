package e2e

import (
	"os"
	"strings"
	"testing"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

// TestPermanentGPUCleanupRedProof is an explicit manual negative. It uses the
// shared runner so a failure raised by the owner cleanup hook must trigger the
// diagnostics-after-cleanup-failure path. The real fixture reset completes
// before the intentional error, leaving the host ready for the next green run.
func TestPermanentGPUCleanupRedProof(t *testing.T) {
	if os.Getenv("FORGE_E2E_BREAK_CLEANUP") != "true" {
		t.Skip("intentional permanent-fixture cleanup red proof is disabled")
	}
	suite := sharede2e.NewSuite(
		sharede2e.SuiteMetadata{Name: "forge-cleanup-red-proof", Owner: "forge", Entrypoint: "forge/test/e2e"},
		func(*testing.T) sharede2e.Fixture {
			return sharede2e.Fixture{Mode: sharede2e.FixtureSource, SourceSHA: strings.Repeat("a", 40)}
		},
	)
	suite.Add(sharede2e.Define(sharede2e.Scenario[*digitalOceanGPUState]{
		Metadata: sharede2e.ScenarioMetadata{
			Name: "permanent-gpu-cleanup-red-proof", Description: "Proves a permanent-fixture owner cleanup failure remains red and retains diagnostics.",
			Tier: sharede2e.TierF0, FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource},
		},
		NewState: func(t *testing.T) *digitalOceanGPUState { return newDigitalOceanGPUState(t) },
		Stages: []sharede2e.Stage[*digitalOceanGPUState]{
			{Name: "establish-clean-fixture", Run: provisionGPUStage},
		},
		Diagnostics: gpuScenarioDiagnostics(), Cleanup: gpuScenarioCleanup(),
	}))
	suite.Run(t)
}
