package kindtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file implements auto-resolution of the control-plane Helm chart version
// and image tag for the e2e (HOR-321). Previously both were hardcoded defaults
// that drifted from every control-plane service release, forcing a manual forge
// bump. Now:
//
//   - the chart version is resolved from the monorepo's GitHub releases
//     (highest stable <chart>-<semver> tag), and
//   - the image tag is derived from the chart's appVersion, so the deployed
//     image can never drift from the chart (the control-plane chart keeps
//     appVersion == service version == image tag, per HOR-317).
//
// CONTROL_PLANE_CHART_VERSION / CONTROL_PLANE_IMAGE_TAG remain as explicit
// overrides for pinning or local dev.

// releasesGitHubRepo returns the "owner/name" GitHub repository that publishes
// Forge and chart releases under namespaced tags such as
// "control-plane-0.2.1". FORGE_RELEASES_REPO supports forks without reviving a
// dependency on the archived standalone chart source repository.
func releasesGitHubRepo() string {
	if repository := os.Getenv("FORGE_RELEASES_REPO"); repository != "" {
		return repository
	}
	return "nunocgoncalves/iterabase-mono"
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// selectLatestChartVersion returns the highest stable chart version and its tag.
// Component tags use "<component>-v<semver>" in the shared release feed, so a
// leading v after the chart prefix must be rejected rather than normalized.
func selectLatestChartVersion(releases []githubRelease, chart string) (best, bestTag string) {
	prefix := chart + "-"
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, prefix) {
			continue
		}
		version := strings.TrimPrefix(release.TagName, prefix)
		if strings.HasPrefix(version, "v") || !looksSemver(version) {
			continue
		}
		if best == "" || compareSemver(version, best) > 0 {
			best = version
			bestTag = release.TagName
		}
	}
	return best, bestTag
}

// nextGitHubPage returns the URL marked rel="next" in GitHub's Link header.
func nextGitHubPage(linkHeader string) string {
	for _, link := range strings.Split(linkHeader, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		isNext := false
		for _, parameter := range parts[1:] {
			if strings.TrimSpace(parameter) == `rel="next"` {
				isNext = true
				break
			}
		}
		target := strings.TrimSpace(parts[0])
		if isNext && len(target) >= 2 && target[0] == '<' && target[len(target)-1] == '>' {
			return target[1 : len(target)-1]
		}
	}
	return ""
}

// listGitHubReleases follows GitHub's release pagination until the feed is
// exhausted. The shared monorepo feed contains independently versioned targets,
// so the latest release for one chart may be older than the first page.
func listGitHubReleases(client *http.Client, firstPageURL, token string) ([]githubRelease, error) {
	var releases []githubRelease
	seen := make(map[string]struct{})
	for pageURL := firstPageURL; pageURL != ""; {
		if _, ok := seen[pageURL]; ok {
			return nil, fmt.Errorf("github releases pagination repeated %q", pageURL)
		}
		seen[pageURL] = struct{}{}

		req, err := http.NewRequest(http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build request for %q: %w", pageURL, err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var page []githubRelease
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		closeErr := resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode page %q: %w", pageURL, decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close page %q: %w", pageURL, closeErr)
		}
		releases = append(releases, page...)
		pageURL = nextGitHubPage(resp.Header.Get("Link"))
	}
	return releases, nil
}

// LatestChartVersion is retained only to fail old callers loudly. HOR-476
// prohibits floating published fixtures; callers must provide an exact version.
func LatestChartVersion(t *testing.T, chart string) string {
	t.Helper()
	t.Fatalf("floating latest chart resolution for %q is prohibited; provide an exact published fixture", chart)
	return ""
}

// ChartAppVersion returns the appVersion declared by the chart's metadata,
// deriving the image tag from the chart so the deployed image can never drift
// from it. For a local chart path it runs `helm show chart <path>`; for a
// remote/OCI chart it runs `helm show chart <ref> --version <v>`.
func ChartAppVersion(t *testing.T, chartRef, version, localChart string) string {
	t.Helper()
	mustBin(t, "helm")
	args := []string{"show", "chart"}
	if localChart != "" {
		args = append(args, localChart)
	} else {
		args = append(args, chartRef)
		if version != "" {
			args = append(args, "--version", version)
		}
	}
	out := run(t, "helm", args...)
	appVer := parseAppVersion(out)
	if appVer == "" {
		t.Fatalf("could not find appVersion in `helm show chart` output:\n%s", out)
	}
	t.Logf("derived image tag from chart appVersion: %s", appVer)
	return appVer
}

// appVersionLineRe matches the top-level appVersion line of `helm show chart`
// (Chart.yaml) output. Top-level keys have no leading indentation, which keeps
// this from matching a nested field of the same name.
var appVersionLineRe = regexp.MustCompile(`(?m)^appVersion:\s*(.*)$`)

// parseAppVersion extracts the top-level appVersion scalar from Chart.yaml
// output, stripping surrounding quotes and any trailing inline comment.
func parseAppVersion(chartYAML string) string {
	m := appVersionLineRe.FindStringSubmatch(chartYAML)
	if m == nil {
		return ""
	}
	v := m[1]
	// strip an inline comment: ` # ...` (a # only starts a comment when preceded
	// by whitespace or at line start; a scalar version contains no #).
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			v = v[1 : len(v)-1]
		}
	}
	return v
}

// looksSemver reports whether v looks like a semver MAJOR.MINOR.PATCH core
// (with an optional leading "v" and optional -prerelease/+build suffix). It is a
// light pre-filter so non-version tags are ignored.
func looksSemver(v string) bool {
	core, _ := splitSemver(v)
	parts := strings.Split(core, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// ChartVersionAtLeast reports whether version is a valid chart SemVer at or
// above boundary. It shares the release resolver's comparison rules so E2E
// setup can follow versioned chart substrate boundaries without string checks.
func ChartVersionAtLeast(version, boundary string) bool {
	return looksSemver(version) && looksSemver(boundary) && compareSemver(version, boundary) >= 0
}

// compareSemver compares two semver strings (optional leading "v", optional
// -prerelease/+build). Returns -1, 0, or 1. A version without a prerelease ranks
// higher than one with a prerelease at the same MAJOR.MINOR.PATCH; prereleases
// are compared lexically (sufficient for the filtered stable set we keep).
func compareSemver(a, b string) int {
	acore, apre := splitSemver(a)
	bcore, bpre := splitSemver(b)
	if c := cmpNumParts(acore, bcore); c != 0 {
		return c
	}
	switch {
	case apre == "" && bpre != "":
		return 1
	case apre != "" && bpre == "":
		return -1
	case apre == bpre:
		return 0
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// splitSemver returns (core "MAJOR.MINOR.PATCH", prerelease) for a semver
// string, stripping a leading "v" and any +build metadata.
func splitSemver(v string) (core, pre string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "-"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// cmpNumParts compares two dot-joined numeric version cores field by field,
// treating a missing trailing field as 0 (so "1.2" == "1.2.0").
func cmpNumParts(a, b string) int {
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	n := len(ap)
	if len(bp) > n {
		n = len(bp)
	}
	for i := 0; i < n; i++ {
		var an, bn int
		if i < len(ap) {
			an, _ = strconv.Atoi(ap[i])
		}
		if i < len(bp) {
			bn, _ = strconv.Atoi(bp[i])
		}
		if an < bn {
			return -1
		}
		if an > bn {
			return 1
		}
	}
	return 0
}
