package e2e

import "os"

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
