package sshprovisioner

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

const (
	k3sInstallScriptURL     = "https://raw.githubusercontent.com/k3s-io/k3s/2977c525a2e7a487886107ce4df43630ae9b03b2/install.sh"
	k3sInstallScriptSHA256  = "e5cc3b3d9dfc1662c2d9be6da5abc9a4cd317d6abc3a5ffc02e3dd3248207fee"
	fluxInstallScriptURL    = "https://raw.githubusercontent.com/fluxcd/flux2/602d14817c2cd3d6124ec948cd8c0348e2bf2b0a/install/flux.sh"
	fluxInstallScriptSHA256 = "bd7765225b731a1df952456eced0abb5dbbf5e11bc70cf6ab5fddd1476088b7e"
	helmInstallScriptURL    = "https://raw.githubusercontent.com/helm/helm/bc0114c70841e2a05c95e53e20358fb761dbfb2e/scripts/get-helm-4"
	helmInstallScriptSHA256 = "aa9943145fbc13c10ea271735cc8fdf538cebbdaeefc7ae5b021ed86ac01f30f"
	helmInstallVersion      = "v4.2.3"
	// Stable command aliases keep the fake-SSH contract readable.
	helmInstallerTempCmd = "mktemp /tmp/forge-helm-installer.XXXXXX"
	helmInstallScript    = helmInstallScriptURL
)

var canonicalSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// downloadVerifiedRemoteContent downloads one reviewed input without executing
// or extracting it, then compares the exact repository-owned SHA-256 on-host.
// A transport retry may fetch the same URL again; a byte mismatch is terminal.
func (p *SSHProvisioner) downloadVerifiedRemoteContent(ctx context.Context, prefix, url, checksum string) (string, error) {
	if !strings.HasPrefix(url, "https://") || !canonicalSHA256.MatchString(checksum) {
		return "", fmt.Errorf("invalid remote content identity for %s", prefix)
	}
	path, err := p.run(ctx, "mktemp /tmp/forge-"+prefix+".XXXXXX")
	if err != nil {
		return "", fmt.Errorf("prepare %s download: %w", prefix, err)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("prepare %s download: mktemp returned an empty path", prefix)
	}
	command := fmt.Sprintf(
		"curl -fsSL --retry 4 --retry-delay 2 --retry-all-errors --connect-timeout 10 -o %s %s",
		shellQuote(path), shellQuote(url),
	)
	if _, err := p.run(ctx, command); err != nil {
		_, _ = p.run(ctx, "rm -f "+shellQuote(path))
		return "", fmt.Errorf("download %s: %w", prefix, err)
	}
	verify := fmt.Sprintf("printf '%%s  %%s\\n' %s %s | sha256sum --check --status", shellQuote(checksum), shellQuote(path))
	if _, err := p.run(ctx, verify); err != nil {
		_, _ = p.run(ctx, "rm -f "+shellQuote(path))
		return "", fmt.Errorf("verify %s content checksum: %w", prefix, err)
	}
	return path, nil
}

func (p *SSHProvisioner) removeRemoteContent(ctx context.Context, path string) {
	_, _ = p.run(ctx, "rm -f "+shellQuote(path))
}

// downloadVerifiedHelmChart resolves a repo-index chart into a local archive,
// then verifies repository-owned bytes before Helm may inspect or apply it.
func (p *SSHProvisioner) downloadVerifiedHelmChart(ctx context.Context, repository, version, checksum string) (string, func(), error) {
	if !canonicalSHA256.MatchString(checksum) || version == "" || !strings.Contains(repository, "/") {
		return "", nil, fmt.Errorf("invalid Helm chart content identity")
	}
	directory, err := p.run(ctx, "mktemp -d /tmp/forge-chart.XXXXXX")
	if err != nil {
		return "", nil, fmt.Errorf("prepare verified Helm chart: %w", err)
	}
	directory = strings.TrimSpace(directory)
	cleanup := func() { _, _ = p.run(ctx, "sudo rm -rf "+shellQuote(directory)) }
	if directory == "" {
		cleanup()
		return "", nil, fmt.Errorf("prepare verified Helm chart: mktemp returned an empty path")
	}
	if _, err := p.run(ctx, helmCmd("pull", repository, "--version", version, "--destination", directory)); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("download verified Helm chart: %w", err)
	}
	chart := repository[strings.LastIndex(repository, "/")+1:]
	archive := directory + "/" + chart + "-" + version + ".tgz"
	verify := fmt.Sprintf("printf '%%s  %%s\\n' %s %s | sha256sum --check --status", shellQuote(checksum), shellQuote(archive))
	if _, err := p.run(ctx, verify); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("verify Helm chart content checksum: %w", err)
	}
	return archive, cleanup, nil
}
