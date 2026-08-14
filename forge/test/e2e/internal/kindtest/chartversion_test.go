package kindtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Unit tests for chart metadata parsing and the historical release-feed parser.
// Runtime floating-latest resolution now fails closed; exact fixtures supply the
// version consumed by ChartAppVersion. Run with: make test-e2e-unit.

func TestParseAppVersion(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"bare", "apiVersion: v2\nappVersion: 0.0.2\n", "0.0.2"},
		{"double-quoted", "apiVersion: v2\nappVersion: \"0.0.2\"\n", "0.0.2"},
		{"single-quoted", "apiVersion: v2\nappVersion: '0.0.2'\n", "0.0.2"},
		{"leading-v", "appVersion: v0.0.2\n", "v0.0.2"},
		{"inline-comment", "appVersion: 0.0.2 # service version\n", "0.0.2"},
		{"quoted-inline-comment", "appVersion: \"0.0.2\" # svc\n", "0.0.2"},
		{"trailing-spaces", "appVersion: 0.0.2   \n", "0.0.2"},
		{"missing", "apiVersion: v2\nname: control-plane\n", ""},
		{"nested-not-matched", "appVersion: top\nsub:\n  appVersion: 1.2.3\n", "top"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseAppVersion(c.yaml); got != c.want {
				t.Errorf("parseAppVersion(%q) = %q, want %q", c.yaml, got, c.want)
			}
		})
	}
}

func TestReleasesGitHubRepo(t *testing.T) {
	t.Setenv("FORGE_RELEASES_REPO", "")
	if got := releasesGitHubRepo(); got != "nunocgoncalves/iterabase-mono" {
		t.Fatalf("releasesGitHubRepo() = %q, want canonical monorepo", got)
	}

	t.Setenv("FORGE_RELEASES_REPO", "example/fork")
	if got := releasesGitHubRepo(); got != "example/fork" {
		t.Fatalf("releasesGitHubRepo() = %q, want fork override", got)
	}
}

func TestLooksSemver(t *testing.T) {
	good := []string{"0.2.1", "v0.2.1", "1.0.0", "10.20.30", "0.2.1-rc.1", "v1.2.3+build", "1.2"}
	bad := []string{"", "v", "1", "x.y.z", "0.2.x", "control-plane-0.2.1", "latest", "0.2.1.4"}
	for _, v := range good {
		if !looksSemver(v) {
			t.Errorf("looksSemver(%q) = false, want true", v)
		}
	}
	for _, v := range bad {
		if looksSemver(v) {
			t.Errorf("looksSemver(%q) = true, want false", v)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int // -1, 0, 1
	}{
		{"0.2.1", "0.2.2", -1},
		{"0.2.2", "0.2.1", 1},
		{"0.2.1", "0.2.1", 0},
		{"0.2.10", "0.2.9", 1},  // numeric, not lexical
		{"0.3.0", "0.2.99", 1},  // minor beats patch
		{"1.0.0", "0.99.99", 1}, // major beats all
		{"v0.2.1", "0.2.1", 0},  // leading v ignored
		{"1.2", "1.2.0", 0},     // missing patch == 0
		{"1.2.0", "1.2", 0},
		{"0.2.1+build", "0.2.1", 0}, // build metadata ignored
		{"0.2.1", "0.3.0-rc.1", -1}, // stable 0.2.1 < 0.3.0 (core wins)
		{"0.3.0", "0.3.0-rc.1", 1},  // stable ranks above its prerelease
		{"0.3.0-rc.1", "0.3.0", -1},
		{"0.3.0-rc.1", "0.3.0-rc.2", -1}, // prerelease compared lexically
	}
	for _, c := range cases {
		got := compareSemver(c.a, c.b)
		if got < 0 {
			got = -1
		} else if got > 0 {
			got = 1
		}
		if got != c.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestChartVersionAtLeast(t *testing.T) {
	if !ChartVersionAtLeast("0.3.0", "0.3.0") {
		t.Fatal("boundary version must match")
	}
	if !ChartVersionAtLeast("0.3.1", "0.3.0") {
		t.Fatal("newer version must match")
	}
	if ChartVersionAtLeast("0.2.2", "0.3.0") {
		t.Fatal("older version must not match")
	}
	if ChartVersionAtLeast("latest", "0.3.0") {
		t.Fatal("invalid version must not match")
	}
}

func TestSelectLatestChartVersionUsesExactChartTagNamespace(t *testing.T) {
	releases := []githubRelease{
		{TagName: "control-plane-0.2.1"},
		{TagName: "control-plane-v9.0.0"}, // component release, not chart release
		{TagName: "control-plane-0.2.10"},
		{TagName: "control-plane-0.3.0-rc.1", Prerelease: true},
		{TagName: "control-plane-9.0.0", Draft: true},
		{TagName: "some-other-chart-9.9.9"},
		{TagName: "control-plane-latest"},
	}

	version, tag := selectLatestChartVersion(releases, "control-plane")
	if version != "0.2.10" || tag != "control-plane-0.2.10" {
		t.Fatalf("selected version %q from tag %q, want chart version 0.2.10", version, tag)
	}
}

func TestListGitHubReleasesFindsChartAfterMoreThan100NewerNonMatchingReleases(t *testing.T) {
	nonMatching := make([]githubRelease, 199)
	for i := range nonMatching {
		nonMatching[i] = githubRelease{TagName: fmt.Sprintf("forge-v0.8.%d", i+3)}
	}

	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		var page []githubRelease
		switch r.URL.Query().Get("page") {
		case "":
			page = nonMatching[:100]
			w.Header().Set("Link", fmt.Sprintf(`<%s/releases?per_page=100&page=2>; rel="next"`, server.URL))
		case "2":
			page = append(page, nonMatching[100:]...)
			page = append(page, githubRelease{TagName: "control-plane-0.4.7"})
			w.Header().Set("Link", fmt.Sprintf(`<%s/releases?per_page=100&page=3>; rel="next"`, server.URL))
		case "3":
			page = []githubRelease{{TagName: "control-plane-0.4.8"}}
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(page); err != nil {
			t.Errorf("encode release page: %v", err)
		}
	}))
	defer server.Close()

	releases, err := listGitHubReleases(server.Client(), server.URL+"/releases?per_page=100", "")
	if err != nil {
		t.Fatalf("listGitHubReleases() error = %v", err)
	}
	if requests != 3 {
		t.Fatalf("listGitHubReleases() made %d requests, want 3", requests)
	}
	version, tag := selectLatestChartVersion(releases, "control-plane")
	if version != "0.4.8" || tag != "control-plane-0.4.8" {
		t.Fatalf("selected version %q from tag %q, want paginated chart version 0.4.8", version, tag)
	}
}
