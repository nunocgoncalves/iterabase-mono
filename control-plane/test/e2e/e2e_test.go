package e2e_test

import (
	"slices"
	"testing"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

type exampleState struct{ events []string }

// TestE2E is the control-plane owner's single compiled suite entrypoint.
func TestE2E(t *testing.T) {
	suite := sharede2e.NewSuite(sharede2e.SuiteMetadata{
		Name: "control-plane", Owner: "control-plane", Entrypoint: "control-plane/test/e2e",
	}, sharede2e.FixtureFromEnv)
	suite.Add(
		hermeticExampleScenario(),
		deployedIdentityAPIScenario(),
		deployedWorkRecoveryScenario(),
		deployedArtifactDurabilityScenario(),
	)
	suite.Run(t)
}

func deployedMetadata(name, description, makeTarget string, timeout int, references []string) sharede2e.ScenarioMetadata {
	return sharede2e.ScenarioMetadata{
		Name: name, Description: description, Tier: sharede2e.TierF2,
		References:     references,
		ReleaseTargets: []string{"control-plane", "control-plane-chart", "iterabase-platform-chart"},
		FixtureModes:   []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate},
		MakeTarget:     makeTarget, TimeoutMinutes: timeout,
	}
}

func deployedIdentityAPIScenario() sharede2e.Definition {
	diagnostics, cleanup := deployedScenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*deployedState]{
		Metadata: deployedMetadata(
			"deployed-identity-api",
			"Builds and deploys the current control-plane, then proves bootstrap, verified TLS/JWKS, API scopes, delegated identity, soft deletion, migrations, and process recovery.",
			"test-e2e-identity", 30,
			[]string{"HOR-478", "REQ-009", "REQ-010", "SCN-008", "SCN-009"},
		),
		NewState: newDeployedState,
		Stages: []sharede2e.Stage[*deployedState]{
			{Name: "build-source-image", Run: buildSourceImageStage},
			{Name: "create-kind", DependsOn: []string{"build-source-image"}, Run: createControlPlaneKindStage},
			{Name: "load-source-image", DependsOn: []string{"create-kind"}, Run: loadSourceImageStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"load-source-image"}, Run: installCertificateSubstrateStage},
			{Name: "install-control-plane", DependsOn: []string{"install-certificate-substrate"}, Run: installControlPlanePlatformStage},
			{Name: "assert-deployment-ready", DependsOn: []string{"install-control-plane"}, Run: assertDeploymentReadyStage},
			{Name: "exercise-identity-api", DependsOn: []string{"assert-deployment-ready"}, Run: exerciseIdentityAPIStage},
		},
		Diagnostics: diagnostics, Cleanup: cleanup,
	})
}

func deployedWorkRecoveryScenario() sharede2e.Definition {
	diagnostics, cleanup := deployedScenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*deployedState]{
		Metadata: deployedMetadata(
			"deployed-work-recovery",
			"Proves authenticated concurrent manual starts, customer-safe work projections, blockers, feedback/revisions, immutable attempts, durable restart, and ordered SSE reconnect.",
			"test-e2e-work", 35,
			[]string{"HOR-478", "REQ-005", "REQ-009", "REQ-018", "REQ-020", "REQ-023", "REQ-024", "REQ-025", "REQ-041", "REQ-043", "SCN-005", "SCN-006", "SCN-007", "SCN-008", "SCN-019"},
		),
		NewState: newDeployedState,
		Stages: []sharede2e.Stage[*deployedState]{
			{Name: "build-source-image", Run: buildSourceImageStage},
			{Name: "create-kind", DependsOn: []string{"build-source-image"}, Run: createControlPlaneKindStage},
			{Name: "load-source-image", DependsOn: []string{"create-kind"}, Run: loadSourceImageStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"load-source-image"}, Run: installCertificateSubstrateStage},
			{Name: "install-control-plane", DependsOn: []string{"install-certificate-substrate"}, Run: installControlPlanePlatformStage},
			{Name: "assert-deployment-ready", DependsOn: []string{"install-control-plane"}, Run: assertDeploymentReadyStage},
			{Name: "setup-work-journey", DependsOn: []string{"assert-deployment-ready"}, Run: setupWorkJourneyStage},
			{Name: "start-concurrently", DependsOn: []string{"setup-work-journey"}, Run: concurrentIdempotentStartStage},
			{Name: "exercise-work-commands", DependsOn: []string{"start-concurrently"}, Run: workCommandsAndHistoryStage},
			{Name: "restart-and-reconnect", DependsOn: []string{"exercise-work-commands"}, Run: restartAndReconnectStage},
		},
		Diagnostics: diagnostics, Cleanup: cleanup,
	})
}

func deployedArtifactDurabilityScenario() sharede2e.Definition {
	diagnostics, cleanup := deployedScenarioHooks()
	return sharede2e.Define(sharede2e.Scenario[*deployedState]{
		Metadata: deployedMetadata(
			"deployed-artifact-durability",
			"Proves public artifact upload/publication, work linking, immutable download, scoped deletion, MinIO/API restart durability, and persistent tombstones.",
			"test-e2e-artifact", 35,
			[]string{"HOR-478", "REQ-005", "REQ-009", "REQ-019", "REQ-033", "REQ-043", "SCN-004", "SCN-008", "SCN-020"},
		),
		NewState: newDeployedState,
		Stages: []sharede2e.Stage[*deployedState]{
			{Name: "build-source-image", Run: buildSourceImageStage},
			{Name: "create-kind", DependsOn: []string{"build-source-image"}, Run: createControlPlaneKindStage},
			{Name: "load-source-image", DependsOn: []string{"create-kind"}, Run: loadSourceImageStage},
			{Name: "install-certificate-substrate", DependsOn: []string{"load-source-image"}, Run: installCertificateSubstrateStage},
			{Name: "install-control-plane", DependsOn: []string{"install-certificate-substrate"}, Run: installControlPlanePlatformStage},
			{Name: "assert-deployment-ready", DependsOn: []string{"install-control-plane"}, Run: assertDeploymentReadyStage},
			{Name: "setup-artifact-journey", DependsOn: []string{"assert-deployment-ready"}, Run: setupArtifactJourneyStage},
			{Name: "upload-publish-and-link", DependsOn: []string{"setup-artifact-journey"}, Run: uploadPublishAndLinkArtifactStage},
			{Name: "restart-artifact-processes", DependsOn: []string{"upload-publish-and-link"}, Run: restartArtifactProcessesStage},
			{Name: "delete-and-assert-tombstone", DependsOn: []string{"restart-artifact-processes"}, Run: deleteAndAssertTombstoneStage},
		},
		Diagnostics: diagnostics, Cleanup: cleanup,
	})
}

func hermeticExampleScenario() sharede2e.Definition {
	return sharede2e.Define(sharede2e.Scenario[*exampleState]{
		Metadata: sharede2e.ScenarioMetadata{
			Name: "hermetic-example", Description: "Proves the control-plane suite composes typed dependent stages without infrastructure.",
			Tier: sharede2e.TierF0, References: []string{"HOR-476"},
			FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate, sharede2e.FixturePublished},
		},
		NewState: func(*testing.T) *exampleState { return &exampleState{} },
		Stages: []sharede2e.Stage[*exampleState]{
			{Name: "arrange", Run: func(_ *testing.T, state *exampleState) { state.events = append(state.events, "arranged") }},
			{Name: "assert", DependsOn: []string{"arrange"}, Run: func(t *testing.T, state *exampleState) {
				if !slices.Equal(state.events, []string{"arranged"}) {
					t.Fatalf("events = %v", state.events)
				}
			}},
		},
	})
}
