package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ScopeChecker validates an overlay git token has access to the repo's host
// (GitHub PAT scope check). The real impl calls the GitHub API; tests use a fake.
type ScopeChecker interface {
	Check(ctx context.Context, token []byte, repo string) error
}

// githubScopeChecker validates a GitHub token via GET /user (401 => invalid) and
// warns if the classic-PAT X-OAuth-Scopes header lacks "repo". GitHub App and
// Actions installation tokens can return 403 for /user; in that case the exact
// repository API must confirm access before the token is accepted. Non-GitHub
// repos skip the check. The clone remains the final repository-access check.
type githubScopeChecker struct {
	client     *http.Client
	apiBaseURL string
}

func (g githubScopeChecker) Check(ctx context.Context, token []byte, repo string) error {
	if !strings.HasPrefix(repo, "https://github.com/") {
		return nil // scope check is GitHub-only
	}
	apiBaseURL := strings.TrimSuffix(g.apiBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/user", nil)
	if err != nil {
		return fmt.Errorf("token scope check: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("token scope check: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("overlay token is invalid or expired (GitHub returned 401)")
	case resp.StatusCode == http.StatusForbidden:
		if err := g.checkRepositoryAccess(ctx, apiBaseURL, token, repo); err != nil {
			return fmt.Errorf("overlay token repository access check after GitHub /user returned 403: %w", err)
		}
		fmt.Fprintln(os.Stderr, "warning: GitHub token cannot use /user; exact overlay repository access was verified")
		return nil
	case resp.StatusCode >= 400:
		return fmt.Errorf("overlay token scope check: GitHub returned %d", resp.StatusCode)
	}
	if scopes := resp.Header.Get("X-OAuth-Scopes"); scopes != "" && !strings.Contains(scopes, "repo") {
		fmt.Fprintf(os.Stderr, "warning: overlay token scopes [%s] lack 'repo'; a private overlay clone may fail\n", scopes)
	}
	return nil
}

func (g githubScopeChecker) checkRepositoryAccess(ctx context.Context, apiBaseURL string, token []byte, repo string) error {
	repositoryPath := strings.TrimSuffix(strings.TrimPrefix(repo, "https://github.com/"), ".git")
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("cannot derive exact GitHub repository from %q", repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/repos/"+parts[0]+"/"+parts[1], nil)
	if err != nil {
		return fmt.Errorf("build repository access request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("repository access request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned %d for %s/%s", resp.StatusCode, parts[0], parts[1])
	}
	return nil
}

// resolveOverlayToken determines the overlay git token:
//   - FORGE_OVERLAY_TOKEN env var (non-interactive) wins; its scopes are checked.
//   - Otherwise, for an https repo when interactive (TTY), prompt (non-echo;
//     empty => public repo, tokenless). A prompted token's scopes are checked.
//   - file:// repos, or https with no env var + non-interactive (CI), need no
//     token (public/CI proceeds tokenless).
func resolveOverlayToken(ctx context.Context, repo, envToken string, interactive bool, tp passwordPrompter, sc ScopeChecker) ([]byte, error) {
	if envToken != "" {
		tok := []byte(envToken)
		if err := sc.Check(ctx, tok, repo); err != nil {
			return nil, err
		}
		return tok, nil
	}
	if !strings.HasPrefix(repo, "https://") {
		return nil, nil // file:// needs no token
	}
	if !interactive || tp == nil {
		return nil, nil // CI / non-interactive proceeds tokenless
	}
	tok, err := tp.Prompt("Overlay repo token (enter for a public repo)")
	if err != nil {
		return nil, fmt.Errorf("read overlay token: %w", err)
	}
	if len(tok) == 0 {
		return nil, nil // empty => public repo, tokenless
	}
	if err := sc.Check(ctx, tok, repo); err != nil {
		return nil, err
	}
	return tok, nil
}

// newGithubScopeChecker builds the production GitHub scope checker.
func newGithubScopeChecker() ScopeChecker {
	return githubScopeChecker{client: &http.Client{Timeout: 15 * time.Second}, apiBaseURL: "https://api.github.com"}
}
