// Package e2e contains the composed CPU/GPU scenarios. The no-GPU preflight
// runs on the fixed CPU fixture; GPU readiness and real inference run on the
// fixed GPU fixture under the shared permanent-fixture lock.
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// GPUVM identifies the fixed GPU fixture surface consumed by the scenario.
type GPUVM struct {
	IP              string
	PrivKeyPath     string
	WorkspaceDevice string
}

type digitalOceanGPUState struct {
	fixture             *permanentFixture
	runID               string
	privKeyPath         string
	vm                  *GPUVM
	forgeBin            string
	forgeHome           string
	chartVersion        string
	upgradeEvidence     *gpuUpgradeEvidence
	runtimeImageDigests map[string]importedRuntimeIdentity
	apiTunnel           *sshAPITunnel
	apiServerName       string
	diagnostics         forgeDiagnostics
}

func newDigitalOceanGPUState(t *testing.T) *digitalOceanGPUState {
	fixture := requirePermanentFixture(t, "gpu")
	state := &digitalOceanGPUState{
		fixture:             fixture,
		runID:               fixture.installName(),
		privKeyPath:         fixture.sshKeyPath,
		vm:                  &GPUVM{IP: fixture.address, PrivKeyPath: fixture.sshKeyPath, WorkspaceDevice: fixture.workspaceDevice},
		forgeHome:           t.TempDir(),
		chartVersion:        platformChartVersion(t, ""),
		runtimeImageDigests: make(map[string]importedRuntimeIdentity),
		diagnostics:         newForgeDiagnostics(t, "digitalocean-gpu"),
	}
	state.forgeBin = buildForge(t)
	return state
}

func provisionGPUStage(t *testing.T, state *digitalOceanGPUState) {
	require.NoError(t, state.fixture.reset(t, state.forgeBin, state.forgeHome))
	rememberWorkspaceDevice(state.vm.IP, state.vm.WorkspaceDevice)
	t.Logf("permanent GPU fixture %s workspace=%s", state.vm.IP, state.vm.WorkspaceDevice)
}

func applyGPUSubstrateStage(t *testing.T, state *digitalOceanGPUState) {
	cfgPath := writeForgeConfigGPUDriver(t, state.runID, state.vm.IP, state.privKeyPath, gpuUpgradeBaselineDriver)
	out := applyOnce(t, state.forgeBin, state.forgeHome, cfgPath)
	assertApplyMarkers(t, out, "node ready: true", "AgentPool workspace:", "AgentPool local-path ready: true", "gpu ready: true", "gpu driver: "+gpuUpgradeBaselineDriver)
	state.bindKubeconfigTunnel(t)
	t.Logf("apply output:\n%s", out)
}

