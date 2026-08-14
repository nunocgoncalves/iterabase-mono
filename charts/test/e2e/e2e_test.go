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
	}, sharede2e.FixtureFromEnv)
	suite.Add(sharede2e.Define(sharede2e.Scenario[*exampleState]{
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
	}))
	suite.Run(t)
}
