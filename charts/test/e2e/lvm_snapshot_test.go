package e2e_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/kube"
)

const (
	managedLVMSnapshotClass = "iterabase-lvm-snapshot"
	snapshotMarker          = "HOR-545-local-snapshot-v1"
)

// assertOpenEBSLVMSnapshotLifecycleStage proves DES-HOR-545-04 against real
// loop-backed LVM in the Kind node. It is an explicit operator-created local
// primitive: no platform chart creates a snapshot automatically.
func assertOpenEBSLVMSnapshotLifecycleStage(t *testing.T, state *chartState) {
	t.Helper()
	baseline := state.kindLVMNames(t)
	assertNoAutomaticSnapshots(t, state)
	createSnapshotSource(t, state, "lvm-snapshot-source", "2Gi", snapshotMarker)
	sourcePV := state.kubectl(t, 30*time.Second, "get", "pvc/lvm-snapshot-source", "-n", testNamespace, "-o", `jsonpath={.spec.volumeName}`)
	sourceHandle := state.kubectl(t, 30*time.Second, "get", "pv/"+sourcePV, "-o", `jsonpath={.spec.csi.volumeHandle}`)

	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "lvm-snapshot.yaml", `apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: {name: lvm-snapshot, namespace: iterabase-system}
spec:
  volumeSnapshotClassName: iterabase-lvm-snapshot
  source: {persistentVolumeClaimName: lvm-snapshot-source}
`))
	waitForSnapshotReady(t, state, "lvm-snapshot", 4*time.Minute)
	content := state.kubectl(t, 30*time.Second, "get", "volumesnapshot/lvm-snapshot", "-n", testNamespace, "-o", `jsonpath={.status.boundVolumeSnapshotContentName}`)
	snapshotHandle := state.kubectl(t, 30*time.Second, "get", "volumesnapshotcontent/"+content, "-o", `jsonpath={.status.snapshotHandle}`)
	if content == "" || snapshotHandle == "" || snapshotHandle == sourceHandle {
		t.Fatalf("snapshot identity is missing or aliases its source: source=%q content=%q snapshot=%q", sourceHandle, content, snapshotHandle)
	}
	lvmSnapshot := state.kubectl(t, 30*time.Second, "get", "lvmsnapshot.local.openebs.io/"+snapshotHandle, "-n", testNamespace, "-o", `jsonpath={.status.state}|{.spec.volGroup}|{.spec.snapSize}|{.spec.thinProvision}|{.spec.ownerNodeID}`)
	parts := strings.Split(lvmSnapshot, "|")
	if len(parts) != 5 || parts[0] != "Ready" || parts[1] != lvmDataVolumeGroupName || parts[2] == "" || parts[3] != "false" || parts[4] == "" {
		t.Fatalf("backing LVMSnapshot does not match the Ready thick local contract: %q", lvmSnapshot)
	}
	if !containsString(state.kindLVMNames(t), snapshotHandle) {
		t.Fatalf("Ready LVMSnapshot %s has no real LVM snapshot LV", snapshotHandle)
	}

	// Mutate only the source after the snapshot. The restored claim must expose
	// the durable pre-snapshot bytes, while the source retains the later bytes.
	createSnapshotWriterPod(t, state, "lvm-snapshot-source-post", "lvm-snapshot-source", "printf post-snapshot >> /data/post; sync")
	state.kubectl(t, 2*time.Minute, "delete", "pod/lvm-snapshot-source-post", "-n", testNamespace, "--wait=true", "--timeout=90s")
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "lvm-snapshot-restore.yaml", `apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: lvm-snapshot-restore, namespace: iterabase-system}
spec:
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  storageClassName: iterabase-lvm-xfs
  dataSource:
    name: lvm-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  resources: {requests: {storage: 2Gi}}
`))
	createSnapshotWriterPod(t, state, "lvm-snapshot-restore-reader", "lvm-snapshot-restore", `test "$(cat /data/marker)" = HOR-545-local-snapshot-v1; test ! -e /data/post`)
	restorePV := state.kubectl(t, 30*time.Second, "get", "pvc/lvm-snapshot-restore", "-n", testNamespace, "-o", `jsonpath={.spec.volumeName}`)
	restoreHandle := state.kubectl(t, 30*time.Second, "get", "pv/"+restorePV, "-o", `jsonpath={.spec.csi.volumeHandle}`)
	if restorePV == "" || restoreHandle == "" || restoreHandle == sourceHandle || restoreHandle == snapshotHandle {
		t.Fatalf("restored volume identity is missing or aliased: source=%q snapshot=%q restore=%q", sourceHandle, snapshotHandle, restoreHandle)
	}

	// Exact companion reapply must preserve the existing CSI/OpenEBS snapshot.
	before := snapshotIdentity(t, state, "lvm-snapshot")
	manager := "system:serviceaccount:" + testNamespace + ":" + testRelease + "-control-plane-manager"
	if out, err := state.client.HelmUpgrade(state.ctx, kube.HelmOptions{
		Release: testRelease + "-lvm-storage", Namespace: testNamespace, Chart: state.lvmSubstrate,
		Values: map[string]string{"agentpool.authorizedManagerIdentity": manager, "lvm-localpv.global.kubeletDir": "/var/lib/kubelet"},
		Wait:   true, Timeout: 8 * time.Minute,
	}); err != nil {
		t.Fatalf("reapply LVM snapshot substrate: %v\n%s", err, out)
	}
	waitForSnapshotReady(t, state, "lvm-snapshot", 3*time.Minute)
	if after := snapshotIdentity(t, state, "lvm-snapshot"); after != before {
		t.Fatalf("companion reapply replaced snapshot identity: before=%q after=%q", before, after)
	}

	// Delete restore and snapshot under their declared Delete policies. The
	// source remains readable and only the corresponding OpenEBS/LVM objects go.
	state.kubectl(t, 2*time.Minute, "delete", "pod/lvm-snapshot-restore-reader", "pvc/lvm-snapshot-restore", "-n", testNamespace, "--wait=true", "--timeout=90s")
	state.kubectl(t, 2*time.Minute, "delete", "volumesnapshot/lvm-snapshot", "-n", testNamespace, "--wait=true", "--timeout=90s")
	waitForSnapshotObjectsGone(t, state, content, snapshotHandle, restorePV, restoreHandle, 3*time.Minute)
	createSnapshotWriterPod(t, state, "lvm-snapshot-source-reader", "lvm-snapshot-source", `test "$(cat /data/marker)" = HOR-545-local-snapshot-v1; test "$(cat /data/post)" = post-snapshot`)

	assertFullOriginSnapshotExhaustion(t, state)
	state.kubectl(t, 2*time.Minute, "delete", "pod/lvm-snapshot-source", "pod/lvm-snapshot-source-post", "pod/lvm-snapshot-source-reader", "pvc/lvm-snapshot-source", "-n", testNamespace, "--ignore-not-found=true", "--wait=true", "--timeout=90s")
	waitForExactLVMNames(t, state, baseline, 3*time.Minute)
}

