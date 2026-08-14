package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	gpuUpgradeBaselineDriver  = "580.126.20"
	gpuUpgradeCandidateDriver = "595.71.05"
	gpuUpgradeNamespace       = "forge-gpu-upgrade"
	gpuUpgradeWorkloadName    = "gpu-driver-upgrade-workload"
	gpuUpgradeReadyPrefix     = "gpu-upgrade-ready "
)

type gpuUpgradeEvidence struct {
	PodUID          types.UID
	PersistentOwner string
	EmptyDirOwner   string
	DriverVersion   string
}

func recordGPUUpgradeInputsStage(t *testing.T, state *digitalOceanGPUState) {
	t.Helper()
	identity, err := runForgeE(state.forgeBin, state.forgeHome, "version")
	if err != nil {
		t.Fatalf("record Forge candidate identity: %v\n%s", err, identity)
	}
	t.Logf(
		"gpu driver transition fixture: forge=%q binary=%q fixture_mode=%q source_sha=%q baseline_driver=%q candidate_driver=%q",
		strings.TrimSpace(identity), state.forgeBin, os.Getenv("ITERABASE_E2E_FIXTURE_MODE"),
		os.Getenv("ITERABASE_E2E_SOURCE_SHA"), gpuUpgradeBaselineDriver, gpuUpgradeCandidateDriver,
	)
}

func startGPUUpgradeWorkloadStage(t *testing.T, state *digitalOceanGPUState) {
	t.Helper()
	clients := newGPUUpgradeClients(t, state)
	ctx := context.Background()
	if _, err := clients.typed.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: gpuUpgradeNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create GPU upgrade namespace: %v", err)
	}
	if _, err := clients.typed.CoreV1().PersistentVolumeClaims(gpuUpgradeNamespace).Create(ctx, gpuUpgradePVC(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create GPU upgrade persistent cache: %v", err)
	}
	if _, err := clients.typed.AppsV1().Deployments(gpuUpgradeNamespace).Create(ctx, gpuUpgradeDeployment(state.runID), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create GPU upgrade workload: %v", err)
	}
	evidence := waitForGPUUpgradeWorkload(t, clients.typed, "", gpuUpgradeBaselineDriver, 10*time.Minute)
	if evidence.PersistentOwner != string(evidence.PodUID) || evidence.EmptyDirOwner != string(evidence.PodUID) {
		t.Fatalf("initial workload did not initialize both stores from pod %s: %+v", evidence.PodUID, evidence)
	}
	state.upgradeEvidence = &evidence
	t.Logf("baseline GPU workload ready: %+v", evidence)
}

func applyGPUDriverUpgradeStage(t *testing.T, state *digitalOceanGPUState) {
	t.Helper()
	cfgPath := writeForgeConfigGPUDriver(t, state.runID, state.vm.IP, state.privKeyPath, gpuUpgradeCandidateDriver)
	t.Logf("reconciling exact GPU driver transition %s -> %s", gpuUpgradeBaselineDriver, gpuUpgradeCandidateDriver)
	out := applyOnce(t, state.forgeBin, state.forgeHome, cfgPath)
	assertApplyMarkers(t, out, "node ready: true", "gpu ready: true", "gpu driver: "+gpuUpgradeCandidateDriver)
	t.Logf("driver upgrade apply output:\n%s", out)
}

func assertGPUDriverUpgradeStage(t *testing.T, state *digitalOceanGPUState) {
	t.Helper()
	if state.upgradeEvidence == nil {
		t.Fatal("baseline GPU workload evidence is missing")
	}
	clients := newGPUUpgradeClients(t, state)
	waitForGPUUpgradeNode(t, clients.typed, 10*time.Minute)
	assertGPUUpgradeClusterPolicy(t, clients.dynamic)

	before := *state.upgradeEvidence
	after := waitForGPUUpgradeWorkload(t, clients.typed, before.PodUID, gpuUpgradeCandidateDriver, 10*time.Minute)
	if after.PodUID == before.PodUID {
		t.Fatalf("GPU workload pod was not recreated: uid=%s", after.PodUID)
	}
	if after.PersistentOwner != before.PersistentOwner {
		t.Fatalf("persistent cache owner changed across driver transition: before=%q after=%q", before.PersistentOwner, after.PersistentOwner)
	}
	if after.EmptyDirOwner != string(after.PodUID) || after.EmptyDirOwner == before.EmptyDirOwner {
		t.Fatalf("disposable emptyDir was not recreated for pod %s: before=%q after=%q", after.PodUID, before.EmptyDirOwner, after.EmptyDirOwner)
	}
	t.Logf("GPU driver transition converged with recreated workload: before=%+v after=%+v", before, after)
}

type gpuUpgradeClients struct {
	typed   kubernetes.Interface
	dynamic dynamic.Interface
}

func newGPUUpgradeClients(t *testing.T, state *digitalOceanGPUState) gpuUpgradeClients {
	t.Helper()
	kubeconfig := filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml")
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("build GPU upgrade kubeconfig: %v", err)
	}
	restConfig.Timeout = 30 * time.Second
	typed, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("create GPU upgrade Kubernetes client: %v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("create GPU upgrade dynamic client: %v", err)
	}
	return gpuUpgradeClients{typed: typed, dynamic: dynamicClient}
}

