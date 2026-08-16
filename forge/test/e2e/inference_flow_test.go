package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nunocgoncalves/iterabase-mono/forge/test/e2e/internal/kindtest"
)

// The portable CPU inference composition moved to control-plane's
// deployed-execution-contracts scenario under HOR-477. These helpers remain in
// Forge only for the irreducibly real digitalocean-gpu serving path.
type catalogEntry struct {
	ModelID    string `json:"model_id"`
	Available  bool   `json:"available"`
	BackendURL string `json:"backend_url"`
}

// createAPIKey POSTs /v1/api-keys as the control-plane admin to issue a
// gateway-scoped key for the given identity. Returns the full key (shown once).
func createAPIKey(t *testing.T, client *http.Client, url, adminKey, identityID, scope string) string {
	t.Helper()
	body := fmt.Sprintf(`{"identityID":%q,"name":"e2e-%s","scope":%q}`, identityID, scope, scope)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: status %d\n%s", resp.StatusCode, respBody)
	}
	var out struct {
		FullKey string `json:"fullKey"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil || out.FullKey == "" {
		t.Fatalf("api-key response missing fullKey: %s", respBody)
	}
	return out.FullKey
}

// getSecretKey reads a key from a Kubernetes Secret via kubectl + base64-decodes
// it (kubectl jsonpath returns the raw base64).
func getSecretKey(t *testing.T, c *kindtest.Cluster, namespace, name, key string) string {
	t.Helper()
	b64 := strings.TrimSpace(c.Kubectl(t, "get", "secret", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.data.%s}", key)))
	if b64 == "" {
		t.Fatalf("secret %s/%s has no key %q", namespace, name, key)
	}
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode secret %s/%s[%s]: %v", namespace, name, key, err)
	}
	return string(dec)
}

// snapshotCatalog fetches the gateway's /admin/v1/snapshot (X-Admin-Key auth)
// and returns the catalog entries + the HTTP status. Best-effort: a request or
// parse error returns (nil, 0, err) so callers can keep polling.
func snapshotCatalog(client *http.Client, baseURL, adminKey string) ([]catalogEntry, int, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/admin/v1/snapshot", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Admin-Key", adminKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var snap struct {
		Catalog []catalogEntry `json:"catalog"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, resp.StatusCode, err
	}
	return snap.Catalog, resp.StatusCode, nil
}

// chatCompletionsStatus POSTs /v1/chat/completions with the given Bearer key +
// model alias and returns (status, body). It does not fatal on non-2xx because
// callers assert both successful serving and fail-closed statuses.
func chatCompletionsStatus(t *testing.T, client *http.Client, baseURL, key, model string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post chat completions: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(respBody)
}
