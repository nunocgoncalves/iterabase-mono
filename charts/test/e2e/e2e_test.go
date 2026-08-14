package e2e_test

import (
	"slices"
	"testing"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

type exampleState struct{ events []string }

// TestE2E is the charts owner's single compiled suite entrypoint.
func TestE2E(t *testing.T) {
	suite := sharede2e.NewSuite(sharede2e.SuiteMetadata{
		Name: "charts", Owner: "charts", Entrypoint: "charts/test/e2e",
	}, chartFixtureFromEnv)
	suite.Add(
		hermeticExampleScenario(),
		certificateMigrationScenario(),
		freshInstallScenario(),
		observabilityScenario(),
		observabilityTLSScenario(),
		internalTLSScenario(),
	)
	suite.Run(t)
}

func chartFixtureFromEnv(t *testing.T) sharede2e.Fixture {
	t.Helper()
	fixture := sharede2e.FixtureFromEnv(t)
	for _, input := range fixture.Inputs {
		if input.Name == "certificate-migration-source" {
			return fixture
		}
	}
	fixture.Inputs = append(fixture.Inputs, sharede2e.FixtureInput{
		Name: "certificate-migration-source", Kind: "published-chart",
		Reference: "oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform:" + certificateMigrationSourceVersion,
	})
	if err := fixture.Validate(); err != nil {
		t.Fatalf("add chart runtime dependency to fixture: %v", err)
	}
	return fixture
}

func chartScenarioMetadata(name, description, makeTarget string, minutes int, references, targets []string) sharede2e.ScenarioMetadata {
	return sharede2e.ScenarioMetadata{
		Name: name, Description: description, Tier: sharede2e.TierF2,
		References: references, ReleaseTargets: targets,
		FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate, sharede2e.FixturePublished},
		MakeTarget:   makeTarget, TimeoutMinutes: minutes,
	}
}

func hermeticExampleScenario() sharede2e.Definition {
	return sharede2e.Define(sharede2e.Scenario[*exampleState]{
		Metadata: sharede2e.ScenarioMetadata{
			Name: "hermetic-example", Description: "Proves the charts suite composes typed dependent stages without infrastructure.",
			Tier: sharede2e.TierF0, References: []string{"HOR-476"},
			FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource, sharede2e.FixtureCandidate, sharede2e.FixturePublished},
		},
		NewState: func(*testing.T) *exampleState { return &exampleState{} },
		Stages: []sharede2e.Stage[*exampleState]{
			{Name: "render", Run: func(_ *testing.T, state *exampleState) { state.events = append(state.events, "rendered") }},
			{Name: "assert", DependsOn: []string{"render"}, Run: func(t *testing.T, state *exampleState) {
				if !slices.Equal(state.events, []string{"rendered"}) {
					t.Fatalf("events = %v", state.events)
				}
			}},
		},
	})
}
