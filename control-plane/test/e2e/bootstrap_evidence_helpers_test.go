package e2e_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestSelectBootstrapEvidencePodUsesExactNewSuccessfulPod(t *testing.T) {
	old := successfulBootstrapEvidencePod("api-old", "uid-old")
	newPod := successfulBootstrapEvidencePod("api-new", "uid-new")
	terminating := successfulBootstrapEvidencePod("api-terminating", "uid-terminating")
	deletingAt := "2026-08-16T19:00:00Z"
	terminating.Metadata.DeletionTimestamp = &deletingAt

	for name, pods := range map[string][]bootstrapEvidencePod{
		"old-first": {old, terminating, newPod},
		"new-first": {newPod, old, terminating},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := selectBootstrapEvidencePod(pods, map[string]struct{}{"uid-old": {}})
			if err != nil {
				t.Fatal(err)
			}
			if selected.Metadata.UID != "uid-new" {
				t.Fatalf("selected pod UID=%q want uid-new", selected.Metadata.UID)
			}
		})
	}
}

func TestSelectBootstrapEvidencePodRejectsStaleOrIncompleteCandidates(t *testing.T) {
	deletingAt := "2026-08-16T19:00:00Z"
	terminating := successfulBootstrapEvidencePod("api-terminating", "uid-terminating")
	terminating.Metadata.DeletionTimestamp = &deletingAt
	failed := successfulBootstrapEvidencePod("api-failed", "uid-failed")
	failed.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 1
	failed.Status.InitContainerStatuses[0].State.Terminated.Reason = "Error"
	waiting := successfulBootstrapEvidencePod("api-waiting", "uid-waiting")
	waiting.Status.InitContainerStatuses[0].State.Terminated = nil
	waiting.Status.InitContainerStatuses[0].State.Waiting = &bootstrapContainerWaiting{Reason: "PodInitializing"}

	for name, testCase := range map[string]struct {
		pods     []bootstrapEvidencePod
		excluded map[string]struct{}
		want     string
	}{
		"stale": {
			pods:     []bootstrapEvidencePod{successfulBootstrapEvidencePod("api-old", "uid-old")},
			excluded: map[string]struct{}{"uid-old": {}}, want: "excluded_uids=1",
		},
		"terminating":     {pods: []bootstrapEvidencePod{terminating}, want: "deleting=true"},
		"failed-init":     {pods: []bootstrapEvidencePod{failed}, want: "terminated(exit=1,reason=Error)"},
		"incomplete-init": {pods: []bootstrapEvidencePod{waiting}, want: "waiting(reason=PodInitializing)"},
		"ambiguous": {
			pods: []bootstrapEvidencePod{
				successfulBootstrapEvidencePod("api-new-a", "uid-new-a"),
				successfulBootstrapEvidencePod("api-new-b", "uid-new-b"),
			},
			want: "eligible=2",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := selectBootstrapEvidencePod(testCase.pods, testCase.excluded)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("selection error=%v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestParsePodBootstrapKeysReportsSafeMetadataOnly(t *testing.T) {
	pod := successfulBootstrapEvidencePod("api-new", "uid-new")
	secret := "cp-admin-must-not-leak"
	output := fmt.Sprintf("Admin API key (scope=admin): %s\n", secret)
	_, err := parsePodBootstrapKeys(pod, output)
	if err == nil {
		t.Fatal("incomplete bootstrap output unexpectedly accepted")
	}
	message := err.Error()
	if strings.Contains(message, secret) || strings.Contains(message, output) {
		t.Fatalf("bootstrap error leaked credential output: %s", message)
	}
	for _, want := range []string{
		`pod="api-new"`, `uid="uid-new"`, `owner="api-rs"`, "ready=true",
		"bootstrap=terminated(exit=0,reason=Completed)", fmt.Sprintf("output_bytes=%d", len(output)),
		"parsed_scopes=[admin]",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("bootstrap error %q does not contain %q", message, want)
		}
	}
}

func successfulBootstrapEvidencePod(name, uid string) bootstrapEvidencePod {
	controller := true
	pod := bootstrapEvidencePod{
		Metadata: bootstrapEvidenceMetadata{
			Name: name, UID: uid, CreationTimestamp: "2026-08-16T18:00:00Z",
			OwnerReferences: []bootstrapOwnerReference{{
				Kind: "ReplicaSet", Name: "api-rs", UID: "rs-uid", Controller: &controller,
			}},
		},
		Status: bootstrapEvidenceStatus{
			Phase:      "Running",
			Conditions: []bootstrapPodCondition{{Type: "Ready", Status: "True"}},
			InitContainerStatuses: []bootstrapInitContainerStatus{{
				Name: "bootstrap",
				State: bootstrapContainerState{Terminated: &bootstrapContainerTerminated{
					ExitCode: 0, Reason: "Completed",
				}},
			}},
		},
	}
	return pod
}