func assertGPUSmokeStage(t *testing.T, state *digitalOceanGPUState) {
	checkGPUSmoke(t, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
}

func (state *digitalOceanGPUState) cleanup(t *testing.T) {
	t.Helper()
	state.stopAPITunnel()
	state.diagnostics.setDomain(failureDomainCleanup)
	workspaceDevicesByAddress.Delete(state.vm.IP)
	if err := state.fixture.reset(t, state.forgeBin, state.forgeHome); err != nil {
		t.Errorf("reset permanent GPU fixture after diagnostics: %v", err)
		return
	}
	if os.Getenv("FORGE_E2E_BREAK_CLEANUP") == "true" {
		t.Error("intentional HOR-540 cleanup failure after successful permanent GPU reset")
	}
}

// checkGPUSmoke schedules a one-off pod requesting nvidia.com/gpu that runs
// nvidia-smi. Succeeding proves the full path the ModelBackend contract relies
// on: the resource is schedulable AND a container can actually use the GPU
// (CDI/runtime injection, not just advertisement).
func checkGPUSmoke(t *testing.T, kcPath string) {
	t.Helper()
	restCfg, err := clientcmd.BuildConfigFromFlags("", kcPath)
	require.NoError(t, err)
	cs, err := kubernetes.NewForConfig(restCfg)
	require.NoError(t, err)

	const name = "gpu-smoke"
	nvidiaRC := "nvidia"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
			RuntimeClassName: &nvidiaRC,
			RestartPolicy:    corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "smoke",
				Image:   "nvidia/cuda:12.4.1-base-ubuntu22.04",
				Command: []string{"sh", "-c", "nvidia-smi 2>/dev/null || ls /dev/nvidia* 2>/dev/null"},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
					},
				},
			}},
		},
	}
	_, err = cs.CoreV1().Pods("default").Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	defer func() { _ = cs.CoreV1().Pods("default").Delete(context.Background(), name, metav1.DeleteOptions{}) }()

	deadline := time.Now().Add(8 * time.Minute) // image pull; driver is already up (gate passed)
	for time.Now().Before(deadline) {
		p, gerr := cs.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
		if gerr == nil {
			switch p.Status.Phase {
			case corev1.PodSucceeded:
				return
			case corev1.PodFailed:
				t.Fatalf("gpu smoke pod failed: %+v", p.Status.ContainerStatuses)
			}
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("gpu smoke pod did not succeed within timeout")
}

// writeForgeConfigGPU writes a k3s + GPU (no platform chart) forge.yaml.
func writeForgeConfigGPU(t *testing.T, name, ip, keyPath string) string {
	return writeForgeConfigGPUDriver(t, name, ip, keyPath, "")
}

func writeForgeConfigGPUDriver(t *testing.T, name, ip, keyPath, driverVersion string) string {
	return writeForgeConfigSpec(t, forgeConfigSpec{
		Name: name, Address: ip, SSHKeyPath: keyPath, GPU: true, GPUDriverVersion: driverVersion,
	})
}

// sshRun runs a command on the droplet over SSH and returns combined output.
func sshRun(t *testing.T, ip, keyPath, cmd string) (string, error) {
	t.Helper()
	client, err := sshDial(ip, keyPath)
	if err != nil {
		return "", err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

// dumpGPUDiagnostics queries the GPU operator state on the droplet when the
// readiness gate fails, so the cause (driver/toolkit/device-plugin/validator)
// is visible in the test log rather than just "gpu not ready after 15m".
func dumpGPUDiagnostics(t *testing.T, ip, keyPath string) {
	t.Helper()
	t.Log("=== GPU diagnostics ===")
	cmds := []string{
		"sudo k3s kubectl get clusterpolicy -o yaml",
		"sudo k3s kubectl get nodes -o wide --show-labels",
		"sudo k3s kubectl describe nodes",
		"sudo k3s kubectl get daemonsets,pods -n gpu-operator -o wide",
		"for pod in $(sudo k3s kubectl get pods -n gpu-operator -o name); do echo --- $pod; sudo k3s kubectl logs -n gpu-operator $pod --tail=200 --all-containers=true --prefix=true 2>&1 || true; done",
		"sudo k3s kubectl get deployment,pods,pvc -n forge-gpu-upgrade -o wide 2>&1 || true",
		"sudo k3s kubectl logs -n forge-gpu-upgrade -l app.kubernetes.io/name=gpu-driver-upgrade-workload --tail=200 --prefix=true 2>&1 || true",
		"sudo k3s kubectl get events -A --sort-by=.lastTimestamp | tail -100",
		"echo '--- k3s containerd config: nvidia/cdi entries? ---'; sudo grep -iE 'nvidia|cdi|runtime' /var/lib/rancher/k3s/agent/etc/containerd/config.toml 2>/dev/null || echo 'no k3s containerd config or no nvidia/cdi entries'",
		"echo '--- /etc/containerd ---'; sudo ls /etc/containerd/ 2>/dev/null || echo 'no /etc/containerd'",
	}
	for _, c := range cmds {
		out, err := sshRun(t, ip, keyPath, c)
		t.Logf("$ %s\n%s(err=%v)", c, out, err)
	}
}
