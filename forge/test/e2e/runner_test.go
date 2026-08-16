package e2e

import (
	"slices"
	"testing"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

// TestE2E is Forge's single compiled suite entrypoint. Infrastructure scenarios
// remain selectable with go test -run while catalogue mode emits registrations
// without resolving fixtures or provisioning anything.
func TestE2E(t *testing.T) {
	suite := sharede2e.NewSuite(sharede2e.SuiteMetadata{
		Name: "forge", Owner: "forge", Entrypoint: "forge/test/e2e",
	}, sharede2e.FixtureFromEnv)
	suite.Add(
		hermeticExampleScenario(),
		sharede2e.Define(sharede2e.Scenario[*digitalOceanCPUState]{
			Metadata: forgeScenarioMetadata(
				"digitalocean-cpu",
				"Provisions a fresh CPU host and proves Forge substrate, exact source handoff, reconciliation, and cleanup.",
				sharede2e.TierF3,
				[]string{"HOR-481"},
				[]string{"forge", "iterabase-platform-chart"},
				"test-e2e", 70, "cpu",
			),
			NewState: newDigitalOceanCPUState,
			Stages: []sharede2e.Stage[*digitalOceanCPUState]{
				{Name: "provision-host", Run: provisionCPUStage},
				{Name: "reject-gpu-on-cpu-host", DependsOn: []string{"provision-host"}, Run: rejectGPUOnCPUStage},
				{Name: "install-migration-source", DependsOn: []string{"reject-gpu-on-cpu-host"}, Run: applyBaselineStage},
				{Name: "assert-migration-source-edge", DependsOn: []string{"install-migration-source"}, Run: assertBaselineStage},
				{Name: "upgrade-current-with-exact-flux", DependsOn: []string{"assert-migration-source-edge"}, Run: runOverlayStage},
				{Name: "assert-current-platform", DependsOn: []string{"upgrade-current-with-exact-flux"}, Run: assertCurrentPlatformStage},
				{Name: "reapply-current-idempotently", DependsOn: []string{"assert-current-platform"}, Run: reapplyCurrentPlatformStage},
				{Name: "sync-secrets", DependsOn: []string{"reapply-current-idempotently"}, Run: runSecretsStage},
				{Name: "reconcile-flux", DependsOn: []string{"sync-secrets"}, Run: runFluxStage},
			},
		}),
		sharede2e.Define(sharede2e.Scenario[*digitalOceanGPUState]{
			Metadata: forgeScenarioMetadata(
				"digitalocean-gpu",
				"Provisions a fresh GPU host and proves an emptyDir-safe driver transition, GPU substrate, and minimal real serving composition.",
				sharede2e.TierF3,
				[]string{"HOR-411", "HOR-481", "HOR-485", "HOR-494"},
				[]string{"forge", "iterabase-platform-chart"},
				"test-e2e-gpu", 110, "gpu",
			),
			NewState: newDigitalOceanGPUState,
			Stages: []sharede2e.Stage[*digitalOceanGPUState]{
				{Name: "record-driver-inputs", Run: recordGPUUpgradeInputsStage},
				{Name: "provision-host", DependsOn: []string{"record-driver-inputs"}, Run: provisionGPUStage},
				{Name: "apply-gpu-substrate", DependsOn: []string{"provision-host"}, Run: applyGPUSubstrateStage},
				{Name: "assert-gpu-smoke", DependsOn: []string{"apply-gpu-substrate"}, Run: assertGPUSmokeStage},
				{Name: "start-emptydir-workload", DependsOn: []string{"assert-gpu-smoke"}, Run: startGPUUpgradeWorkloadStage},
				{Name: "apply-driver-upgrade", DependsOn: []string{"start-emptydir-workload"}, Run: applyGPUDriverUpgradeStage},
				{Name: "assert-driver-upgrade", DependsOn: []string{"apply-driver-upgrade"}, Run: assertGPUDriverUpgradeStage},
				{Name: "apply-platform", DependsOn: []string{"assert-driver-upgrade"}, Run: applyInferencePlatformStage},
				{Name: "run-real-inference", DependsOn: []string{"apply-platform"}, Run: runInferenceGPUStage},
			},
		}),
		simpleForgeScenario(
			forgeScenarioMetadata("kind-controlplane-identity", "Proves the deployed control-plane identity and JWT contract on fresh Kind.", sharede2e.TierF2,
				[]string{"HOR-478"}, []string{"control-plane", "control-plane-chart", "iterabase-platform-chart"}, "test-e2e-controlplane", 20, ""),
			"exercise-identity-contract", runControlPlaneIdentity,
		),
		simpleForgeScenario(
			forgeScenarioMetadata("kind-inference-contract", "Proves deployed control-plane to inference-gateway catalogue and authentication composition on fresh Kind.", sharede2e.TierF2,
				[]string{"HOR-477"}, []string{"control-plane", "inference-gateway", "inference-gateway-chart", "iterabase-platform-chart"}, "test-e2e-inference", 20, ""),
			"exercise-inference-contract", runInferenceFlowContract,
		),
		simpleForgeScenario(
			forgeScenarioMetadata("kind-tool-runner-contract", "Proves exact Flux artifact materialization, tool registration, pinning, drain, and retirement on fresh Kind.", sharede2e.TierF2,
				[]string{"HOR-477"}, []string{"control-plane", "control-plane-chart", "iterabase-platform-chart"}, "test-e2e-tool-runner", 35, ""),
			"exercise-tool-runner-contract", runToolRunnerContract,
		),
	)
	suite.Run(t)
}

func forgeScenarioMetadata(name, description string, tier sharede2e.Tier, references, targets []string, makeTarget string, timeout int, capacity string) sharede2e.ScenarioMetadata {
	return sharede2e.ScenarioMetadata{
		Name: name, Description: description, Tier: tier,
		References: references, ReleaseTargets: targets,
		FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate, sharede2e.FixturePublished},
		MakeTarget:   makeTarget, TimeoutMinutes: timeout, Capacity: capacity, Mandatory: capacity != "",
	}
}

func simpleForgeScenario(metadata sharede2e.ScenarioMetadata, stageName string, run func(*testing.T)) sharede2e.Definition {
	return sharede2e.Define(sharede2e.Scenario[struct{}]{
		Metadata: metadata,
		Stages:   []sharede2e.Stage[struct{}]{{Name: stageName, Run: func(t *testing.T, _ struct{}) { run(t) }}},
	})
}

type hermeticExampleState struct{ events []string }

func hermeticExampleScenario() sharede2e.Definition {
	return sharede2e.Define(sharede2e.Scenario[*hermeticExampleState]{
		Metadata: sharede2e.ScenarioMetadata{
			Name: "hermetic-example", Description: "Proves the Forge suite composes typed dependent stages without infrastructure.",
			Tier: sharede2e.TierF0, References: []string{"HOR-476"},
			FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate, sharede2e.FixturePublished},
		},
		NewState: func(*testing.T) *hermeticExampleState { return &hermeticExampleState{} },
		Stages: []sharede2e.Stage[*hermeticExampleState]{
			{Name: "arrange", Run: func(_ *testing.T, state *hermeticExampleState) { state.events = append(state.events, "arranged") }},
			{Name: "assert", DependsOn: []string{"arrange"}, Run: func(t *testing.T, state *hermeticExampleState) {
				if !slices.Equal(state.events, []string{"arranged"}) {
					t.Fatalf("events = %v", state.events)
				}
			}},
		},
	})
}
