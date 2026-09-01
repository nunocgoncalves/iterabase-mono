package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordFixtureEvidenceUpsertsCleanupIdentity(t *testing.T) {
	result := filepath.Join(t.TempDir(), "result.json")
	t.Setenv(RequiredEnv, "true")
	t.Setenv(ResultOutputEnv, result)
	evidence := FixtureEvidence{
		Name: "lifecycle", Capacity: "cpu", HostKeySHA256: strings.Repeat("a", 64),
		WorkspaceDevice: "/dev/disk/by-id/workspace", BootIDBefore: "boot-1", BootIDAfter: "boot-2",
	}
	if err := RecordFixtureEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	evidence.BootIDBefore, evidence.BootIDAfter = "boot-2", "boot-3"
	if err := RecordFixtureEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	records, err := fixtureEvidenceForResult()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].BootIDBefore != "boot-2" || records[0].BootIDAfter != "boot-3" {
		t.Fatalf("fixture evidence did not retain final cleanup identity: %+v", records)
	}
}

func TestRecordFixtureEvidenceRejectsAliasedOrFloatingGPUCache(t *testing.T) {
	t.Setenv(RequiredEnv, "true")
	t.Setenv(ResultOutputEnv, filepath.Join(t.TempDir(), "result.json"))
	base := FixtureEvidence{
		Name: "model-cache", Capacity: "gpu", HostKeySHA256: strings.Repeat("a", 64),
		WorkspaceDevice: "/dev/disk/by-id/workspace", BootIDBefore: "boot-1", BootIDAfter: "boot-2",
		ModelCacheDevice: "/dev/disk/by-id/cache", ModelCacheMount: "/data/hf-cache", ModelCacheUUID: "uuid",
		ModelID: "public/model", ModelRevision: strings.Repeat("b", 40), ModelContentSHA256: strings.Repeat("c", 64),
	}
	for name, mutate := range map[string]func(*FixtureEvidence){
		"aliased":  func(value *FixtureEvidence) { value.ModelCacheDevice = value.WorkspaceDevice },
		"floating": func(value *FixtureEvidence) { value.ModelRevision = "main" },
		"corrupt":  func(value *FixtureEvidence) { value.ModelContentSHA256 = "corrupt" },
		"same boot": func(value *FixtureEvidence) {
			value.BootIDAfter = value.BootIDBefore
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := RecordFixtureEvidence(value); err == nil {
				t.Fatalf("invalid fixture evidence unexpectedly passed: %+v", value)
			}
		})
	}
}
