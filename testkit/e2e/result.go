package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// RuntimeBundleEnv points every required scenario at the single composed,
	// checksum-verified runtime bundle manifest.
	RuntimeBundleEnv = "ITERABASE_E2E_RUNTIME_BUNDLE"
	// ExecutionPlanEnv points at the exact generated plan that selected the scenario.
	ExecutionPlanEnv = "ITERABASE_E2E_PLAN"
	// ScenarioIDEnv binds one matrix job to one compiled scenario ID.
	ScenarioIDEnv = "ITERABASE_E2E_SCENARIO_ID"
	// ResultOutputEnv is the machine-readable result path owned by the scenario.
	ResultOutputEnv = "ITERABASE_E2E_RESULT"
	// RequiredEnv enables fail-closed plan, bundle, result, and skip semantics.
	RequiredEnv = "ITERABASE_E2E_REQUIRED"
)

// RuntimeArtifact is one identity verified by the shared runtime composer.
type RuntimeArtifact struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Custody    string `json:"custody"`
	SourceSHA  string `json:"source_sha,omitempty"`
	Reference  string `json:"reference"`
	Digest     string `json:"digest,omitempty"`
	Checksum   string `json:"checksum,omitempty"`
	Path       string `json:"path,omitempty"`
	RecipeHash string `json:"recipe_sha256,omitempty"`
}

// RuntimeBundle binds a generated plan to the exact artifacts supplied to a scenario.
type RuntimeBundle struct {
	SchemaVersion   int               `json:"schema_version"`
	Intent          ExecutionIntent   `json:"intent"`
	SourceSHA       string            `json:"source_sha"`
	PlanSHA256      string            `json:"plan_sha256"`
	CatalogueSHA256 string            `json:"catalogue_sha256"`
	Artifacts       []RuntimeArtifact `json:"artifacts"`
}

// StageResult is one terminal status for one declared stage.
type StageResult struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"depends_on,omitempty"`
	Status    string   `json:"status"`
}

// ScenarioResult is the strict evidence reconciled by required CI and release gates.
type ScenarioResult struct {
	SchemaVersion    int               `json:"schema_version"`
	ScenarioID       string            `json:"scenario_id"`
	Status           string            `json:"status"`
	SourceSHA        string            `json:"source_sha"`
	PlanSHA256       string            `json:"plan_sha256"`
	CatalogueSHA256  string            `json:"catalogue_sha256"`
	RuntimeSHA256    string            `json:"runtime_bundle_sha256"`
	StageGraphSHA256 string            `json:"stage_graph_sha256"`
	FixtureMode      FixtureMode       `json:"fixture_mode"`
	Artifacts        []RuntimeArtifact `json:"artifacts"`
	Stages           []StageResult     `json:"stages"`
	CompletedAt      string            `json:"completed_at"`
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func loadRuntimeBundle(path string) (RuntimeBundle, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeBundle{}, "", fmt.Errorf("read runtime bundle: %w", err)
	}
	var bundle RuntimeBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return RuntimeBundle{}, "", fmt.Errorf("decode runtime bundle: %w", err)
	}
	if err := validateRuntimeBundle(bundle); err != nil {
		return RuntimeBundle{}, "", err
	}
	digest := sha256.Sum256(data)
	return bundle, hex.EncodeToString(digest[:]), nil
}

func validateRuntimeBundle(bundle RuntimeBundle) error {
	if bundle.SchemaVersion != 1 {
		return fmt.Errorf("runtime bundle must use schema_version 1")
	}
	if bundle.Intent != IntentPR && bundle.Intent != IntentNightly && bundle.Intent != IntentCandidate {
		return fmt.Errorf("runtime bundle has invalid intent %q", bundle.Intent)
	}
	if !fullSHA.MatchString(bundle.SourceSHA) {
		return fmt.Errorf("runtime bundle requires a full lowercase source SHA")
	}
	for label, value := range map[string]string{
		"plan_sha256": bundle.PlanSHA256, "catalogue_sha256": bundle.CatalogueSHA256,
	} {
		if !canonicalHash.MatchString(value) || strings.HasPrefix(value, "sha256:") {
			return fmt.Errorf("runtime bundle %s is not a canonical SHA-256", label)
		}
	}
	seen := make(map[string]struct{}, len(bundle.Artifacts))
	for _, artifact := range bundle.Artifacts {
		if artifact.Name == "" || artifact.Kind == "" || artifact.Custody == "" || artifact.Reference == "" {
			return fmt.Errorf("runtime artifact has incomplete identity: %+v", artifact)
		}
		if _, exists := seen[artifact.Name]; exists {
			return fmt.Errorf("runtime bundle repeats artifact %q", artifact.Name)
		}
		seen[artifact.Name] = struct{}{}
		if artifact.Custody != "published-baseline" && artifact.SourceSHA != bundle.SourceSHA {
			return fmt.Errorf("runtime artifact %q source SHA does not match the bundle", artifact.Name)
		}
		if artifact.Custody == "published-baseline" && artifact.SourceSHA != "" {
			return fmt.Errorf("published baseline %q must not claim source custody", artifact.Name)
		}
		if artifact.Digest != "" && !canonicalHash.MatchString(artifact.Digest) {
			return fmt.Errorf("runtime artifact %q has invalid digest", artifact.Name)
		}
		if artifact.Checksum != "" && !canonicalHash.MatchString(artifact.Checksum) {
			return fmt.Errorf("runtime artifact %q has invalid checksum", artifact.Name)
		}
		if artifact.RecipeHash != "" && (!canonicalHash.MatchString(artifact.RecipeHash) || strings.HasPrefix(artifact.RecipeHash, "sha256:")) {
			return fmt.Errorf("runtime artifact %q has invalid recipe hash", artifact.Name)
		}
	}
	return nil
}

func fixtureFromRuntimeBundle(bundle RuntimeBundle) Fixture {
	mode := FixtureSource
	if bundle.Intent == IntentCandidate {
		mode = FixtureCandidate
	}
	fixture := Fixture{
		Mode: mode, SourceSHA: bundle.SourceSHA, PlanSHA256: bundle.PlanSHA256,
		CatalogueSHA256: bundle.CatalogueSHA256,
	}
	for _, artifact := range bundle.Artifacts {
		fixture.Inputs = append(fixture.Inputs, FixtureInput{
			Name: artifact.Name, Kind: artifact.Kind, Custody: artifact.Custody,
			SourceSHA: artifact.SourceSHA, Reference: artifact.Reference,
			Digest: artifact.Digest, Checksum: artifact.Checksum, Path: artifact.Path,
		})
	}
	sort.Slice(fixture.Inputs, func(i, j int) bool { return fixture.Inputs[i].Name < fixture.Inputs[j].Name })
	return fixture
}

func stageGraphSHA256(stages []StageMetadata) string {
	data, err := json.Marshal(stages)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeScenarioResult(path string, result ScenarioResult) error {
	if path == "" {
		return fmt.Errorf("%s is empty", ResultOutputEnv)
	}
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
