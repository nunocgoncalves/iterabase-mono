package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	fixtureModeEnv             = "ITERABASE_E2E_FIXTURE_MODE"
	fixtureSourceSHAEnv        = "ITERABASE_E2E_SOURCE_SHA"
	fixtureSourceDirtyEnv      = "ITERABASE_E2E_SOURCE_DIRTY"
	fixtureSourceInputsFileEnv = "ITERABASE_E2E_SOURCE_INPUTS"
	fixturePublishedFileEnv    = "ITERABASE_E2E_PUBLISHED_FIXTURE"
)

// Fixture records the exact source, candidate, or published inputs used by one
// suite execution.
type Fixture struct {
	Mode            FixtureMode    `json:"mode"`
	SourceSHA       string         `json:"source_sha,omitempty"`
	Dirty           bool           `json:"dirty,omitempty"`
	PlanSHA256      string         `json:"plan_sha256,omitempty"`
	CatalogueSHA256 string         `json:"catalogue_sha256,omitempty"`
	Inputs          []FixtureInput `json:"inputs,omitempty"`
}

// FixtureInput records one immutable runtime input. Reference is a semantic
// version, full source identity, or immutable path; Digest/Checksum strengthen
// identities when the artifact format supports them.
type FixtureInput struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Custody      string `json:"custody,omitempty"`
	SourceSHA    string `json:"source_sha,omitempty"`
	Reference    string `json:"reference"`
	Digest       string `json:"digest,omitempty"`
	ConfigDigest string `json:"config_digest,omitempty"`
	Checksum     string `json:"checksum,omitempty"`
	Path         string `json:"path,omitempty"`
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
		if input.ConfigDigest != "" && !canonicalHash.MatchString(input.ConfigDigest) {
			return fmt.Errorf("fixture input %q has invalid config digest %q", input.Name, input.ConfigDigest)
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
	if path := os.Getenv(RuntimeBundleEnv); path != "" {
		bundle, _, err := loadRuntimeBundle(path)
		if err != nil {
			t.Fatalf("load composed runtime bundle: %v", err)
		}
		fixture := fixtureFromRuntimeBundle(bundle)
		if err := fixture.Validate(); err != nil {
			t.Fatalf("invalid composed runtime fixture: %v", err)
		}
		return fixture
	}
	if os.Getenv(RequiredEnv) == "true" {
		t.Fatalf("required E2E requires %s", RuntimeBundleEnv)
	}
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
		t.Fatalf("%s=candidate requires the shared %s", fixtureModeEnv, RuntimeBundleEnv)
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
