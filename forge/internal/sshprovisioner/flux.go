package sshprovisioner

import (
	"context"
	"fmt"
	"strings"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/fluxer"
)

// Compile-time assertion: SSHProvisioner implements fluxer.Fluxer.
var _ fluxer.Fluxer = (*SSHProvisioner)(nil)

const (
	fluxHelmControllerImage         = "ghcr.io/fluxcd/helm-controller:v1.1.0@sha256:4c75ca6c24ceb1f1bd7e935d9287a93e4f925c512f206763ec5a47de3ef3ff48"
	fluxKustomizeControllerImage    = "ghcr.io/fluxcd/kustomize-controller:v1.4.0@sha256:e3b0cf847e9cdf47b19af0fbcfe22786b80b598e0caeea8b6d2a5f9c26a48a24"
	fluxNotificationControllerImage = "ghcr.io/fluxcd/notification-controller:v1.4.0@sha256:425309a159b15e07f7d97622effc79bc432a37ed55289dd465d37fa217a92a7d"
	fluxSourceControllerImage       = "ghcr.io/fluxcd/source-controller:v1.4.1@sha256:3c5f0f022f990ffc0daf00e5b199548fc0fa6e7119e972318f0267081a332963"
)

// EnsureFlux installs only the repository-reviewed Flux executable, exports its
// manifests without contacting the cluster, substitutes every controller image
// with the reviewed digest, and applies that closed runtime set through k3s.
func (p *SSHProvisioner) EnsureFlux(ctx context.Context, version string) error {
	tool, installed, identityErr := p.installedReviewedTool(ctx, "flux", "/usr/local/bin/flux")
	if identityErr != nil || !installed || tool.version != version {
		if _, err := p.installReviewedRemoteTool(ctx, "flux", version); err != nil {
			return fmt.Errorf("install reviewed Flux executable: %w", err)
		}
	}
	replacements := [][2]string{
		{strings.SplitN(fluxHelmControllerImage, "@", 2)[0], fluxHelmControllerImage},
		{strings.SplitN(fluxKustomizeControllerImage, "@", 2)[0], fluxKustomizeControllerImage},
		{strings.SplitN(fluxNotificationControllerImage, "@", 2)[0], fluxNotificationControllerImage},
		{strings.SplitN(fluxSourceControllerImage, "@", 2)[0], fluxSourceControllerImage},
	}
	filter := "cat"
	for _, replacement := range replacements {
		filter += " | sed " + shellQuote("s#"+replacement[0]+"#"+replacement[1]+"#g")
	}
	manifest, err := p.run(ctx, "mktemp /tmp/forge-flux-runtime.XXXXXX")
	if err != nil {
		return fmt.Errorf("prepare reviewed Flux runtime: %w", err)
	}
	manifest = strings.TrimSpace(manifest)
	defer p.removeRemoteContent(ctx, manifest)
	pipeline := fmt.Sprintf(
		"sudo /usr/local/bin/flux install --export --version=%s | %s > %s && "+
			"test \"$(grep -Ec '^[[:space:]]+image:' %s)\" -eq %d && "+
			"! grep -E '^[[:space:]]+image:' %s | grep -v '@sha256:' && "+
			"sudo /usr/local/bin/k3s kubectl apply -f %s",
		shellQuote(version), filter, shellQuote(manifest), shellQuote(manifest), len(replacements), shellQuote(manifest), shellQuote(manifest),
	)
	if _, err := p.run(ctx, "bash -o pipefail -c "+shellQuote(pipeline)); err != nil {
		return fmt.Errorf("apply reviewed Flux runtime: %w", err)
	}
	return nil
}

// UninstallFlux implements fluxer.Fluxer. Best-effort `flux uninstall`
// (non-interactive, --silent skips the confirmation prompt + suppresses info
// output) — removes Flux components + CRDs + flux-system resources. A missing
// Flux install (or absent flux CLI) is not an error so destroy always proceeds
// to k3s removal (which wipes the cluster regardless).
func (p *SSHProvisioner) UninstallFlux(ctx context.Context) error {
	_, installed, err := p.installedReviewedTool(ctx, "flux", "/usr/local/bin/flux")
	if err != nil || !installed {
		return nil // never execute an absent or unreviewed root-level binary
	}
	_, _ = p.run(ctx, fluxCmd("uninstall", "--silent")) // best-effort
	return nil
}

// GitRepositoryArtifact implements fluxer.Fluxer. It reports the Ready
// condition plus Flux's exact materialized revision and content digest
// (HOR-397). Forge never downloads or parses tool code. Missing/not-yet-ready
// status remains a tolerated zero value for lifecycle polling; command and API
// failures are returned so operators see their cause immediately.
func (p *SSHProvisioner) GitRepositoryArtifact(ctx context.Context, name string) (fluxer.GitRepositoryArtifact, error) {
	out, err := p.run(ctx, kubectlCmd("get", "gitrepository", "-n", "flux-system", name,
		"--ignore-not-found=true",
		"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}{"\t"}{.status.artifact.revision}{"\t"}{.status.artifact.digest}`))
	if err != nil {
		return fluxer.GitRepositoryArtifact{}, fmt.Errorf("get Flux GitRepository %q: %w", name, err)
	}
	parts := strings.Split(strings.TrimSpace(out), "\t")
	if len(parts) == 0 || parts[0] == "" {
		return fluxer.GitRepositoryArtifact{}, nil
	}
	artifact := fluxer.GitRepositoryArtifact{Ready: parts[0] == "True"}
	if len(parts) > 1 {
		artifact.Revision = parts[1]
	}
	if len(parts) > 2 {
		artifact.Digest = parts[2]
	}
	return artifact, nil
}

// fluxCmd builds a sudo flux command targeting the k3s kubeconfig on the host.
// The flux CLI has no --kubeconfig flag; it reads KUBECONFIG, so it is exported
// via env. sudo runs as root (the k3s kubeconfig at /etc/rancher/k3s/k3s.yaml is
// root-owned 0600), and the in-process server URL (127.0.0.1:6443) is reachable
// on the host — no rewrite needed (unlike off-host kubeconfig use).
func fluxCmd(args ...string) string {
	return joinArgs(append([]string{"sudo", "env", "KUBECONFIG=" + k3sKubeconfigPath, "/usr/local/bin/flux"}, args...))
}
