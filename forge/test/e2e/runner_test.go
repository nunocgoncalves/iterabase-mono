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
		sharede2e.Define(sharede2e.Scenario[*permanentCPUFixtureState]{
			Metadata: forgeScenarioMetadata(
				permanentCPUScenarioName,
				"Resets the permanent CPU fixture and proves receipt-bound thick LVM preparation, exact OpenEBS/source/Flux handoff, two-worker same-node RWO readiness, persistence, worker replacement, reapply, diagnostics, and cleanup.",
				sharede2e.TierF3,
				[]string{"HOR-406", "HOR-545", "DES-HOR-545-01", "DES-HOR-545-02", "DES-HOR-545-03", "DES-HOR-538-03"},
				[]string{"forge", "control-plane", "iterabase-platform-chart"},
				"test-e2e", 100, "cpu",
			),
			NewState: newPermanentCPUFixtureState,
			Stages: []sharede2e.Stage[*permanentCPUFixtureState]{
				{Name: "reset-permanent-cpu-fixture", Run: cpuDiagnosticStage(failureDomainFixtureReset, resetPermanentCPUFixtureStage)},
				{Name: "reject-gpu-on-cpu-host", DependsOn: []string{"reset-permanent-cpu-fixture"}, Run: cpuDiagnosticStage(failureDomainSubstrate, rejectGPUOnCPUStage)},
				{Name: "fresh-current-with-exact-flux", DependsOn: []string{"reject-gpu-on-cpu-host"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runOverlayStage)},
				{Name: "assert-openebs-lvm-foundation", DependsOn: []string{"fresh-current-with-exact-flux"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, assertCurrentPlatformStage)},
				{Name: "setup-two-worker-rwo-agentpool", DependsOn: []string{"assert-openebs-lvm-foundation"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupLVMSharedAgentPoolStage)},
				{Name: "replace-one-workspace-worker", DependsOn: []string{"setup-two-worker-rwo-agentpool"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, replaceWorkspaceWorkerStage)},
				{Name: "seed-dedicated-rwo-claim", DependsOn: []string{"replace-one-workspace-worker"}, Run: cpuDiagnosticStage(failureDomainSubstrate, seedLVMReapplyStage)},
				{Name: "reapply-current-idempotently", DependsOn: []string{"seed-dedicated-rwo-claim"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, reapplyCurrentPlatformStage)},
				{Name: "assert-lvm-reapply", DependsOn: []string{"reapply-current-idempotently"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertLVMReapplyStage)},
				{Name: "sync-secrets", DependsOn: []string{"assert-lvm-reapply"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runSecretsStage)},
				{Name: "reconcile-flux", DependsOn: []string{"sync-secrets"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runFluxStage)},
			},
			Diagnostics: cpuScenarioDiagnostics(), Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*permanentCPUFixtureState]{
			Metadata: forgeScenarioMetadata(
				permanentCPUWorkspaceScenarioName,
				"Fresh exact-head real-machine install proving process-open refusal, receipt-bound PV/VG identity, pinned OpenEBS thick XFS claims, authenticated concurrent same-pool work with isolated markers, per-pool active-turn capacity gating, aggregate VG pressure, human-gate worker replacement, persisted bytes, and exact reapply.",
				sharede2e.TierF3,
				[]string{"HOR-545", "REQ-018", "REQ-035", "SCN-018", "DES-HOR-545-01", "DES-HOR-545-02", "DES-HOR-545-03", "DES-HOR-538-03"},
				[]string{"forge", "control-plane", "iterabase-platform-chart"},
				"test-e2e-workspace", 90, "cpu",
			),
			NewState: newPermanentCPUWorkspaceFixtureState,
			Stages: []sharede2e.Stage[*permanentCPUFixtureState]{
				{Name: "reset-permanent-cpu-fixture", Run: cpuDiagnosticStage(failureDomainFixtureReset, resetPermanentCPUFixtureStage)},
				{Name: "refuse-process-held-raw-disk", DependsOn: []string{"reset-permanent-cpu-fixture"}, Run: cpuDiagnosticStage(failureDomainSubstrate, refuseProcessHeldWorkspaceDiskStage)},
				{Name: "fresh-exact-head-install", DependsOn: []string{"refuse-process-held-raw-disk"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runOverlayStage)},
				{Name: "assert-pvs-vg-substrate-and-classes", DependsOn: []string{"fresh-exact-head-install"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertCurrentPlatformStage)},
				{Name: "setup-two-worker-rwo-agentpool", DependsOn: []string{"assert-pvs-vg-substrate-and-classes"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupLVMSharedAgentPoolStage)},
				{Name: "install-real-workspace-execution-fixture", DependsOn: []string{"setup-two-worker-rwo-agentpool"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupWorkspaceExecutionFixtureStage)},
				{Name: "run-authenticated-concurrent-isolated-work", DependsOn: []string{"install-real-workspace-execution-fixture"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, exerciseConcurrentWorkspaceWorkStage)},
				{Name: "cross-capacity-floor-during-active-turn", DependsOn: []string{"run-authenticated-concurrent-isolated-work"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, exerciseActiveWorkspaceCapacityStage)},
				{Name: "prove-aggregate-vg-pressure-and-new-claim-exhaustion", DependsOn: []string{"cross-capacity-floor-during-active-turn"}, Run: cpuDiagnosticStage(failureDomainSubstrate, exerciseAggregateVGCapacityStage)},
				{Name: "resume-human-gated-session-after-worker-replacement", DependsOn: []string{"prove-aggregate-vg-pressure-and-new-claim-exhaustion"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, exerciseHumanGateWorkspaceReplacementStage)},
				{Name: "seed-committed-workspace-bytes", DependsOn: []string{"resume-human-gated-session-after-worker-replacement"}, Run: cpuDiagnosticStage(failureDomainSubstrate, seedLVMReapplyStage)},
				{Name: "reboot-with-unchanged-storage-identities", DependsOn: []string{"seed-committed-workspace-bytes"}, Run: cpuDiagnosticStage(failureDomainSubstrate, rebootPreservesLVMStorageStage)},
				{Name: "reapply-with-unchanged-identities", DependsOn: []string{"reboot-with-unchanged-storage-identities"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, reapplyCurrentPlatformStage)},
				{Name: "assert-persisted-bytes", DependsOn: []string{"reapply-with-unchanged-identities"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertLVMReapplyStage)},
				{Name: "delete-general-claim-without-leaked-lv", DependsOn: []string{"assert-persisted-bytes"}, Run: cpuDiagnosticStage(failureDomainSubstrate, deleteLVMClaimStage)},
				{Name: "ordinary-destroy-preserves-data-vg", DependsOn: []string{"delete-general-claim-without-leaked-lv"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, destroyPreservesDataStorageStage)},
			},

			Diagnostics: cpuScenarioDiagnostics(), Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*permanentGPUFixtureState]{
			Metadata: forgeScenarioMetadata(
				permanentGPUScenarioName,
				"Resets the permanent GPU fixture and proves Forge GPU readiness, an emptyDir-safe driver transition, exact artifact handoff, diagnostics, cleanup, and one non-authoritative real-serving smoke request.",
				sharede2e.TierF3,
				[]string{"HOR-411", "HOR-406", "HOR-481", "HOR-485", "HOR-494", "DES-HOR-545-02"},
				[]string{"forge", "iterabase-platform-chart"},
				"test-e2e-gpu", 110, "gpu",
			),
			NewState: newPermanentGPUFixtureState,
			Stages: []sharede2e.Stage[*permanentGPUFixtureState]{
				{Name: "record-driver-inputs", Run: gpuDiagnosticStage(failureDomainSubstrate, recordGPUUpgradeInputsStage)},
				{Name: "reset-permanent-gpu-fixture", DependsOn: []string{"record-driver-inputs"}, Run: gpuDiagnosticStage(failureDomainFixtureReset, resetPermanentGPUFixtureStage)},
				{Name: "apply-gpu-substrate", DependsOn: []string{"reset-permanent-gpu-fixture"}, Run: gpuDiagnosticStage(failureDomainSubstrate, applyGPUSubstrateStage)},
				{Name: "assert-gpu-smoke", DependsOn: []string{"apply-gpu-substrate"}, Run: gpuDiagnosticStage(failureDomainSubstrate, assertGPUSmokeStage)},
				{Name: "apply-dependent-platform-smoke", DependsOn: []string{"assert-gpu-smoke"}, Run: gpuDiagnosticStage(failureDomainForgeHandoff, applyInferencePlatformStage)},
				{Name: "start-emptydir-workload", DependsOn: []string{"apply-dependent-platform-smoke"}, Run: gpuDiagnosticStage(failureDomainSubstrate, startGPUUpgradeWorkloadStage)},
				{Name: "apply-driver-upgrade", DependsOn: []string{"start-emptydir-workload"}, Run: gpuDiagnosticStage(failureDomainSubstrate, applyGPUDriverUpgradeStage)},
				{Name: "assert-driver-upgrade", DependsOn: []string{"apply-driver-upgrade"}, Run: gpuDiagnosticStage(failureDomainSubstrate, assertGPUDriverUpgradeStage)},
				{Name: "run-real-serving-smoke", DependsOn: []string{"assert-driver-upgrade"}, Run: gpuDiagnosticStage(failureDomainDependentSmoke, runInferenceGPUStage)},
			},
			Diagnostics: gpuScenarioDiagnostics(), Cleanup: gpuScenarioCleanup(),
		}),
	)
	suite.Run(t)
}

