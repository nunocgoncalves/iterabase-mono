package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const controlPlaneAPIPodSelector = "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=api"

type bootstrapEvidencePodList struct {
	Items []bootstrapEvidencePod `json:"items"`
}

type bootstrapEvidencePod struct {
	Metadata bootstrapEvidenceMetadata `json:"metadata"`
	Status   bootstrapEvidenceStatus   `json:"status"`
}

type bootstrapEvidenceMetadata struct {
	Name              string                    `json:"name"`
	UID               string                    `json:"uid"`
	CreationTimestamp string                    `json:"creationTimestamp"`
	DeletionTimestamp *string                   `json:"deletionTimestamp"`
	OwnerReferences   []bootstrapOwnerReference `json:"ownerReferences"`
}

type bootstrapOwnerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

type bootstrapEvidenceStatus struct {
	Phase                 string                         `json:"phase"`
	Conditions            []bootstrapPodCondition        `json:"conditions"`
	InitContainerStatuses []bootstrapInitContainerStatus `json:"initContainerStatuses"`
}

type bootstrapPodCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type bootstrapInitContainerStatus struct {
	Name         string                  `json:"name"`
	RestartCount int32                   `json:"restartCount"`
	State        bootstrapContainerState `json:"state"`
}

type bootstrapContainerState struct {
	Running    *struct{}                     `json:"running"`
	Terminated *bootstrapContainerTerminated `json:"terminated"`
	Waiting    *bootstrapContainerWaiting    `json:"waiting"`
}

type bootstrapContainerTerminated struct {
	ExitCode int32  `json:"exitCode"`
	Reason   string `json:"reason"`
}

type bootstrapContainerWaiting struct {
	Reason string `json:"reason"`
}

func (state *deployedState) readBootstrapEvidencePods(ctx context.Context) ([]bootstrapEvidencePod, error) {
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", state.cluster.Kubeconfig,
		"get", "pods", "-n", controlPlaneNamespace, "-l", controlPlaneAPIPodSelector, "-o", "json")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list API pods for bootstrap evidence: %w", err)
	}
	var list bootstrapEvidencePodList
	if err := json.Unmarshal(output, &list); err != nil {
		return nil, fmt.Errorf("decode API pods for bootstrap evidence: %w", err)
	}
	return list.Items, nil
}

func (state *deployedState) readBootstrapKeys(ctx context.Context, excludedUIDs map[string]struct{}) (map[string]string, bootstrapEvidencePod, error) {
	pods, err := state.readBootstrapEvidencePods(ctx)
	if err != nil {
		return nil, bootstrapEvidencePod{}, err
	}
	pod, err := selectBootstrapEvidencePod(pods, excludedUIDs)
	if err != nil {
		return nil, bootstrapEvidencePod{}, err
	}
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", state.cluster.Kubeconfig,
		"logs", "-n", controlPlaneNamespace, pod.Metadata.Name, "-c", "bootstrap")
	output, err := command.Output()
	if err != nil {
		return nil, pod, fmt.Errorf("read bootstrap logs from %s: %w", pod.safeSummary(), err)
	}
	keys, err := parsePodBootstrapKeys(pod, string(output))
	if err != nil {
		return nil, pod, err
	}
	return keys, pod, nil
}

func selectBootstrapEvidencePod(pods []bootstrapEvidencePod, excludedUIDs map[string]struct{}) (bootstrapEvidencePod, error) {
	eligible := make([]bootstrapEvidencePod, 0, len(pods))
	for _, pod := range pods {
		if pod.Metadata.UID == "" {
			continue
		}
		if _, excluded := excludedUIDs[pod.Metadata.UID]; excluded {
			continue
		}
		if pod.Metadata.DeletionTimestamp != nil || !pod.ready() || pod.replicaSetOwner() == "" {
			continue
		}
		bootstrap, found := pod.bootstrapStatus()
		if !found || bootstrap.State.Terminated == nil || bootstrap.State.Terminated.ExitCode != 0 {
			continue
		}
		// A restarted init container may have emitted credentials retained in its
		// previous logs. Reject it because only the current attempt is read here.
		if bootstrap.RestartCount != 0 {
			continue
		}
		eligible = append(eligible, pod)
	}
	if len(eligible) == 1 {
		return eligible[0], nil
	}
	summaries := make([]string, 0, len(pods))
	for _, pod := range pods {
		summaries = append(summaries, pod.safeSummary())
	}
	sort.Strings(summaries)
	return bootstrapEvidencePod{}, fmt.Errorf(
		"expected exactly one new ready API pod with a successful bootstrap init container; eligible=%d excluded_uids=%d pods=[%s]",
		len(eligible), len(excludedUIDs), strings.Join(summaries, "; "),
	)
}

func bootstrapEvidencePodUIDs(pods []bootstrapEvidencePod) map[string]struct{} {
	uids := make(map[string]struct{}, len(pods))
	for _, pod := range pods {
		if pod.Metadata.UID != "" {
			uids[pod.Metadata.UID] = struct{}{}
		}
	}
	return uids
}

func parsePodBootstrapKeys(pod bootstrapEvidencePod, output string) (map[string]string, error) {
	keys, err := parseRequiredBootstrapKeys(output)
	if err == nil {
		return keys, nil
	}
	scopes := make([]string, 0, len(keys))
	for scope := range keys {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return nil, fmt.Errorf("bootstrap evidence incomplete for %s output_bytes=%d parsed_scopes=%v: %w",
		pod.safeSummary(), len(output), scopes, err)
}

func (pod bootstrapEvidencePod) ready() bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func (pod bootstrapEvidencePod) bootstrapStatus() (bootstrapInitContainerStatus, bool) {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == "bootstrap" {
			return status, true
		}
	}
	return bootstrapInitContainerStatus{}, false
}

func (pod bootstrapEvidencePod) replicaSetOwner() string {
	for _, owner := range pod.Metadata.OwnerReferences {
		if owner.Kind == "ReplicaSet" && owner.Controller != nil && *owner.Controller {
			return owner.Name
		}
	}
	return ""
}

func (pod bootstrapEvidencePod) safeSummary() string {
	bootstrap, found := pod.bootstrapStatus()
	bootstrapState := "missing"
	restartCount := int32(0)
	if found {
		restartCount = bootstrap.RestartCount
		switch {
		case bootstrap.State.Terminated != nil:
			bootstrapState = fmt.Sprintf("terminated(exit=%d,reason=%s)", bootstrap.State.Terminated.ExitCode, bootstrap.State.Terminated.Reason)
		case bootstrap.State.Waiting != nil:
			bootstrapState = "waiting(reason=" + bootstrap.State.Waiting.Reason + ")"
		case bootstrap.State.Running != nil:
			bootstrapState = "running"
		default:
			bootstrapState = "unknown"
		}
	}
	return fmt.Sprintf("pod=%q uid=%q owner=%q created=%q deleting=%t ready=%t phase=%q bootstrap=%s restart_count=%d",
		pod.Metadata.Name, pod.Metadata.UID, pod.replicaSetOwner(), pod.Metadata.CreationTimestamp,
		pod.Metadata.DeletionTimestamp != nil, pod.ready(), pod.Status.Phase, bootstrapState, restartCount)
}
