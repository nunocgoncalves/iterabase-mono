package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	platformChartArchiveEnv  = "FORGE_E2E_PLATFORM_CHART_ARCHIVE"
	substrateChartArchiveEnv = "FORGE_E2E_SUBSTRATE_CHART_ARCHIVE"
)

// prepareCandidateChart transfers the exact Actions-retained platform and
// companion archives to the ephemeral host. Forge then gives remote Helm those
// extracted directories, so real-machine validation consumes the candidate
// bytes without publishing a persistent candidate package.
func prepareCandidateImages(t *testing.T, ip, keyPath string) {
	t.Helper()
	productArchives := []string{
		os.Getenv("FORGE_E2E_CONTROL_PLANE_IMAGE_ARCHIVE"),
		os.Getenv("FORGE_E2E_HARNESS_IMAGE_ARCHIVE"),
		os.Getenv("FORGE_E2E_TOOL_RUNNER_IMAGE_ARCHIVE"),
		os.Getenv("FORGE_E2E_INFERENCE_IMAGE_ARCHIVE"),
	}
	fixtureArchive := os.Getenv("FORGE_E2E_RUNTIME_IMAGE_ARCHIVE")
	archives := make([]string, 0, len(productArchives)+1)
	productArchiveCount := 0
	for _, archive := range productArchives {
		if archive != "" {
			productArchiveCount++
		}
	}
	if productArchiveCount != 0 && productArchiveCount != len(productArchives) {
		t.Fatal("exact source workspace candidate requires all four local product image archives")
	}
	if productArchiveCount == len(productArchives) {
		archives = append(archives, productArchives...)
	}
	if fixtureArchive != "" {
		archives = append(archives, fixtureArchive)
	}
	if len(archives) == 0 {
		return
	}
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial candidate host to transfer images: %v", err)
	}
	defer client.Close()
	for _, archive := range archives {
		source, err := os.Open(archive)
		if err != nil {
			t.Fatalf("open candidate image archive %s: %v", archive, err)
		}
		remote := filepath.Join("/tmp", filepath.Base(archive))
		session, err := client.NewSession()
		if err != nil {
			source.Close()
			t.Fatal(err)
		}
		session.Stdin = source
		output, transferErr := session.CombinedOutput("cat > " + candidateShellQuote(remote))
		session.Close()
		source.Close()
		if transferErr != nil {
			t.Fatalf("transfer candidate image %s: %v\n%s", archive, transferErr, output)
		}
		if output, err := sshOutput(client, "sudo k3s ctr images import "+candidateShellQuote(remote)+" && rm -f "+candidateShellQuote(remote)); err != nil {
			t.Fatalf("import candidate image %s: %v\n%s", archive, err, output)
		}
	}
}

func candidateShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func prepareCandidateChart(t *testing.T, ip, keyPath string) {
	t.Helper()
	platform := os.Getenv(platformChartArchiveEnv)
	substrate := os.Getenv(substrateChartArchiveEnv)
	if platform == "" || substrate == "" {
		t.Fatalf("exact platform validation requires %s and %s", platformChartArchiveEnv, substrateChartArchiveEnv)
	}

	root := fmt.Sprintf("/tmp/iterabase-release-charts-%d", os.Getpid())
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial candidate host to transfer charts: %v", err)
	}
	defer client.Close()
	if output, err := sshOutput(client, "mkdir -p "+candidateShellQuote(root)); err != nil {
		t.Fatalf("prepare remote candidate chart directory: %v\n%s", err, output)
	}

	for _, archive := range []string{platform, substrate} {
		source, err := os.Open(archive)
		if err != nil {
			t.Fatalf("open exact candidate chart %s: %v", archive, err)
		}
		remote := filepath.Join(root, filepath.Base(archive))
		session, err := client.NewSession()
		if err != nil {
			source.Close()
			t.Fatalf("create candidate chart transfer session: %v", err)
		}
		session.Stdin = source
		output, transferErr := session.CombinedOutput("cat > " + candidateShellQuote(remote))
		session.Close()
		source.Close()
		if transferErr != nil {
			t.Fatalf("transfer exact candidate chart %s: %v\n%s", archive, transferErr, output)
		}
		if output, err := sshOutput(client, "tar -xzf "+candidateShellQuote(remote)+" -C "+candidateShellQuote(root)); err != nil {
			t.Fatalf("extract exact candidate chart %s: %v\n%s", archive, err, output)
		}
	}

	t.Setenv("FORGE_E2E_CHART_REPOSITORY", filepath.Join(root, "iterabase-platform"))
	t.Cleanup(func() {
		cleanup, err := sshDial(ip, keyPath)
		if err != nil {
			t.Logf("remove remote candidate charts: dial host: %v", err)
			return
		}
		defer cleanup.Close()
		if output, err := sshOutput(cleanup, "rm -rf "+candidateShellQuote(root)); err != nil {
			t.Logf("remove remote candidate charts: %v\n%s", err, output)
		}
	})
	t.Log("transferred exact platform and certificate-substrate candidate archives to the real-machine host")
}
