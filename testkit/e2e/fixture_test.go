package e2e

import (
	"encoding/json"
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

func TestObservedRuntimeImageIdentitiesReconcilePassedResult(t *testing.T) {
	t.Setenv(RequiredEnv, "true")
	t.Setenv(ResultOutputEnv, filepath.Join(t.TempDir(), "result.json"))
	configDigest := "sha256:" + strings.Repeat("a", 64)
	artifacts := []RuntimeArtifact{
		{Name: "control-plane-image", Kind: "image", Digest: configDigest, ConfigDigest: configDigest},
		{Name: "platform-chart", Kind: "chart", Checksum: strings.Repeat("b", 64)},
	}
	runtimeDigest := "sha256:" + strings.Repeat("c", 64)
	if err := RecordRuntimeImageIdentity("control-plane-image", runtimeDigest); err != nil {
		t.Fatal(err)
	}
	observed, err := resultArtifactsWithRuntimeIdentities(artifacts, true)
	if err != nil {
		t.Fatal(err)
	}
	if observed[0].RuntimeDigest != runtimeDigest {
		t.Fatalf("observed runtime digest=%q", observed[0].RuntimeDigest)
	}
	if err := RecordRuntimeImageIdentity("control-plane-image", runtimeDigest); err == nil {
		t.Fatal("duplicate observed runtime identity unexpectedly passed")
	}
}

func TestPassedResultRejectsMissingRuntimeImageIdentity(t *testing.T) {
	t.Setenv(RequiredEnv, "true")
	t.Setenv(ResultOutputEnv, filepath.Join(t.TempDir(), "result.json"))
	_, err := resultArtifactsWithRuntimeIdentities([]RuntimeArtifact{{Name: "control-plane-image", Kind: "image"}}, true)
	if err == nil || !strings.Contains(err.Error(), "no observed runtime identity") {
		t.Fatalf("missing runtime identity error=%v", err)
	}
}

func TestFixtureFromRuntimeBundleRecordsSelectedAndBaselineInputs(t *testing.T) {
	for _, intent := range []ExecutionIntent{IntentPR, IntentCandidate} {
		t.Run(string(intent), func(t *testing.T) {
			bundle := RuntimeBundle{
				SchemaVersion: 1, Intent: intent, SourceSHA: strings.Repeat("a", 40),
				PlanSHA256: strings.Repeat("b", 64), CatalogueSHA256: strings.Repeat("c", 64),
				Artifacts: []RuntimeArtifact{
					{Name: "control-plane-image", Kind: "image", Custody: "selected-temporary", SourceSHA: strings.Repeat("a", 40), Reference: "registry/control-plane:source", Digest: "sha256:" + strings.Repeat("d", 64), ConfigDigest: "sha256:" + strings.Repeat("2", 64), RecipeHash: strings.Repeat("e", 64)},
					{Name: "platform-chart", Kind: "chart", Custody: "published-baseline", Reference: "oci://registry/platform:1.2.3", Checksum: strings.Repeat("f", 64), RecipeHash: strings.Repeat("1", 64)},
				},
			}
			if intent == IntentCandidate {
				bundle.Artifacts[0].Custody = "selected-candidate"
			}
			data, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "runtime-bundle.json")
			if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(RuntimeBundleEnv, path)
			fixture := FixtureFromEnv(t)
			wantMode := FixtureSource
			if intent == IntentCandidate {
				wantMode = FixtureCandidate
			}
			if fixture.Mode != wantMode || fixture.SourceSHA != bundle.SourceSHA || len(fixture.Inputs) != 2 || fixture.Inputs[0].Name != "control-plane-image" || fixture.Inputs[1].Custody != "published-baseline" {
				t.Fatalf("runtime fixture = %+v", fixture)
			}
		})
	}
}
