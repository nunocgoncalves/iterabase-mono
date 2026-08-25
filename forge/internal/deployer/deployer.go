// Package deployer defines the cluster-level manifest operations interface
// (Helm + kustomize) — the testability seam for installing/upgrading/uninstalling
// Helm releases (the iterabase-platform chart, the NVIDIA GPU Operator, etc.) and
// applying kustomize directories (overlay CRD instances). The real implementation
// lives in internal/sshprovisioner (helm + k3s kubectl run on the host over SSH,
// sharing the same transport as the k3s Provisioner); tests use fakes. Lifecycle
// logic never talks to SSH, helm, or kubectl directly.
package deployer

import "context"

// ChartState is the reconciled state of the platform chart release, read for
// status reporting. A release that does not exist is {Installed: false}.
type ChartState struct {
	Installed bool   // a helm release exists for this name/namespace
	Status    string // helm status: deployed/failed/pending-upgrade/pending-install/...
	Version   string // installed chart version (semver)
}

// ApplyOpts configures a Helm release install/upgrade (helm upgrade --install).
type ApplyOpts struct {
	Release    string   // helm release name
	Repository string   // chart ref (OCI URL, e.g. oci://.../iterabase-platform, or repo/name)
	Version    string   // chart version (semver)
	Namespace  string   // target namespace (--create-namespace)
	Values     []string // --set inline values (e.g. GPU operator overrides)
	ValueFiles []string // -f value files, applied in order (later wins); overlay values
	NoWait     bool     // omit Helm --wait; used only to break the 0.2 -> 0.3 gateway-config rollout dependency
	Timeout    string   // Helm operation timeout (default 10m); long stateful conformance gates set an explicit bound
}

// Deployer abstracts cluster-level manifest operations (Helm + kustomize). One
// instance is bound to a single host (the same host the Provisioner bootstrapped
// k3s on); helm + k3s kubectl run there over SSH using the k3s kubeconfig.
type Deployer interface {
	// Apply idempotently reconciles CRDs bundled in the pinned chart artifact,
	// waits for them to become Established, then installs or upgrades the Helm
	// release (helm upgrade --install), applying -f value files (ValueFiles, in
	// order) then --set values (Values). It ensures helm is present first.
	Apply(ctx context.Context, opts ApplyOpts) error
	// EnsureRepo adds (or force-updates) a Helm repository on the host. Needed
	// for repo-based charts (e.g. the NVIDIA GPU Operator); a no-op concern for
	// OCI charts. Idempotent.
	EnsureRepo(ctx context.Context, name, url string) error
	// Status reads the helm release state. A missing release is not an error.
	Status(ctx context.Context, release, namespace string) (*ChartState, error)
	// CRDOwnedBy reports whether every CRD selected by label carries the given
	// Helm release ownership annotations. An empty selection reports false.
	CRDOwnedBy(ctx context.Context, labelSelector, release, namespace string) (bool, error)
	// CRDsAnnotated reports whether every selected CRD has annotation=value.
	// It provides a durable completion checkpoint across interrupted migrations.
	CRDsAnnotated(ctx context.Context, labelSelector, annotation, value string) (bool, error)
	// AnnotateCRDs sets annotation=value on every selected CRD.
	AnnotateCRDs(ctx context.Context, labelSelector, annotation, value string) error
	// TransferCertificateHookOwnership annotates the certificate resources that
	// platform 0.2.x created as unowned Helm hooks so the 0.3 platform release can
	// adopt them during upgrade without deleting issuers, roots, or leaf Secrets.
	TransferCertificateHookOwnership(ctx context.Context, labelSelector, release, namespace string) error
	// TransferCRDOwnership annotates CRDs selected by label for adoption by a
	// different Helm release. Used only for the platform 0.2.x -> 0.3 substrate
	// ownership hand-off; CRD contents and custom resources are left untouched.
	TransferCRDOwnership(ctx context.Context, labelSelector, release, namespace string) error
	// TransferMetalLBHookOwnership adopts the IPAddressPool and L2Advertisement
	// resources the pre-DES-HOR-511 platform created as Helm post-install hooks
	// into the platform release before it renders them as ordinary resources.
	// It annotates only this release's objects, adding release-ownership metadata
	// and dropping the stale hook annotations so Helm upgrades them in place
	// (preserving object UIDs) instead of deleting and recreating them. It is
	// idempotent and non-destructive: it changes only ownership/hook metadata and
	// is a no-op when no such objects exist.
	TransferMetalLBHookOwnership(ctx context.Context, release, namespace string) error
	// RestartDeployment rolls Deployments selected by label and waits for the
	// rollout. Used by the 0.2 -> 0.3 migration after publishing gateway config.
	RestartDeployment(ctx context.Context, labelSelector, namespace string) error
	// UninstallChart removes the Helm release. A missing release is not an error.
	UninstallChart(ctx context.Context, release, namespace string) error
	// ApplyKustomize runs `kubectl apply -k dir` against the k3s kubeconfig on
	// the host. Used for overlay CRD instances (kubectl apply -k crds/client/),
	// after the chart so the CRD kinds exist. Idempotent.
	ApplyKustomize(ctx context.Context, dir string) error
	// ApplyManifest applies a Kubernetes manifest (JSON/YAML) via `kubectl apply
	// -f -` over SSH stdin, so secret values never appear in the command string
	// or ps. Used by the secret-sync phase to ensure namespaces + materialize
	// Secrets from operator-local env vars. Idempotent (kubectl apply).
	ApplyManifest(ctx context.Context, manifest string) error
	// DeleteKustomize runs `kubectl delete -k dir` (best-effort; for destroy). A
	// missing resource is not an error.
	DeleteKustomize(ctx context.Context, dir string) error
}
