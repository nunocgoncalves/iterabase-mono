package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureValidationRejectsFloatingAndIncompleteInputs(t *testing.T) {
	t.Parallel()
	fixtures := []Fixture{
		{Mode: FixtureSource, SourceSHA: "short"},
		{Mode: FixtureCandidate, SourceSHA: strings.Repeat("a", 40)},
		{Mode: FixtureCandidate, SourceSHA: strings.Repeat("a", 40), Dirty: true, Inputs: []FixtureInput{{Name: "candidate", Kind: "candidate", Reference: "1.0.0@" + strings.Repeat("a", 40)}}},
		{Mode: FixturePublished, Inputs: []FixtureInput{{Name: "platform", Kind: "chart", Reference: "latest"}}},
		{Mode: FixturePublished, Inputs: []FixtureInput{{Name: "gateway", Kind: "image", Reference: "gateway:1.0.0", Digest: "bad"}}},
	}
	for _, fixture := range fixtures {
		if err := fixture.Validate(); err == nil {
			t.Fatalf("fixture unexpectedly valid: %+v", fixture)
		}
	}
}

func TestFixtureFromEnvRecordsSourceAndPublishedModes(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source-inputs.json")
		data := `{"mode":"published","inputs":[{"name":"platform","kind":"published-chart","reference":"oci://example/platform:1.2.3"}]}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(fixtureModeEnv, string(FixtureSource))
		t.Setenv(fixtureSourceSHAEnv, strings.Repeat("a", 40))
		t.Setenv(fixtureSourceDirtyEnv, "true")
		t.Setenv(fixtureSourceInputsFileEnv, path)
		fixture := FixtureFromEnv(t)
		if fixture.Mode != FixtureSource || !fixture.Dirty || fixture.SourceSHA != strings.Repeat("a", 40) || len(fixture.Inputs) != 1 {
			t.Fatalf("source fixture = %+v", fixture)
		}
	})
	t.Run("published", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "published.json")
		data := `{"mode":"published","inputs":[{"name":"platform","kind":"chart","reference":"oci://example/platform:1.2.3"}]}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(fixtureModeEnv, string(FixturePublished))
		t.Setenv(fixturePublishedFileEnv, path)
		fixture := FixtureFromEnv(t)
		if fixture.Mode != FixturePublished || len(fixture.Inputs) != 1 {
			t.Fatalf("published fixture = %+v", fixture)
		}
	})
}

func TestCandidateFixtureFromPlanRecordsSelectedAndPinnedInputs(t *testing.T) {
	for _, prefix := range []string{"CONTROL_PLANE", "INFERENCE_GATEWAY", "TOOL_RUNNER"} {
		t.Setenv(prefix+"_IMAGE_DIGEST", "")
		t.Setenv(prefix+"_IMAGE_REPO", "")
		t.Setenv(prefix+"_IMAGE_TAG", "")
	}
	path := filepath.Join(t.TempDir(), "candidate-plan.json")
	plan := `{
  "source_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "releases":[{"target":"control-plane","version":"1.2.3"}],
  "baseline_dependencies":{
    "images":[{"name":"inference-gateway","repository":"ghcr.io/example/gateway","version":"2.0.0","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],
    "charts":[{"chart":"iterabase-platform","repository":"oci://example/platform","version":"3.0.0","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]
  }
}`
	if err := os.WriteFile(path, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture, err := CandidateFixtureFromPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Mode != FixtureCandidate || len(fixture.Inputs) != 3 {
		t.Fatalf("candidate fixture = %+v", fixture)
	}
	if fixture.Inputs[0].Kind != "candidate" || fixture.Inputs[1].Kind != "published-chart" || fixture.Inputs[2].Kind != "published-image" {
		t.Fatalf("candidate inputs are not deterministically sorted: %+v", fixture.Inputs)
	}

	t.Setenv("CONTROL_PLANE_IMAGE_REPO", "ghcr.io/example/control-plane")
	t.Setenv("CONTROL_PLANE_IMAGE_TAG", strings.Repeat("a", 40)+"@sha256:"+strings.Repeat("d", 64))
	t.Setenv("CONTROL_PLANE_IMAGE_DIGEST", "sha256:"+strings.Repeat("d", 64))
	fixture, err = CandidateFixtureFromPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.Inputs) != 4 || fixture.Inputs[1].Kind != "candidate-image" {
		t.Fatalf("candidate image identity was not recorded: %+v", fixture.Inputs)
	}
}
