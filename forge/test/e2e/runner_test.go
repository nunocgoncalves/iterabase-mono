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
				"Provisions a fresh CPU host and proves Forge bootstrap, exact-source managed Longhorn RWX prerequisites/conformance/persistence, internal-CA gRPC mTLS with negative rejection probes, migration, exact source/Flux handoff, secret transport, idempotent reconciliation, diagnostics, and cleanup.",
				sharede2e.TierF3,
				[]string{"HOR-406", "HOR-469", "DES-HOR-424-01", "DES-HOR-469-01", "DES-HOR-469-02"},
				[]string{"forge", "iterabase-platform-chart"},
				"test-e2e", 90, "cpu",
			),
			NewState: newDigitalOceanCPUState,
			Stages: []sharede2e.Stage[*digitalOceanCPUState]{
				{Name: "provision-host", Run: cpuDiagnosticStage(failureDomainProvisioning, provisionCPUStage)},
				{Name: "reject-gpu-on-cpu-host", DependsOn: []string{"provision-host"}, Run: cpuDiagnosticStage(failureDomainSubstrate, rejectGPUOnCPUStage)},
				{Name: "install-migration-source", DependsOn: []string{"reject-gpu-on-cpu-host"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, applyBaselineStage)},
				{Name: "assert-migration-source-edge", DependsOn: []string{"install-migration-source"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, assertBaselineStage)},
				{Name: "upgrade-current-with-exact-flux", DependsOn: []string{"assert-migration-source-edge"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runOverlayStage)},
				{Name: "assert-dependent-health-smoke", DependsOn: []string{"upgrade-current-with-exact-flux"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, assertCurrentPlatformStage)},
				{Name: "setup-managed-agentpool", DependsOn: []string{"assert-dependent-health-smoke"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, setupManagedAgentPoolStage)},
				{Name: "force-share-manager-loss", DependsOn: []string{"setup-managed-agentpool"}, Run: cpuDiagnosticStage(failureDomainSubstrate, exerciseManagedShareManagerFailureStage)},
				{Name: "assert-fresh-worker-storage-recovery", DependsOn: []string{"force-share-manager-loss"}, Run: cpuDiagnosticStage(failureDomainDependentSmoke, assertManagedStorageRecoveryStage)},
				{Name: "seed-managed-rwx-claim", DependsOn: []string{"assert-fresh-worker-storage-recovery"}, Run: cpuDiagnosticStage(failureDomainSubstrate, seedManagedRWXReapplyStage)},
				{Name: "reapply-current-idempotently", DependsOn: []string{"seed-managed-rwx-claim"}, Run: cpuDiagnosticStage(failureDomainForgeReconcile, reapplyCurrentPlatformStage)},
				{Name: "assert-managed-rwx-reapply", DependsOn: []string{"reapply-current-idempotently"}, Run: cpuDiagnosticStage(failureDomainSubstrate, assertManagedRWXReapplyStage)},
				{Name: "sync-secrets", DependsOn: []string{"assert-managed-rwx-reapply"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runSecretsStage)},
				{Name: "reconcile-flux", DependsOn: []string{"sync-secrets"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runFluxStage)},
			},
			Diagnostics: cpuScenarioDiagnostics(), Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*digitalOceanCPUState]{
			Metadata: forgeScenarioMetadata(
				"digitalocean-rwx-tls",
				"Provisions a fresh single-node host, packages the exact platform/certificate/RWX companions, establishes the platform internal CA before Longhorn, and proves every current instance-manager gRPC service accepts mTLS while rejecting unauthenticated TLS and plaintext.",
				sharede2e.TierF3,
				[]string{"HOR-469", "DES-HOR-424-01", "DES-HOR-469-01", "DES-HOR-469-02"},
				[]string{"iterabase-platform-chart"},
				"test-e2e-rwx-tls", 70, "cpu",
			),
			NewState: newDigitalOceanRWXTLSState,
			Stages: []sharede2e.Stage[*digitalOceanCPUState]{
				{Name: "provision-single-node", Run: cpuDiagnosticStage(failureDomainProvisioning, provisionCPUStage)},
				{Name: "install-and-prove-managed-internal-tls", DependsOn: []string{"provision-single-node"}, Run: cpuDiagnosticStage(failureDomainForgeHandoff, runOverlayStage)},
			},
			Diagnostics: cpuScenarioDiagnostics(), Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*rwxThreeNodeState]{
			Metadata: forgeScenarioMetadata(
				"digitalocean-rwx-three-node",
				"Provisions three K3s storage nodes, installs the exact managed Longhorn companion, proves three replicas and generic conformance, replaces one lost node, preserves committed bytes across rebuild/reapply, and exercises deletion-confirmed uninstall.",
				sharede2e.TierF3,
				[]string{"HOR-469", "DES-HOR-424-01", "DES-HOR-424-03", "DES-HOR-424-05", "DES-HOR-424-06", "DES-HOR-469-01"},
				[]string{"iterabase-platform-chart"},
				"test-e2e-rwx-three-node", 150, "cpu",
			),
			NewState: newRWXThreeNodeState,
			Stages: []sharede2e.Stage[*rwxThreeNodeState]{
				{Name: "provision-three-nodes-with-dedicated-ssds", Run: provisionRWXThreeNodesStage},
				{Name: "bootstrap-k3s-baseline", DependsOn: []string{"provision-three-nodes-with-dedicated-ssds"}, Run: bootstrapRWXThreeNodeK3sStage},
				{Name: "install-longhorn-1-11-predecessor", DependsOn: []string{"bootstrap-k3s-baseline"}, Run: installThreeNodeRWXPredecessorStage},
				{Name: "validate-external-byo-agentpool", DependsOn: []string{"install-longhorn-1-11-predecessor"}, Run: validateExternalRWXAgentPoolStage},
				{Name: "seed-pre-upgrade-three-replica-volume", DependsOn: []string{"validate-external-byo-agentpool"}, Run: seedThreeNodeRWXVolumeStage},
				{Name: "upgrade-longhorn-1-11-to-1-12", DependsOn: []string{"seed-pre-upgrade-three-replica-volume"}, Run: upgradeThreeNodeRWXCompanionStage},
				{Name: "replace-lost-storage-node", DependsOn: []string{"upgrade-longhorn-1-11-to-1-12"}, Run: replaceLostRWXNodeStage},
				{Name: "assert-persistence-reapply-uninstall", DependsOn: []string{"replace-lost-storage-node"}, Run: assertThreeNodePersistenceReapplyAndUninstallStage},
			},
			Diagnostics: []sharede2e.Hook[*rwxThreeNodeState]{{Name: "three-node-storage-evidence", Run: func(t *testing.T, state *rwxThreeNodeState) { state.diagnostics(t) }}},
			Cleanup:     []sharede2e.Hook[*rwxThreeNodeState]{{Name: "delete-three-node-capacity", Run: func(t *testing.T, state *rwxThreeNodeState) { state.cleanup(t) }}},
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
	return sharede2e.ScenarioMetadata{
		Name: name, Description: description, Tier: tier,
		References: references, ReleaseTargets: targets,
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