func gpuUpgradePVC() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: gpuUpgradeWorkloadName + "-cache", Namespace: gpuUpgradeNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			}},
		},
	}
}

func gpuUpgradeDeployment(runID string) *appsv1.Deployment {
	labels := map[string]string{
		"app.kubernetes.io/name":       gpuUpgradeWorkloadName,
		"e2e.horizonshift.io/run":      runID,
		"e2e.horizonshift.io/contract": "gpu-driver-upgrade",
	}
	replicas := int32(1)
	gracePeriod := int64(0)
	sharedMemoryLimit := resource.MustParse("1Gi")
	nvidiaRuntime := "nvidia"
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: gpuUpgradeWorkloadName, Namespace: gpuUpgradeNamespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RuntimeClassName:              &nvidiaRuntime,
					TerminationGracePeriodSeconds: &gracePeriod,
					Containers: []corev1.Container{{
						Name:  "inference",
						Image: "nvidia/cuda:12.4.1-base-ubuntu22.04",
						Command: []string{"sh", "-ceu", `
if [ ! -s /cache/owner ]; then
  printf '%s' "$POD_UID" > /cache/owner
fi
if [ -e /dev/shm/owner ]; then
  echo "emptyDir unexpectedly retained a prior pod owner" >&2
  exit 1
fi
printf '%s' "$POD_UID" > /dev/shm/owner
driver="$(nvidia-smi --query-gpu=driver_version --format=csv,noheader | head -n1 | tr -d '[:space:]')"
printf 'gpu-upgrade-ready pod_uid=%s persistent_owner=%s emptydir_owner=%s driver=%s\n' \
  "$POD_UID" "$(cat /cache/owner)" "$(cat /dev/shm/owner)" "$driver"
touch /tmp/ready
exec tail -f /dev/null
`},
						Env: []corev1.EnvVar{{
							Name: "POD_UID",
							ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
								APIVersion: "v1", FieldPath: "metadata.uid",
							}},
						}},
						Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
							Command: []string{"test", "-f", "/tmp/ready"},
						}}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "shared-memory", MountPath: "/dev/shm"},
							{Name: "persistent-cache", MountPath: "/cache"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "shared-memory", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumMemory, SizeLimit: &sharedMemoryLimit,
						}}},
						{Name: "persistent-cache", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: gpuUpgradeWorkloadName + "-cache",
						}}},
					},
				},
			},
		},
	}
}

