package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nunocgoncalves/iterabase-mono/forge/test/e2e/internal/kindtest"
)

const (
	controlPlaneDigestEnv     = "CONTROL_PLANE_IMAGE_DIGEST"
	inferenceGatewayDigestEnv = "INFERENCE_GATEWAY_IMAGE_DIGEST"
	toolRunnerDigestEnv       = "TOOL_RUNNER_IMAGE_DIGEST"
)

// applyCandidateImageOverrides keeps release validation on the normal chart
// surface while replacing only explicitly selected component artifacts. An
// unset value remains manifest/chart pinned; there is no floating fallback.
func applyCandidateImageOverrides(values map[string]string) {
	if repository := os.Getenv("CONTROL_PLANE_IMAGE_REPO"); repository != "" {
		values["control-plane.image.repository"] = repository
	}
	if tag := os.Getenv("CONTROL_PLANE_IMAGE_TAG"); tag != "" {
		values["control-plane.image.tag"] = tag
	}
	if repository := os.Getenv("INFERENCE_GATEWAY_IMAGE_REPO"); repository != "" {
		values["inference-gateway.image.repository"] = repository
	}
	if tag := os.Getenv("INFERENCE_GATEWAY_IMAGE_TAG"); tag != "" {
		values["inference-gateway.image.tag"] = tag
	}
}

type candidateContainerSpec struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type candidateContainerStatus struct {
	Name    string `json:"name"`
	ImageID string `json:"imageID"`
}

// assertCandidateImageDigests verifies both sides of the runtime contract: a
// Pod requested the selected index digest, and CRI reported an immutable image
// ID for that container. For multi-platform images the runtime ID may be the
// selected child-manifest digest rather than the parent index digest.
func assertCandidateImageDigests(t *testing.T, cluster *kindtest.Cluster, namespace string, digestEnvs ...string) {
	t.Helper()
	var pods struct {
		Items []struct {
			Spec struct {
				Containers     []candidateContainerSpec `json:"containers"`
				InitContainers []candidateContainerSpec `json:"initContainers"`
			} `json:"spec"`
			Status struct {
				ContainerStatuses     []candidateContainerStatus `json:"containerStatuses"`
				InitContainerStatuses []candidateContainerStatus `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	raw := cluster.Kubectl(t, "get", "pods", "-n", namespace, "-o", "json")
	if err := json.Unmarshal([]byte(raw), &pods); err != nil {
		t.Fatalf("decode candidate pod image identities: %v", err)
	}
	for _, envName := range digestEnvs {
		digest := os.Getenv(envName)
		if digest == "" {
			continue
		}
		if !strings.HasPrefix(digest, "sha256:") {
			t.Fatalf("%s=%q is not a canonical sha256 digest", envName, digest)
		}
		found := false
		for _, pod := range pods.Items {
			specs := append(pod.Spec.Containers, pod.Spec.InitContainers...)
			statuses := append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...)
			for _, spec := range specs {
				if !strings.HasSuffix(spec.Image, "@"+digest) {
					continue
				}
				for _, status := range statuses {
					if status.Name == spec.Name && strings.Contains(status.ImageID, "@sha256:") {
						found = true
						break
					}
				}
			}
		}
		if !found {
			t.Fatalf("no running container in %s requested %s=%s and reported an immutable image ID", namespace, envName, digest)
		}
		t.Logf("verified candidate request %s and its runtime image ID", digest)
	}
}
