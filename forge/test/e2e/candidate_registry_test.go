package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const candidateRegistryConfigPath = "/etc/forge/helm-registry.json"

// loginCandidateRegistry authenticates the ephemeral host to the private,
// run-addressed candidate chart namespace. The token is supplied over SSH
// stdin, stored only in Forge's isolated root-owned Helm registry config, and
// removed before the ephemeral host is destroyed.
func loginCandidateRegistry(t *testing.T, ip, keyPath string) {
	t.Helper()
	if os.Getenv("FORGE_E2E_CHART_REPOSITORY") == "" {
		return
	}
	username := os.Getenv("FORGE_E2E_REGISTRY_USERNAME")
	token := os.Getenv("FORGE_E2E_REGISTRY_TOKEN")
	if username == "" || token == "" {
		t.Fatal("candidate chart validation requires FORGE_E2E_REGISTRY_USERNAME and FORGE_E2E_REGISTRY_TOKEN")
	}
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial candidate host for registry login: %v", err)
	}
	defer client.Close()
	if output, err := sshOutput(client, "sudo install -d -m 700 /etc/forge"); err != nil {
		t.Fatalf("prepare isolated candidate registry config: %v\n%s", err, output)
	}
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("create registry login session: %v", err)
	}
	session.Stdin = strings.NewReader(token + "\n")
	command := fmt.Sprintf(
		"sudo helm --registry-config %s registry login ghcr.io --username %s --password-stdin",
		candidateShellQuote(candidateRegistryConfigPath), candidateShellQuote(username),
	)
	output, loginErr := session.CombinedOutput(command)
	session.Close()
	if loginErr != nil {
		t.Fatalf("candidate registry login failed: %v\n%s", loginErr, output)
	}
	if output, err := sshOutput(client, "sudo chmod 600 "+candidateShellQuote(candidateRegistryConfigPath)); err != nil {
		t.Fatalf("protect candidate registry config: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		cleanupClient, err := sshDial(ip, keyPath)
		if err != nil {
			t.Logf("remove candidate registry config: dial host: %v", err)
			return
		}
		defer cleanupClient.Close()
		if output, err := sshOutput(cleanupClient, "sudo rm -f "+candidateShellQuote(candidateRegistryConfigPath)); err != nil {
			t.Logf("remove candidate registry config: %v\n%s", err, output)
		}
	})
	t.Log("authenticated Forge's isolated root Helm config to the candidate chart registry")
}

// applyCandidateImagesOnHost reapplies the already-installed exact chart with
// immutable image identities while retaining the real overlay values Forge
// applied. This keeps coordinated real-machine validation on the normal Forge
// chart/release and proves the selected image union on the provisioned host.
func applyCandidateImagesOnHost(t *testing.T, ip, keyPath, release, namespace, chartRepository, chartVersion string) {
	t.Helper()
	values := make([]string, 0, 6)
	add := func(repositoryEnv, digestEnv, valuePrefix string) {
		digest := os.Getenv(digestEnv)
		if digest == "" {
			return
		}
		repository := os.Getenv(repositoryEnv)
		if repository == "" {
			t.Fatalf("%s requires %s", digestEnv, repositoryEnv)
		}
		tag := os.Getenv(strings.TrimSuffix(digestEnv, "_DIGEST") + "_TAG")
		if tag == "" {
			t.Fatalf("%s requires its immutable *_IMAGE_TAG reference", digestEnv)
		}
		values = append(values,
			valuePrefix+".repository="+repository,
			valuePrefix+".tag="+tag,
		)
	}
	add("CONTROL_PLANE_IMAGE_REPO", controlPlaneDigestEnv, "control-plane.image")
	add("INFERENCE_GATEWAY_IMAGE_REPO", inferenceGatewayDigestEnv, "inference-gateway.image")
	add("TOOL_RUNNER_IMAGE_REPO", toolRunnerDigestEnv, "control-plane.toolRunner.image")
	if len(values) == 0 {
		return
	}
	if chartRepository == "" {
		chartRepository = "oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform"
	}
	args := []string{
		"sudo", "helm",
		"--kubeconfig", "/etc/rancher/k3s/k3s.yaml",
		"--registry-config", candidateRegistryConfigPath,
		"upgrade", release, chartRepository,
		"--version", chartVersion,
		"--namespace", namespace,
		"--reuse-values", "--wait", "--timeout", "10m",
	}
	for _, value := range values {
		args = append(args, "--set-string", value)
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = candidateShellQuote(arg)
	}
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial candidate host for immutable image apply: %v", err)
	}
	defer client.Close()
	if output, err := sshOutput(client, strings.Join(quoted, " ")); err != nil {
		t.Fatalf("apply immutable candidate images: %v\n%s", err, output)
	}
	t.Log("reapplied real-machine chart with selected immutable image digests")
}

func candidateShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
