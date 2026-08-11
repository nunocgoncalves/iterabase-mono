package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// loginCandidateRegistry authenticates the ephemeral host to the private,
// run-addressed candidate chart namespace. The token is supplied over SSH
// stdin, never in the remote command, logs, Forge config, or persisted state.
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
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("create registry login session: %v", err)
	}
	defer session.Close()
	session.Stdin = strings.NewReader(token + "\n")
	command := fmt.Sprintf("helm registry login ghcr.io --username %s --password-stdin", candidateShellQuote(username))
	if output, err := session.CombinedOutput(command); err != nil {
		t.Fatalf("candidate registry login failed: %v\n%s", err, output)
	}
	t.Log("authenticated ephemeral host to the candidate chart registry")
}

func candidateShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