func forgeScenarioMetadata(name, description string, tier sharede2e.Tier, references, targets []string, makeTarget string, timeout int, capacity string) sharede2e.ScenarioMetadata {
	artifacts := []string{"forge-binary", "iterabase-platform-chart", "cert-manager-substrate-chart", "lvm-storage-substrate-chart", "control-plane-image", "tool-runner-image", "inference-gateway-image"}
	if name == permanentCPUScenarioName || name == permanentCPUWorkspaceScenarioName {
		artifacts = append(artifacts, "harness-image")
	}
	if name == permanentCPUWorkspaceScenarioName {
		artifacts = append(artifacts, "runtime-fixture-image")
	}
	return sharede2e.ScenarioMetadata{
		Name: name, Description: description, Tier: tier,
		References: references, ReleaseTargets: targets, RequiredArtifacts: artifacts,
		Intents:      []sharede2e.ExecutionIntent{sharede2e.IntentPR, sharede2e.IntentCandidate},
		FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate},
		MakeTarget:   makeTarget, TimeoutMinutes: timeout, Capacity: capacity, Mandatory: capacity != "",
	}
}

func TestGPUScenarioSelectsEveryChartRuntimeImage(t *testing.T) {
	metadata := forgeScenarioMetadata(permanentGPUScenarioName, "gpu", sharede2e.TierF3, nil, nil, "test-e2e-gpu", 110, "gpu")
	for _, artifact := range []string{"control-plane-image", "inference-gateway-image", "tool-runner-image"} {
		if !slices.Contains(metadata.RequiredArtifacts, artifact) {
			t.Fatalf("GPU scenario does not select chart runtime artifact %q: %v", artifact, metadata.RequiredArtifacts)
		}
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