func assertNoAutomaticSnapshots(t *testing.T, state *chartState) {
	t.Helper()
	classes := strings.Fields(state.kubectl(t, 30*time.Second, "get", "volumesnapshotclass.snapshot.storage.k8s.io", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`))
	if len(classes) != 1 || classes[0] != managedLVMSnapshotClass {
		t.Fatalf("managed VolumeSnapshotClass set=%v, want exactly %s", classes, managedLVMSnapshotClass)
	}
	contract := state.kubectl(t, 30*time.Second, "get", "volumesnapshotclass.snapshot.storage.k8s.io/"+managedLVMSnapshotClass, "-o", `jsonpath={.driver}|{.deletionPolicy}|{.parameters.snapSize}|{.metadata.annotations.snapshot\.storage\.kubernetes\.io/is-default-class}`)
	if contract != "local.csi.openebs.io|Delete|100%|false" {
		t.Fatalf("managed VolumeSnapshotClass contract=%q", contract)
	}
	if got := state.kubectl(t, 30*time.Second, "get", "volumesnapshot.snapshot.storage.k8s.io", "-A", "-o", "name"); strings.TrimSpace(got) != "" {
		t.Fatalf("platform chart created automatic VolumeSnapshots: %s", got)
	}
}

func createSnapshotSource(t *testing.T, state *chartState, name, size, marker string) {
	t.Helper()
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, name+".yaml", fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: %[1]s, namespace: iterabase-system}
spec:
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  storageClassName: iterabase-lvm-xfs
  resources: {requests: {storage: %[2]s}}
`, name, size)))
	createSnapshotWriterPod(t, state, name, name, "printf '%s' > /data/marker; sync", marker)
	state.kubectl(t, 2*time.Minute, "delete", "pod/"+name, "-n", testNamespace, "--wait=true", "--timeout=90s")
}

