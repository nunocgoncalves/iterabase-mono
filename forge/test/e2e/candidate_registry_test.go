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

func candidateShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
