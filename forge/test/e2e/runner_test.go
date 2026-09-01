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
				"Provisions a fresh one-node host plus dedicated workspace disk and proves safe Forge preparation, exact source/Flux handoff, fixed local-path isolation, two-worker RWO readiness, persistence, worker replacement, reapply, diagnostics, and cleanup.",
				sharede2e.TierF3,
				[]string{"HOR-406", "HOR-538", "DES-HOR-538-01", "DES-HOR-538-02", "DES-HOR-538-03"},
				[]string{"forge", "control-plane", "iterabase-platform-chart"},
				"test-e2e", 100, "cpu",
			),
			NewState: newDigitalOceanCPUState,
			Stages: []sharede2e.Stage[*digitalOceanCPUState]{
				{Name: "provision-host-and-dedicated-disk", Run: cpuDiagnosticStage(failureDomainProvisioning, provisionCPUStage)},
				{Name: "reject-gpu-on-cpu-host", DependsOn: []string{"provision-host-and-dedicated-disk"}, Run: cpuDiagnosticStage(failureDomainSubstrate, rejectGPUOnCPUStage)},
				{Name: "install-migration-source", DependsOn: []string{"reject-gpu-on-cpu-host"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, applyBaselineStage)},
				{Name: "assert-migration-source-edge", DependsOn: []string{"install-migration-source"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, assertBaselineStage)},
				{Name: "upgrade-current-with-exact-flux", DependsOn: []string{"assert-migration-source-edge"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runOverlayStage)},
				{Name: "assert-dedicated-local-path", DependsOn: []string{"upgrade-current-with-exact-flux"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, assertCurrentPlatformStage)},
				{Name: "setup-two-worker-rwo-agentpool", DependsOn: []string{"assert-dedicated-local-path"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupLocalPathAgentPoolStage)},
				{Name: "replace-one-workspace-worker", DependsOn: []string{"setup-two-worker-rwo-agentpool"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, replaceWorkspaceWorkerStage)},
				{Name: "seed-dedicated-rwo-claim", DependsOn: []string{"replace-one-workspace-worker"}, Run: cpuDiagnosticStage(failureDomainSubstrate, seedLocalPathReapplyStage)},
				{Name: "reapply-current-idempotently", DependsOn: []string{"seed-dedicated-rwo-claim"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, reapplyCurrentPlatformStage)},
				{Name: "assert-local-path-reapply", DependsOn: []string{"reapply-current-idempotently"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertLocalPathReapplyStage)},
				{Name: "sync-secrets", DependsOn: []string{"assert-local-path-reapply"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runSecretsStage)},
				{Name: "reconcile-flux", DependsOn: []string{"sync-secrets"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runFluxStage)},
			},
			Diagnostics: cpuScenarioDiagnostics(), Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*digitalOceanCPUState]{
			Metadata: forgeScenarioMetadata(
				"digitalocean-workspace",
				"Fresh exact-head real-machine install proving process-open refusal, transport-resolved ext4/XFS identity, authenticated concurrent same-pool work with isolated markers, active-turn capacity gating, human-gate worker replacement, persisted bytes, and reapply with no obsolete backend.",
				sharede2e.TierF3,
				[]string{"HOR-538", "REQ-018", "REQ-035", "SCN-018", "DES-HOR-538-01", "DES-HOR-538-02", "DES-HOR-538-03"},
				[]string{"forge", "control-plane", "iterabase-platform-chart"},
				"test-e2e-workspace", 90, "cpu",
			),
			NewState: newDigitalOceanWorkspaceState,
			Stages: []sharede2e.Stage[*digitalOceanCPUState]{
				{Name: "provision-exact-candidate-and-disk", Run: cpuDiagnosticStage(failureDomainProvisioning, provisionCPUStage)},
				{Name: "refuse-process-held-raw-disk", DependsOn: []string{"provision-exact-candidate-and-disk"}, Run: cpuDiagnosticStage(failureDomainSubstrate, refuseProcessHeldWorkspaceDiskStage)},
				{Name: "fresh-exact-head-install", DependsOn: []string{"refuse-process-held-raw-disk"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runOverlayStage)},
				{Name: "assert-fixed-mount-and-classes", DependsOn: []string{"fresh-exact-head-install"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertCurrentPlatformStage)},
				{Name: "setup-two-worker-rwo-agentpool", DependsOn: []string{"assert-fixed-mount-and-classes"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupLocalPathAgentPoolStage)},
				{Name: "install-real-workspace-execution-fixture", DependsOn: []string{"setup-two-worker-rwo-agentpool"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupWorkspaceExecutionFixtureStage)},
				{Name: "run-authenticated-concurrent-isolated-work", DependsOn: []string{"install-real-workspace-execution-fixture"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, exerciseConcurrentWorkspaceWorkStage)},
				{Name: "cross-capacity-floor-during-active-turn", DependsOn: []string{"run-authenticated-concurrent-isolated-work"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, exerciseActiveWorkspaceCapacityStage)},
				{Name: "resume-human-gated-session-after-worker-replacement", DependsOn: []string{"cross-capacity-floor-during-active-turn"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, exerciseHumanGateWorkspaceReplacementStage)},
				{Name: "seed-committed-workspace-bytes", DependsOn: []string{"resume-human-gated-session-after-worker-replacement"}, Run: cpuDiagnosticStage(failureDomainSubstrate, seedLocalPathReapplyStage)},
				{Name: "reapply-with-unchanged-identities", DependsOn: []string{"seed-committed-workspace-bytes"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, reapplyCurrentPlatformStage)},
				{Name: "assert-persisted-bytes", DependsOn: []string{"reapply-with-unchanged-identities"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertLocalPathReapplyStage)},
			},

			Diagnostics: cpuScenarioDiagnostics(), Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*digitalOceanGPUState]{
			Metadata: forgeScenarioMetadata(
				"digitalocean-gpu",
				"Provisions a fresh GPU host and proves Forge GPU readiness, an emptyDir-safe driver transition, exact artifact handoff, diagnostics, cleanup, and one non-authoritative real-serving smoke request.",
				sharede2e.TierF3,
				[]string{"HOR-411", "HOR-406", "HOR-481", "HOR-485", "HOR-494"},
				[]string{"forge", "iterabase-platform-chart"},
				"test-e2e-gpu", 110, "gpu",
			),
			NewState: newDigitalOceanGPUState,
			Stages: []sharede2e.Stage[*digitalOceanGPUState]{
				{Name: "record-driver-inputs", Run: gpuDiagnosticStage(failureDomainSubstrate, recordGPUUpgradeInputsStage)},
				{Name: "provision-host", DependsOn: []string{"record-driver-inputs"}, Run: gpuDiagnosticStage(failureDomainProvisioning, provisionGPUStage)},
				{Name: "apply-gpu-substrate", DependsOn: []string{"provision-host"}, Run: gpuDiagnosticStage(failureDomainSubstrate, applyGPUSubstrateStage)},
				{Name: "assert-gpu-smoke", DependsOn: []string{"apply-gpu-substrate"}, Run: gpuDiagnosticStage(failureDomainSubstrate, assertGPUSmokeStage)},
				{Name: "start-emptydir-workload", DependsOn: []string{"assert-gpu-smoke"}, Run: gpuDiagnosticStage(failureDomainSubstrate, startGPUUpgradeWorkloadStage)},
				{Name: "apply-driver-upgrade", DependsOn: []string{"start-emptydir-workload"}, Run: gpuDiagnosticStage(failureDomainSubstrate, applyGPUDriverUpgradeStage)},
				{Name: "assert-driver-upgrade", DependsOn: []string{"apply-driver-upgrade"}, Run: gpuDiagnosticStage(failureDomainSubstrate, assertGPUDriverUpgradeStage)},
				{Name: "apply-dependent-platform-smoke", DependsOn: []string{"assert-driver-upgrade"}, Run: gpuDiagnosticStage(failureDomainForgeHandoff, applyInferencePlatformStage)},
				{Name: "run-real-serving-smoke", DependsOn: []string{"apply-dependent-platform-smoke"}, Run: gpuDiagnosticStage(failureDomainDependentSmoke, runInferenceGPUStage)},
			},
			Diagnostics: gpuScenarioDiagnostics(), Cleanup: gpuScenarioCleanup(),
		}),
	)
	suite.Run(t)
}

func forgeScenarioMetadata(name, description string, tier sharede2e.Tier, references, targets []string, makeTarget string, timeout int, capacity string) sharede2e.ScenarioMetadata {
	artifacts := []string{"forge-binary", "iterabase-platform-chart", "cert-manager-substrate-chart", "control-plane-image", "inference-gateway-image"}
	if name == "digitalocean-cpu" || name == "digitalocean-workspace" {
		artifacts = append(artifacts, "harness-image", "tool-runner-image", "certificate-migration-chart")
	}
	if name == "digitalocean-workspace" {
		artifacts = append(artifacts, "runtime-fixture-image")
	}
	return sharede2e.ScenarioMetadata{
		Name: name, Description: description, Tier: tier,
		References: references, ReleaseTargets: targets, RequiredArtifacts: artifacts,
		Intents:      []sharede2e.ExecutionIntent{sharede2e.IntentPR, sharede2e.IntentNightly, sharede2e.IntentCandidate},
		FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate},
		MakeTarget:   makeTarget, TimeoutMinutes: timeout, Capacity: capacity, Mandatory: capacity != "",
	}
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
