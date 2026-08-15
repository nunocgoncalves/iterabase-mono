package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

func exerciseIdentityAPIStage(t *testing.T, state *deployedState) {
	t.Helper()
	state.captureBootstrapKeys(t)

	status, jwksBody := state.request(t, http.MethodGet, "/.well-known/jwks.json", "", nil, nil)
	requireStatus(t, status, http.StatusOK, jwksBody)
	var keys jwkSet
	mustDecode(t, jwksBody, &keys)
	if len(keys.Keys) == 0 {
		t.Fatal("deployed JWKS has no signing key")
	}

	tokenRequest := map[string]any{"provider": "teams", "type": "user", "externalID": "aad:deployed-alice"}
	status, body := state.requestJSON(t, http.MethodPost, "/v1/token", "", tokenRequest)
	requireStatus(t, status, http.StatusUnauthorized, body)
	status, body = state.requestJSON(t, http.MethodPost, "/v1/token", state.adminKey, tokenRequest)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = state.request(t, http.MethodGet, "/v1/users", state.tokenKey, nil, nil)
	requireStatus(t, status, http.StatusForbidden, body)

	identity := `apiVersion: platform.iterabase.com/v1alpha1
kind: IdentityMapping
metadata:
  name: deployed-alice
  namespace: iterabase-system
spec:
  identity:
    kind: user
    displayName: Alice Deployed
  bindings:
    - provider: teams
      type: user
      externalID: aad:deployed-alice
`
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "identity.yaml", identity))
	state.kubectl(t, 2*time.Minute, "wait", "--for=jsonpath={.status.ready}=true", "identitymapping/deployed-alice",
		"-n", controlPlaneNamespace, "--timeout=90s")

	status, body = state.requestJSON(t, http.MethodPost, "/v1/token", state.tokenKey, tokenRequest)
	requireStatus(t, status, http.StatusOK, body)
	var delegated struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	mustDecode(t, body, &delegated)
	if delegated.AccessToken == "" || delegated.TokenType != "Bearer" || delegated.ExpiresIn <= 0 {
		t.Fatalf("invalid delegated-token response: token=%v type=%q expires=%d", delegated.AccessToken != "", delegated.TokenType, delegated.ExpiresIn)
	}
	state.redactor.Add(delegated.AccessToken)
	claims, err := verifyRS256(delegated.AccessToken, keys)
	if err != nil {
		t.Fatalf("verify delegated RS256 token: %v", err)
	}
	if subject, _ := claims["sub"].(string); subject == "" {
		t.Fatalf("delegated token has no subject: %v", claims)
	}

	state.createWorkIdentity(t, "deployed-operator@local")
	status, body = state.request(t, http.MethodGet, "/v1/work-items", state.adminKey, nil, nil)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = state.request(t, http.MethodGet, "/v1/work-items", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	assertCustomerSafeJSON(t, body)

	oldAdminKey, oldTokenKey := state.adminKey, state.tokenKey
	state.restartAPI(t)
	status, body = state.request(t, http.MethodGet, "/v1/users", oldAdminKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	status, body = state.requestJSON(t, http.MethodPost, "/v1/token", oldTokenKey, tokenRequest)
	requireStatus(t, status, http.StatusOK, body)
	status, restartedJWKS := state.request(t, http.MethodGet, "/.well-known/jwks.json", "", nil, nil)
	requireStatus(t, status, http.StatusOK, restartedJWKS)
	if string(restartedJWKS) != string(jwksBody) {
		t.Fatal("JWKS changed across API process restart")
	}

	state.kubectl(t, 30*time.Second, "delete", "identitymapping/deployed-alice", "-n", controlPlaneNamespace)
	status, body = state.requestJSON(t, http.MethodPost, "/v1/token", oldTokenKey, tokenRequest)
	requireStatus(t, status, http.StatusForbidden, body)
}
