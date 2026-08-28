package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	platformChartArchiveEnv    = "FORGE_E2E_PLATFORM_CHART_ARCHIVE"
	substrateChartArchiveEnv   = "FORGE_E2E_SUBSTRATE_CHART_ARCHIVE"
	storageChartArchiveEnv     = "FORGE_E2E_RWX_STORAGE_CHART_ARCHIVE"
	forceExternalStorageEnv    = "FORGE_E2E_FORCE_EXTERNAL_STORAGE"
	requireManagedTLSEnv       = "FORGE_E2E_REQUIRE_MANAGED_TLS"
	storageTLSOnlyEnv          = "FORGE_E2E_STORAGE_TLS_ONLY"
	sourceImageArchiveEnv      = "FORGE_E2E_SOURCE_IMAGE_ARCHIVE"
	requireManagedAgentPoolEnv = "FORGE_E2E_REQUIRE_MANAGED_AGENTPOOL"
)

// prepareCandidateChart transfers the exact Actions-retained platform and
// companion archives to the ephemeral host. Forge then gives remote Helm those
// extracted directories, so real-machine validation consumes the candidate
// bytes without publishing a persistent candidate package.
func prepareCandidateChart(t *testing.T, ip, keyPath string) {
	t.Helper()
	prepareExactSourceImages(t, ip, keyPath)
	platform := os.Getenv(platformChartArchiveEnv)
	substrate := os.Getenv(substrateChartArchiveEnv)
	storage := os.Getenv(storageChartArchiveEnv)
	if platform == "" && substrate == "" && storage == "" {
		return
	}
	if platform == "" || substrate == "" || storage == "" {
		t.Fatalf("exact platform validation requires %s, %s, and %s", platformChartArchiveEnv, substrateChartArchiveEnv, storageChartArchiveEnv)
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

	for _, archive := range []string{platform, substrate, storage} {
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
	t.Log("transferred exact platform, certificate, and managed-RWX substrate candidate archives to the real-machine host")
}

func prepareExactSourceImages(t *testing.T, ip, keyPath string) {
	t.Helper()
	archive := os.Getenv(sourceImageArchiveEnv)
	if archive == "" {
		return
	}
	controlPlaneImage := exactSourceImageReference(t, "CONTROL_PLANE_IMAGE_REPO", "CONTROL_PLANE_IMAGE_TAG")
	toolRunnerImage := exactSourceImageReference(t, "TOOL_RUNNER_IMAGE_REPO", "TOOL_RUNNER_IMAGE_TAG")
	harnessImage := exactSourceImageReference(t, "HARNESS_IMAGE_REPO", "HARNESS_IMAGE_TAG")

	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("dial source fixture host to transfer exact images: %v", err)
	}
	defer client.Close()
	remote := filepath.Join("/tmp", filepath.Base(archive))
	transferRWXFixture(t, client, archive, remote)
	mustSSHOutput(t, client, "sudo k3s ctr images import "+candidateShellQuote(remote))
	images := mustSSHOutput(t, client, "sudo k3s ctr images list -q")
	for _, expected := range []string{controlPlaneImage, toolRunnerImage, harnessImage} {
		if !strings.Contains("\n"+images+"\n", "\n"+expected+"\n") {
			t.Fatalf("exact source image %s was not imported into k3s containerd:\n%s", expected, images)
		}
	}
	mustSSHOutput(t, client, "rm -f "+candidateShellQuote(remote))
	t.Logf("imported exact source control-plane, tool-runner, and harness images for %s", os.Getenv("ITERABASE_E2E_SOURCE_SHA"))
}
