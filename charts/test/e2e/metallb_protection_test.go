package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// metalLBCRDNames is the exact MetalLB 0.16.1 CRD set owned as umbrella template
// resources (DES-HOR-511-03).
var metalLBCRDNames = []string{
	"bfdprofiles.metallb.io",
	"bgpadvertisements.metallb.io",
	"bgppeers.metallb.io",
	"communities.metallb.io",
	"configurationstates.metallb.io",
	"ipaddresspools.metallb.io",
	"l2advertisements.metallb.io",
	"servicebgpstatuses.metallb.io",
	"servicel2statuses.metallb.io",
}

func TestSelectMetalLBCRDs(t *testing.T) {
	in := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\nspec:\n  group: metallb.io\n---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: agentpools.platform.iterabase.com\nspec:\n  group: platform.iterabase.com\n"
	out, err := selectMetalLBCRDs(in)
	if err != nil {
		t.Fatalf("selectMetalLBCRDs: %v", err)
	}
	if !strings.Contains(out, "ipaddresspools.metallb.io") {
		t.Fatalf("selectMetalLBCRDs dropped the MetalLB CRD:\n%s", out)
	}
	if strings.Contains(out, "agentpools.platform.iterabase.com") {
		t.Fatalf("selectMetalLBCRDs kept a non-MetalLB CRD:\n%s", out)
	}
	// Empty input is the empty string (not a stray newline) so the pre-apply
	// emptiness guard triggers when MetalLB is disabled.
	empty, err := selectMetalLBCRDs("")
	if err != nil {
		t.Fatalf("selectMetalLBCRDs(\"\"): %v", err)
	}
	if empty != "" {
		t.Fatalf("selectMetalLBCRDs(\"\") = %q, want \"\"", empty)
	}
}

func TestUnitMetalLBCRDsAreDeletionProtected(t *testing.T) {
	chartsRoot := os.Getenv("ITERABASE_CHARTS_ROOT")
	if chartsRoot == "" {
		chartsRoot = filepath.Join("..", "..")
	}
	tmpl, err := os.ReadFile(filepath.Join(chartsRoot, "charts", "iterabase-platform", "templates", "metallb-crds.yaml"))
	if err != nil {
		t.Fatalf("read metallb-crds template: %v", err)
	}
	source := string(tmpl)

	// The template is gated by metallb.enabled, so disabling MetalLB removes the
	// CRDs from the render; the keep annotation above is the compensation guard,
	// not silent CRD-data loss.
	if !strings.Contains(source, "{{- if .Values.metallb.enabled }}") || !strings.Contains(source, "{{- end }}") {
		t.Fatalf("metallb-crds template must be gated by metallb.enabled")
	}

	// Parse each CRD block and assert the exact MetalLB set, each with the
	// deletion-protection annotation so disable, uninstall, upgrade, and rollback
	// never delete the CRDs or their custom-resource data.
	parts := strings.Split(source, "kind: CustomResourceDefinition\n")
	blocks := parts[1:] // each starts with "metadata:\n..." (parts[0] is the file head)
	if len(blocks) != len(metalLBCRDNames) {
		t.Fatalf("metallb-crds template contains %d CRD blocks, want %d", len(blocks), len(metalLBCRDNames))
	}
	found := map[string]string{}
	for _, block := range blocks {
		specBoundary := strings.Index(block, "\nspec:")
		var header struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(block[:specBoundary]), &header); err != nil {
			t.Fatalf("decode CRD block metadata: %v", err)
		}
		found[header.Metadata.Name] = block
	}
	for _, name := range metalLBCRDNames {
		block, ok := found[name]
		if !ok {
			t.Fatalf("MetalLB CRD %q missing from metallb-crds template", name)
		}
		if !strings.Contains(block, "helm.sh/resource-policy: keep") {
			t.Fatalf("MetalLB CRD %s lacks helm.sh/resource-policy: keep:\n%s", name, block)
		}
	}
}

func TestUnitMarkRenderedCRDsOwnedAddsReleaseOwnership(t *testing.T) {
	in := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n  annotations:\n    helm.sh/resource-policy: keep\n    controller-gen.kubebuilder.io/version: v0.19.0\nspec:\n  group: metallb.io\n---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: observability.example.com\n  annotations:\n    controller-gen.kubebuilder.io/version: v0.19.0\nspec:\n  group: example.com\n"
	out, err := markRenderedCRDsOwned(in, "iterabase", "iterabase-system")
	if err != nil {
		t.Fatalf("markRenderedCRDsOwned: %v", err)
	}
	for _, want := range []string{
		"meta.helm.sh/release-name: iterabase",
		"meta.helm.sh/release-namespace: iterabase-system",
		"app.kubernetes.io/managed-by: Helm",
		// existing metadata (including the deletion-protection annotation) is preserved
		"helm.sh/resource-policy: keep",
		"controller-gen.kubebuilder.io/version: v0.19.0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("marked CRD missing %q:\n%s", want, out)
		}
	}
	// Strict founder scope: a non-MetalLB rendered CRD stays in the pre-apply set
	// (so it is still established before Helm) but is NOT marked Helm-adoptable.
	if !strings.Contains(out, "name: observability.example.com") {
		t.Fatalf("non-MetalLB rendered CRD dropped from pre-apply set:\n%s", out)
	}
	if strings.Count(out, "meta.helm.sh/release-name: iterabase") != 1 {
		t.Fatalf("non-MetalLB CRD was incorrectly marked Helm-adoptable:\n%s", out)
	}
	// Idempotent: re-marking the already-marked output is a no-op.
	out2, err := markRenderedCRDsOwned(out, "iterabase", "iterabase-system")
	if err != nil {
		t.Fatalf("markRenderedCRDsOwned (idempotent): %v", err)
	}
	if out2 != out {
		t.Fatalf("markRenderedCRDsOwned not idempotent:\nfirst:\n%s\nsecond:\n%s", out, out2)
	}
}