func createSnapshotWriterPod(t *testing.T, state *chartState, pod, claim, command string, args ...any) {
	t.Helper()
	command = fmt.Sprintf(command, args...)
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata: {name: %[1]s, namespace: iterabase-system}
spec:
  restartPolicy: Never
  containers:
    - name: proof
      image: busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0
      command: [sh, -ceu]
      args: [%[2]q]
      volumeMounts: [{name: data, mountPath: /data}]
  volumes: [{name: data, persistentVolumeClaim: {claimName: %[3]s}}]
`, pod, command, claim)
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, pod+".yaml", manifest))
	state.kubectl(t, 5*time.Minute, "wait", "pod/"+pod, "-n", testNamespace, "--for=jsonpath={.status.phase}=Succeeded", "--timeout=4m")
}

func waitForSnapshotReady(t *testing.T, state *chartState, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = state.kubectl(t, 30*time.Second, "get", "volumesnapshot/"+name, "-n", testNamespace, "-o", `jsonpath={.status.readyToUse}|{.status.error.message}|{.status.boundVolumeSnapshotContentName}`)
		if strings.HasPrefix(last, "true||") && !strings.HasSuffix(last, "|") {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("VolumeSnapshot %s did not become Ready: %s", name, last)
}

func snapshotIdentity(t *testing.T, state *chartState, name string) string {
	t.Helper()
	content := state.kubectl(t, 30*time.Second, "get", "volumesnapshot/"+name, "-n", testNamespace, "-o", `jsonpath={.metadata.uid}|{.status.boundVolumeSnapshotContentName}`)
	parts := strings.Split(content, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("snapshot %s has incomplete identity: %q", name, content)
	}
	return content + "|" + state.kubectl(t, 30*time.Second, "get", "volumesnapshotcontent/"+parts[1], "-o", `jsonpath={.metadata.uid}|{.status.snapshotHandle}|{.status.readyToUse}`)
}

func waitForSnapshotObjectsGone(t *testing.T, state *chartState, content, snapshotHandle, restorePV, restoreHandle string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		objects := []string{
			state.kubectl(t, 30*time.Second, "get", "volumesnapshotcontent/"+content, "--ignore-not-found=true", "-o", "name"),
			state.kubectl(t, 30*time.Second, "get", "lvmsnapshot.local.openebs.io/"+snapshotHandle, "-n", testNamespace, "--ignore-not-found=true", "-o", "name"),
			state.kubectl(t, 30*time.Second, "get", "pv/"+restorePV, "--ignore-not-found=true", "-o", "name"),
			state.kubectl(t, 30*time.Second, "get", "lvmvolume.local.openebs.io/"+restoreHandle, "-n", testNamespace, "--ignore-not-found=true", "-o", "name"),
		}
		if strings.TrimSpace(strings.Join(objects, "")) == "" && !containsString(state.kindLVMNames(t), snapshotHandle) && !containsString(state.kindLVMNames(t), restoreHandle) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("snapshot/restore Delete lifecycle leaked content=%s snapshot=%s pv=%s volume=%s", content, snapshotHandle, restorePV, restoreHandle)
}

func assertFullOriginSnapshotExhaustion(t *testing.T, state *chartState) {
	t.Helper()
	before := state.kindLVMNames(t)
	createSnapshotSource(t, state, "lvm-snapshot-exhaustion-source", "55Gi", "HOR-545-full-origin")
	sourcePV := state.kubectl(t, 30*time.Second, "get", "pvc/lvm-snapshot-exhaustion-source", "-n", testNamespace, "-o", `jsonpath={.spec.volumeName}`)
	sourceHandle := state.kubectl(t, 30*time.Second, "get", "pv/"+sourcePV, "-o", `jsonpath={.spec.csi.volumeHandle}`)
	state.kubectl(t, 30*time.Second, "apply", "-f", state.writeManifest(t, "lvm-snapshot-exhaustion.yaml", `apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: {name: lvm-snapshot-exhaustion, namespace: iterabase-system}
spec:
  volumeSnapshotClassName: iterabase-lvm-snapshot
  source: {persistentVolumeClaimName: lvm-snapshot-exhaustion-source}
`))
	deadline := time.Now().Add(3 * time.Minute)
	var observation string
	for time.Now().Before(deadline) {
		observation = state.kubectl(t, 30*time.Second, "get", "volumesnapshot/lvm-snapshot-exhaustion", "-n", testNamespace, "-o", `jsonpath={.status.readyToUse}|{.status.error.message}`)
		if strings.HasPrefix(observation, "true|") {
			t.Fatalf("full-origin snapshot unexpectedly became Ready: %s", observation)
		}
		lower := strings.ToLower(observation)
		if strings.Contains(lower, "not enough") || strings.Contains(lower, "insufficient") || strings.Contains(lower, "no space") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	lower := strings.ToLower(observation)
	if !strings.Contains(lower, "not enough") && !strings.Contains(lower, "insufficient") && !strings.Contains(lower, "no space") {
		t.Fatalf("full-origin snapshot did not fail with actionable capacity evidence: %q", observation)
	}
	state.kubectl(t, 2*time.Minute, "delete", "volumesnapshot/lvm-snapshot-exhaustion", "pod/lvm-snapshot-exhaustion-source", "pvc/lvm-snapshot-exhaustion-source", "-n", testNamespace, "--ignore-not-found=true", "--wait=true", "--timeout=90s")
	waitForLVMHandlesGone(t, state, []string{sourceHandle}, before, 3*time.Minute)
}

func waitForLVMHandlesGone(t *testing.T, state *chartState, handles, baseline []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current := state.kindLVMNames(t)
		gone := true
		for _, handle := range handles {
			gone = gone && !containsString(current, handle) &&
				strings.TrimSpace(state.kubectl(t, 30*time.Second, "get", "lvmvolume.local.openebs.io/"+handle, "-n", testNamespace, "--ignore-not-found=true", "-o", "name")) == ""
		}
		snapshots := strings.TrimSpace(state.kubectl(t, 30*time.Second, "get", "lvmsnapshot.local.openebs.io", "-A", "-o", "name"))
		if gone && snapshots == "" && equalStrings(current, baseline) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("LVM handles %v were not reclaimed; current=%v baseline=%v", handles, state.kindLVMNames(t), baseline)
}

func waitForExactLVMNames(t *testing.T, state *chartState, want []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := state.kindLVMNames(t); equalStrings(got, want) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("LVM state did not return to baseline: got=%v want=%v", state.kindLVMNames(t), want)
}

func (state *chartState) kindLVMNames(t *testing.T) []string {
	t.Helper()
	node := strings.TrimSpace(state.process(t, 30*time.Second, "kind", "get", "nodes", "--name", state.cluster.Name))
	out := state.process(t, 30*time.Second, "docker", "exec", node, "lvs", "--noheadings", "--select", "vg_name="+lvmDataVolumeGroupName, "-o", "lv_name")
	values := strings.Fields(out)
	sort.Strings(values)
	return values
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
