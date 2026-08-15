package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	fixtureModeEnv             = "ITERABASE_E2E_FIXTURE_MODE"
	fixtureSourceSHAEnv        = "ITERABASE_E2E_SOURCE_SHA"
	fixtureSourceDirtyEnv      = "ITERABASE_E2E_SOURCE_DIRTY"
	fixtureSourceInputsFileEnv = "ITERABASE_E2E_SOURCE_INPUTS"
	fixtureCandidatePlanEnv    = "ITERABASE_E2E_CANDIDATE_PLAN"
	fixturePublishedFileEnv    = "ITERABASE_E2E_PUBLISHED_FIXTURE"
)

// Fixture records the exact source, candidate, or published inputs used by one
// suite execution.
type Fixture struct {
	Mode      FixtureMode    `json:"mode"`
	SourceSHA string         `json:"source_sha,omitempty"`
	Dirty     bool           `json:"dirty,omitempty"`
	Inputs    []FixtureInput `json:"inputs,omitempty"`
}

// FixtureInput records one immutable runtime input. Reference is a semantic
// version, full source identity, or immutable path; Digest/Checksum strengthen
// identities when the artifact format supports them.
type FixtureInput struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Digest    string `json:"digest,omitempty"`
	Checksum  string `json:"checksum,omitempty"`
}

var (
	fullSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	canonicalHash = regexp.MustCompile(`^(?:sha256:)?[0-9a-f]{64}$`)
)

// Validate rejects floating or incomplete fixture identities.
func (fixture Fixture) Validate() error {
	switch fixture.Mode {
	case FixtureSource:
		if !fullSHA.MatchString(fixture.SourceSHA) {
			return fmt.Errorf("source fixture requires a full lowercase source SHA")
		}
	case FixtureCandidate:
		if fixture.Dirty {
			return fmt.Errorf("candidate fixture cannot use a dirty source tree")
		}
		if !fullSHA.MatchString(fixture.SourceSHA) {
			return fmt.Errorf("candidate fixture requires a full lowercase source SHA")
		}
		if len(fixture.Inputs) == 0 {
			return fmt.Errorf("candidate fixture has no exact inputs")
		}
	case FixturePublished:
		if fixture.Dirty {
			return fmt.Errorf("published fixture cannot use a dirty source tree")
		}
		if len(fixture.Inputs) == 0 {
			return fmt.Errorf("published fixture has no exact inputs")
		}
	default:
		return fmt.Errorf("unsupported fixture mode %q", fixture.Mode)
	}
	seen := make(map[string]struct{}, len(fixture.Inputs))
	for _, input := range fixture.Inputs {
		if input.Name == "" || input.Kind == "" || input.Reference == "" {
			return fmt.Errorf("fixture input must include name, kind, and reference: %+v", input)
		}
		if strings.Contains(strings.ToLower(input.Reference), "latest") {
			return fmt.Errorf("fixture input %q uses floating latest reference %q", input.Name, input.Reference)
		}
		key := input.Kind + "/" + input.Name
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate fixture input %q", key)
		}
		seen[key] = struct{}{}
		if input.Digest != "" && !canonicalHash.MatchString(input.Digest) {
			return fmt.Errorf("fixture input %q has invalid digest %q", input.Name, input.Digest)
		}
		if input.Checksum != "" && !canonicalHash.MatchString(input.Checksum) {
			return fmt.Errorf("fixture input %q has invalid checksum %q", input.Name, input.Checksum)
		}
	}
	return nil
}

// FixtureFromEnv resolves the explicit suite mode. There is intentionally no
// default and no coordinated-ref or latest fallback.
func FixtureFromEnv(t *testing.T) Fixture {
	t.Helper()
	mode := FixtureMode(os.Getenv(fixtureModeEnv))
	var fixture Fixture
	switch mode {
	case FixtureSource:
		fixture = Fixture{
			Mode: mode, SourceSHA: os.Getenv(fixtureSourceSHAEnv),
			Dirty: os.Getenv(fixtureSourceDirtyEnv) == "true",
		}
		if path := os.Getenv(fixtureSourceInputsFileEnv); path != "" {
			inputsFixture, err := fixtureFromFile(path)
			if err != nil {
				t.Fatalf("load source fixture inputs: %v", err)
			}
			if inputsFixture.Mode != FixturePublished {
				t.Fatalf("%s must contain published dependency inputs (got mode %q)", fixtureSourceInputsFileEnv, inputsFixture.Mode)
			}
			if err := inputsFixture.Validate(); err != nil {
				t.Fatalf("invalid source fixture inputs: %v", err)
			}
			fixture.Inputs = inputsFixture.Inputs
		}
	case FixtureCandidate:
		path := os.Getenv(fixtureCandidatePlanEnv)
		if path == "" {
			t.Fatalf("%s=candidate requires %s", fixtureModeEnv, fixtureCandidatePlanEnv)
		}
		var err error
		fixture, err = CandidateFixtureFromPlan(path)
		if err != nil {
			t.Fatalf("load candidate fixture: %v", err)
		}
	case FixturePublished:
		path := os.Getenv(fixturePublishedFileEnv)
		if path == "" {
			t.Fatalf("%s=published requires %s", fixtureModeEnv, fixturePublishedFileEnv)
		}
		var err error
		fixture, err = fixtureFromFile(path)
		if err != nil {
			t.Fatalf("load published fixture: %v", err)
		}
		if fixture.Mode != FixturePublished {
			t.Fatalf("published fixture file records mode %q", fixture.Mode)
		}
	default:
		t.Fatalf("%s must be source, candidate, or published (got %q)", fixtureModeEnv, mode)
	}
	if err := fixture.Validate(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	return fixture
}

func fixtureFromFile(path string) (Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	var fixture Fixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return Fixture{}, err
	}
	return fixture, nil
}