func waitForGPUUpgradeWorkload(t *testing.T, client kubernetes.Interface, previousUID types.UID, wantDriver string, timeout time.Duration) gpuUpgradeEvidence {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	selector := "app.kubernetes.io/name=" + gpuUpgradeWorkloadName
	for time.Now().Before(deadline) {
		pods, err := client.CoreV1().Pods(gpuUpgradeNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			t.Fatalf("observe GPU upgrade workload: %v", err)
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.UID == previousUID || !podReady(pod) {
				continue
			}
			logs, err := client.CoreV1().Pods(gpuUpgradeNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "inference"}).DoRaw(ctx)
			if err != nil {
				t.Fatalf("read ready GPU upgrade workload logs: %v", err)
			}
			evidence, err := parseGPUUpgradeEvidence(string(logs))
			if err != nil {
				t.Fatalf("parse ready GPU upgrade workload logs: %v\n%s", err, logs)
			}
			if evidence.PodUID != pod.UID {
				t.Fatalf("workload reported pod uid %s, Kubernetes reports %s", evidence.PodUID, pod.UID)
			}
			if evidence.DriverVersion != wantDriver {
				t.Fatalf("workload driver = %q, want %q", evidence.DriverVersion, wantDriver)
			}
			return evidence
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("GPU upgrade workload did not become ready with driver %s within %s", wantDriver, timeout)
	return gpuUpgradeEvidence{}
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func parseGPUUpgradeEvidence(logs string) (gpuUpgradeEvidence, error) {
	for _, line := range strings.Split(logs, "\n") {
		if !strings.HasPrefix(line, gpuUpgradeReadyPrefix) {
			continue
		}
		values := make(map[string]string)
		for _, field := range strings.Fields(strings.TrimPrefix(line, gpuUpgradeReadyPrefix)) {
			key, value, ok := strings.Cut(field, "=")
			if ok {
				values[key] = value
			}
		}
		for _, key := range []string{"pod_uid", "persistent_owner", "emptydir_owner", "driver"} {
			if values[key] == "" {
				return gpuUpgradeEvidence{}, fmt.Errorf("GPU upgrade evidence lacks %s", key)
			}
		}
		return gpuUpgradeEvidence{
			PodUID:          types.UID(values["pod_uid"]),
			PersistentOwner: values["persistent_owner"],
			EmptyDirOwner:   values["emptydir_owner"],
			DriverVersion:   values["driver"],
		}, nil
	}
	return gpuUpgradeEvidence{}, fmt.Errorf("GPU upgrade readiness record not found")
}

func waitForGPUUpgradeNode(t *testing.T, client kubernetes.Interface, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("observe node after GPU driver upgrade: %v", err)
		}
		if len(nodes.Items) != 1 {
			t.Fatalf("GPU upgrade fixture has %d nodes, want 1", len(nodes.Items))
		}
		node := &nodes.Items[0]
		state := node.Labels["nvidia.com/gpu-driver-upgrade-state"]
		if state == "upgrade-failed" {
			t.Fatalf("GPU driver upgrade entered upgrade-failed on node %s", node.Name)
		}
		if state == "upgrade-done" && !node.Spec.Unschedulable && nodeReady(node) {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("GPU node did not reach Ready, schedulable, upgrade-done within %s", timeout)
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func assertGPUUpgradeClusterPolicy(t *testing.T, client dynamic.Interface) {
	t.Helper()
	policies, err := client.Resource(schema.GroupVersionResource{
		Group: "nvidia.com", Version: "v1", Resource: "clusterpolicies",
	}).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("read ClusterPolicy after GPU driver upgrade: %v", err)
	}
	if len(policies.Items) != 1 {
		t.Fatalf("GPU upgrade fixture has %d ClusterPolicies, want 1", len(policies.Items))
	}
	policy := &policies.Items[0]
	assertNestedString(t, policy, gpuUpgradeCandidateDriver, "spec", "driver", "version")
	assertNestedString(t, policy, "ready", "status", "state")
	assertNestedBool(t, policy, true, "spec", "driver", "upgradePolicy", "podDeletion", "deleteEmptyDir")
	assertNestedBool(t, policy, false, "spec", "driver", "upgradePolicy", "drain", "enable")
}

func assertNestedString(t *testing.T, object *unstructured.Unstructured, want string, fields ...string) {
	t.Helper()
	got, found, err := unstructured.NestedString(object.Object, fields...)
	if err != nil || !found || got != want {
		t.Fatalf("ClusterPolicy %s = %q (found=%v err=%v), want %q", strings.Join(fields, "."), got, found, err, want)
	}
}

func assertNestedBool(t *testing.T, object *unstructured.Unstructured, want bool, fields ...string) {
	t.Helper()
	got, found, err := unstructured.NestedBool(object.Object, fields...)
	if err != nil || !found || got != want {
		t.Fatalf("ClusterPolicy %s = %t (found=%v err=%v), want %t", strings.Join(fields, "."), got, found, err, want)
	}
}

func TestBrokenEmptyDirPolicyForgeBuild(t *testing.T) {
	t.Setenv("FORGE_E2E_BINARY", "")
	t.Setenv("FORGE_E2E_BREAK_DELETE_EMPTYDIR", "true")
	bin := buildForge(t)
	identity, err := runForgeE(bin, t.TempDir(), "version")
	if err != nil || !strings.HasPrefix(identity, "forge ") {
		t.Fatalf("intentional broken-policy Forge build is not runnable: err=%v output=%q", err, identity)
	}
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(binary, []byte("driver.upgradePolicy.gpuPodDeletion.deleteEmptyDir=false")) ||
		bytes.Contains(binary, []byte("driver.upgradePolicy.gpuPodDeletion.deleteEmptyDir=true")) {
		t.Fatal("intentional broken-policy Forge binary did not contain only the false policy value")
	}
}

func TestGPUUpgradeDeploymentSeparatesDisposableAndPersistentState(t *testing.T) {
	deployment := gpuUpgradeDeployment("test-run")
	pod := deployment.Spec.Template.Spec
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "nvidia" {
		t.Fatalf("runtimeClassName = %v, want nvidia", pod.RuntimeClassName)
	}
	if len(pod.Volumes) != 2 || pod.Volumes[0].EmptyDir == nil || pod.Volumes[0].EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Fatalf("shared memory volume is not a memory-backed emptyDir: %+v", pod.Volumes)
	}
	if pod.Volumes[1].PersistentVolumeClaim == nil {
		t.Fatalf("persistent cache is not PVC-backed: %+v", pod.Volumes)
	}
	container := pod.Containers[0]
	gpuLimit := container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]
	if gpuLimit.Cmp(resource.MustParse("1")) != 0 {
		t.Fatalf("workload does not request exactly one GPU: %+v", container.Resources.Limits)
	}
	command := strings.Join(container.Command, " ")
	for _, contract := range []string{"/dev/shm/owner", "/cache/owner", "nvidia-smi"} {
		if !strings.Contains(command, contract) {
			t.Fatalf("workload command does not assert %s: %s", contract, command)
		}
	}
}

func TestParseGPUUpgradeEvidence(t *testing.T) {
	logs := "startup\n" + gpuUpgradeReadyPrefix + "pod_uid=new persistent_owner=old emptydir_owner=new driver=" + gpuUpgradeCandidateDriver + "\n"
	got, err := parseGPUUpgradeEvidence(logs)
	if err != nil {
		t.Fatal(err)
	}
	if got.PodUID != "new" || got.PersistentOwner != "old" || got.EmptyDirOwner != "new" || got.DriverVersion != gpuUpgradeCandidateDriver {
		t.Fatalf("unexpected evidence: %+v", got)
	}
}