// CandidateFixtureFromPlan derives exact candidate and pinned-baseline inputs
// from the release plan consumed by the gate.
func CandidateFixtureFromPlan(path string) (Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	var plan struct {
		SourceSHA string `json:"source_sha"`
		Releases  []struct {
			Target  string `json:"target"`
			Version string `json:"version"`
		} `json:"releases"`
		Baselines struct {
			Images []struct {
				Name       string `json:"name"`
				Repository string `json:"repository"`
				Version    string `json:"version"`
				Digest     string `json:"digest"`
			} `json:"images"`
			Charts []struct {
				Chart      string `json:"chart"`
				Repository string `json:"repository"`
				Version    string `json:"version"`
				Checksum   string `json:"sha256"`
			} `json:"charts"`
		} `json:"baseline_dependencies"`
		TransitionBaselines struct {
			Charts []struct {
				Name       string `json:"name"`
				Chart      string `json:"chart"`
				Repository string `json:"repository"`
				Version    string `json:"version"`
				Checksum   string `json:"sha256"`
			} `json:"charts"`
		} `json:"transition_baselines"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return Fixture{}, err
	}
	fixture := Fixture{Mode: FixtureCandidate, SourceSHA: plan.SourceSHA}
	for _, release := range plan.Releases {
		fixture.Inputs = append(fixture.Inputs, FixtureInput{
			Name: release.Target, Kind: "candidate", Reference: release.Version + "@" + plan.SourceSHA,
		})
	}
	for _, image := range plan.Baselines.Images {
		fixture.Inputs = append(fixture.Inputs, FixtureInput{
			Name: image.Name, Kind: "published-image", Reference: image.Repository + ":" + image.Version, Digest: image.Digest,
		})
	}
	for _, chart := range plan.Baselines.Charts {
		fixture.Inputs = append(fixture.Inputs, FixtureInput{
			Name: chart.Chart, Kind: "published-chart", Reference: chart.Repository + ":" + chart.Version, Checksum: chart.Checksum,
		})
	}
	for _, chart := range plan.TransitionBaselines.Charts {
		fixture.Inputs = append(fixture.Inputs, FixtureInput{
			Name: chart.Name, Kind: "published-chart", Reference: chart.Repository + ":" + chart.Version, Checksum: chart.Checksum,
		})
	}
	for _, image := range []struct {
		Name   string
		Prefix string
	}{
		{Name: "control-plane", Prefix: "CONTROL_PLANE"},
		{Name: "inference-gateway", Prefix: "INFERENCE_GATEWAY"},
		{Name: "control-plane-tool-runner", Prefix: "TOOL_RUNNER"},
	} {
		digest := os.Getenv(image.Prefix + "_IMAGE_DIGEST")
		if digest == "" {
			continue
		}
		repository := os.Getenv(image.Prefix + "_IMAGE_REPO")
		tag := os.Getenv(image.Prefix + "_IMAGE_TAG")
		if repository == "" || tag == "" {
			return Fixture{}, fmt.Errorf("%s candidate digest has no exact repository/tag", image.Prefix)
		}
		reference := repository + ":" + tag
		fixture.Inputs = append(fixture.Inputs, FixtureInput{
			Name: image.Name, Kind: "candidate-image", Reference: reference, Digest: digest,
		})
	}
	sort.Slice(fixture.Inputs, func(i, j int) bool {
		if fixture.Inputs[i].Kind == fixture.Inputs[j].Kind {
			return fixture.Inputs[i].Name < fixture.Inputs[j].Name
		}
		return fixture.Inputs[i].Kind < fixture.Inputs[j].Kind
	})
	if err := fixture.Validate(); err != nil {
		return Fixture{}, err
	}
	return fixture, nil
}
