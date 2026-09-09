package kind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
)

// Exact DES-HOR-545-01 storage identities shared by owner scenarios.
const (
	AgentPoolWorkspaceStorageClass = "iterabase-agentpool-lvm-xfs"
	PlatformDataStorageClass       = "iterabase-lvm-xfs"
	AgentPoolWorkspaceProvisioner  = "local.csi.openebs.io"
	DataVolumeGroupName            = "iterabase-data"
	lvmTopologyKey                 = "openebs.io/nodename"
	kindKubeletDirectory           = "/var/lib/kubelet"
	defaultClassAnnotation         = "storageclass.kubernetes.io/is-default-class"
	betaDefaultClassAnnotation     = "storageclass.beta.kubernetes.io/is-default-class"
	// The largest owner scenario enables 70 GiB of thick chart claims; keep
	// bounded headroom for representative general and AgentPool claims.
	kindDataVolumeGroupSizeBytes = 96 << 30
)

// ConfigureLVMStorage removes Kind's local-path fallback, creates a real thick
// loop-backed LVM VG inside the one privileged node, installs the exact pinned
// lvm-storage-substrate chart, and waits for CRD/CSI/VG/class discovery.
func (cluster *Cluster) ConfigureLVMStorage(ctx context.Context, chart, namespace, release string) error {
	if cluster == nil || cluster.executor == nil || cluster.Kubeconfig == "" {
		return fmt.Errorf("kind LVM storage requires an initialized cluster")
	}
	if chart == "" || namespace == "" || release == "" {
		return fmt.Errorf("kind LVM storage requires chart, namespace, and release identities")
	}
	if info, err := os.Stat(chart); err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("kind LVM storage chart %q is unavailable", chart)
	}
	nodes, err := cluster.executor.Run(ctx, process.Command{
		Name: "kind", Args: []string{"get", "nodes", "--name", cluster.Name}, Timeout: 30 * time.Second,
		OutputName: "kind-lvm-nodes-" + cluster.Name + ".log",
	})
	if err != nil {
		return fmt.Errorf("list Kind nodes for LVM storage: %w", err)
	}
	nodeNames := strings.Fields(nodes.Output)
	if len(nodeNames) != 1 {
		return fmt.Errorf("kind LVM storage requires exactly one node, observed %d", len(nodeNames))
	}

	if _, err := cluster.runKubectl(ctx, 2*time.Minute, "kind-remove-local-path-"+cluster.Name+".log",
		"delete", "namespace", "local-path-storage", "--ignore-not-found=true", "--wait=true", "--timeout=90s"); err != nil {
		return fmt.Errorf("remove Kind local-path provisioner: %w", err)
	}
	if _, err := cluster.runKubectl(ctx, 30*time.Second, "kind-remove-default-class-"+cluster.Name+".log",
		"delete", "storageclass", "standard", "--ignore-not-found=true"); err != nil {
		return fmt.Errorf("remove Kind default StorageClass: %w", err)
	}

	prepare := `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v pvcreate >/dev/null || ! command -v mkfs.xfs >/dev/null || ! command -v losetup >/dev/null || ! command -v modprobe >/dev/null; then
  apt-get update
  apt-get install -y lvm2 xfsprogs util-linux kmod
fi
modprobe dm-snapshot
if vgs iterabase-data >/dev/null 2>&1; then
  test "$(vgs --noheadings -o pv_count iterabase-data | awk '{$1=$1;print}')" = 1
else
  test ! -e /var/lib/iterabase-lvm-loop || exit 42
  truncate -s ` + fmt.Sprintf("%d", kindDataVolumeGroupSizeBytes) + ` /var/lib/iterabase-data.img
  loop=$(losetup --find --show /var/lib/iterabase-data.img)
  printf '%s\n' "$loop" > /var/lib/iterabase-lvm-loop
  pvcreate --yes "$loop"
  vgcreate --yes iterabase-data "$loop"
fi
vgs --noheadings -o vg_name,vg_uuid,vg_size,vg_free,pv_count,lv_count iterabase-data
`
	if result, err := cluster.executor.Run(ctx, process.Command{
		Name: "docker", Args: []string{"exec", nodeNames[0], "bash", "-ceu", prepare}, Timeout: 5 * time.Minute,
		OutputName: "kind-prepare-lvm-" + cluster.Name + ".log",
	}); err != nil {
		return fmt.Errorf("prepare real Kind iterabase-data VG: %w\n%s", err, result.Output)
	}

	cluster.mu.Lock()
	cluster.lvmPrepared = true
	cluster.lvmNode = nodeNames[0]
	cluster.lvmNamespace = namespace
	cluster.lvmRelease = release
	cluster.mu.Unlock()

	if result, err := cluster.executor.Run(ctx, process.Command{
		Name: "helm", Args: lvmStorageHelmArgs(release, chart, cluster.Kubeconfig, namespace),
		Timeout: 10 * time.Minute, OutputName: "kind-install-lvm-storage-" + cluster.Name + ".log",
	}); err != nil {
		return fmt.Errorf("install pinned LVM storage substrate: %w\n%s", err, result.Output)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for {
		if err := cluster.validateLVMStorage(ctx, namespace, nodeNames[0]); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (cluster *Cluster) cleanupLVMStorage(ctx context.Context) error {
	script := `set -euo pipefail
kubeconfig=$1
namespace=$2
lvm_release=$3
node=$4
if kubectl --kubeconfig "$kubeconfig" get crd agentpools.platform.iterabase.com >/dev/null 2>&1; then
  kubectl --kubeconfig "$kubeconfig" delete agentpools.platform.iterabase.com --all -A --ignore-not-found=true --wait=true --timeout=3m
fi
namespaced_resources=$(kubectl --kubeconfig "$kubeconfig" api-resources --api-group=platform.iterabase.com --namespaced=true --verbs=list,delete -o name)
while IFS= read -r resource; do
  test -n "$resource" || continue
  test "$resource" = agentpools.platform.iterabase.com && continue
  kubectl --kubeconfig "$kubeconfig" delete "$resource" --all -A --ignore-not-found=true --wait=true --timeout=3m
done <<<"$namespaced_resources"
cluster_resources=$(kubectl --kubeconfig "$kubeconfig" api-resources --api-group=platform.iterabase.com --namespaced=false --verbs=list,delete -o name)
while IFS= read -r resource; do
  test -n "$resource" || continue
  kubectl --kubeconfig "$kubeconfig" delete "$resource" --all --ignore-not-found=true --wait=true --timeout=3m
done <<<"$cluster_resources"
while IFS= read -r release; do
  test -n "$release" || continue
  test "$release" = "$lvm_release" && continue
  helm uninstall "$release" --kubeconfig "$kubeconfig" -n "$namespace" --wait --timeout 3m
done < <(helm list --kubeconfig "$kubeconfig" -n "$namespace" -q)
kubectl --kubeconfig "$kubeconfig" delete pod -n "$namespace" -l 'app notin (openebs-lvm-node,openebs-lvm-controller)' --ignore-not-found=true --wait=true --timeout=3m
kubectl --kubeconfig "$kubeconfig" delete job --all -n "$namespace" --ignore-not-found=true --wait=true --timeout=3m
kubectl --kubeconfig "$kubeconfig" delete pvc --all -A --ignore-not-found=true --wait=true --timeout=3m
for i in $(seq 1 90); do
  count=0
  if kubectl --kubeconfig "$kubeconfig" get crd lvmvolumes.local.openebs.io >/dev/null 2>&1; then
    count=$(kubectl --kubeconfig "$kubeconfig" get lvmvolumes.local.openebs.io -A --no-headers | awk 'NF {n++} END {print n+0}')
  fi
  test "$count" = 0 && break
  test "$i" -lt 90 || exit 42
  sleep 2
done
if helm status "$lvm_release" --kubeconfig "$kubeconfig" -n "$namespace" >/dev/null 2>&1; then
  helm uninstall "$lvm_release" --kubeconfig "$kubeconfig" -n "$namespace" --wait --timeout 3m
fi
docker exec "$node" bash -ceu '
test "$(lvs --noheadings --select vg_name=iterabase-data -o lv_name | awk "NF {n++} END {print n+0}")" = 0
loop=$(cat /var/lib/iterabase-lvm-loop)
vgremove --yes iterabase-data
pvremove --yes "$loop"
losetup -d "$loop"
rm -f /var/lib/iterabase-lvm-loop /var/lib/iterabase-data.img
'
`
	result, err := cluster.executor.Run(ctx, process.Command{
		Name: "bash", Args: []string{"-ceu", script, "lvm-cleanup", cluster.Kubeconfig, cluster.lvmNamespace, cluster.lvmRelease, cluster.lvmNode},
		Timeout: 10 * time.Minute, OutputName: "kind-cleanup-lvm-storage-" + cluster.Name + ".log",
	})
	if err != nil {
		return fmt.Errorf("cleanup Kind LVM storage before cluster deletion: %w\n%s", err, result.Output)
	}
	return nil
}

func lvmStorageHelmArgs(release, chart, kubeconfig, namespace string) []string {
	// DES-HOR-545-01: mirror Forge's fail-closed admission identity. The substrate
	// release is <platform>-lvm-storage; the control-plane manager SA is
	// <platform>-control-plane-manager, so the authorized identity is derived from
	// the release passed to the substrate install.
	managerIdentity := lvmStorageManagerIdentityForTest(release, namespace)
	return []string{"upgrade", "--install", release, chart, "--kubeconfig", kubeconfig,
		"--namespace", namespace, "--create-namespace", "--set-string", "lvm-localpv.global.kubeletDir=" + kindKubeletDirectory,
		"--set-string", "agentpool.authorizedManagerIdentity=" + managerIdentity,
		"--wait", "--timeout", "8m"}
}

func lvmStorageManagerIdentityForTest(release, namespace string) string {
	platformRelease := strings.TrimSuffix(release, "-lvm-storage")
	return "system:serviceaccount:" + namespace + ":" + platformRelease + "-control-plane-manager"
}

func (cluster *Cluster) validateLVMStorage(ctx context.Context, namespace, nodeName string) error {
	classes, err := cluster.runKubectl(ctx, 30*time.Second, "kind-lvm-storageclasses-"+cluster.Name+".json", "get", "storageclass", "-o", "json")
	if err != nil {
		return fmt.Errorf("read managed LVM StorageClasses: %w", err)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(classes.Output), &list); err != nil {
		return fmt.Errorf("decode managed LVM StorageClasses: %w", err)
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("managed cluster must have exactly two StorageClasses, observed %d", len(list.Items))
	}
	seen := map[string]bool{}
	for _, item := range list.Items {
		name, err := ValidateManagedLVMStorageClass(item)
		if err != nil {
			return err
		}
		seen[name] = true
	}
	if !seen[PlatformDataStorageClass] || !seen[AgentPoolWorkspaceStorageClass] {
		return fmt.Errorf("managed LVM StorageClass set is incomplete")
	}
	if _, err := cluster.runKubectl(ctx, 30*time.Second, "kind-lvm-csidriver-"+cluster.Name+".json", "get", "csidriver", AgentPoolWorkspaceProvisioner, "-o", "json"); err != nil {
		return fmt.Errorf("read OpenEBS LVM CSIDriver: %w", err)
	}
	csiNode, err := cluster.runKubectl(ctx, 30*time.Second, "kind-lvm-csinode-"+cluster.Name+".json", "get", "csinode", nodeName, "-o", "json")
	if err != nil {
		return fmt.Errorf("read OpenEBS LVM CSINode registration: %w", err)
	}
	if err := ValidateLVMCSINodeRegistration([]byte(csiNode.Output), nodeName); err != nil {
		return err
	}
	nodes, err := cluster.runKubectl(ctx, 30*time.Second, "kind-lvmnode-"+cluster.Name+".json", "get", "lvmnodes.local.openebs.io", "-n", namespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("read OpenEBS LVMNode: %w", err)
	}
	var lvmNodes struct {
		Items []struct {
			VolumeGroups []struct {
				Name           string `json:"name"`
				UUID           string `json:"uuid"`
				PVCount        int    `json:"pvCount"`
				MissingPVCount int    `json:"missingPvCount"`
				ThinPools      []any  `json:"thinPools"`
			} `json:"volumeGroups"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(nodes.Output), &lvmNodes); err != nil {
		return fmt.Errorf("decode OpenEBS LVMNode: %w", err)
	}
	if len(lvmNodes.Items) != 1 {
		return fmt.Errorf("expected one OpenEBS LVMNode, observed %d", len(lvmNodes.Items))
	}
	for _, group := range lvmNodes.Items[0].VolumeGroups {
		if group.Name == DataVolumeGroupName && group.UUID != "" && group.PVCount == 1 && group.MissingPVCount == 0 && len(group.ThinPools) == 0 {
			return nil
		}
	}
	return fmt.Errorf("OpenEBS LVMNode has not discovered the exact thick iterabase-data VG")
}

// ValidateLVMCSINodeRegistration rejects a missing, duplicate, wrong-node, or
// incomplete OpenEBS node/topology registration before a WaitForFirstConsumer
// claim can enter an unschedulable capacity loop.
func ValidateLVMCSINodeRegistration(data []byte, nodeName string) error {
	var csiNode struct {
		Spec struct {
			Drivers []struct {
				Name         string   `json:"name"`
				NodeID       string   `json:"nodeID"`
				TopologyKeys []string `json:"topologyKeys"`
			} `json:"drivers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &csiNode); err != nil {
		return fmt.Errorf("decode OpenEBS LVM CSINode registration: %w", err)
	}
	matches := 0
	for _, driver := range csiNode.Spec.Drivers {
		if driver.Name != AgentPoolWorkspaceProvisioner {
			continue
		}
		matches++
		keys := append([]string(nil), driver.TopologyKeys...)
		sort.Strings(keys)
		expectedKeys := []string{"kubernetes.io/hostname", lvmTopologyKey}
		if driver.NodeID != nodeName || !reflect.DeepEqual(keys, expectedKeys) {
			return fmt.Errorf("OpenEBS LVM CSINode registration for %q does not match node/topology contract", nodeName)
		}
	}
	if matches != 1 {
		return fmt.Errorf("expected exactly one OpenEBS LVM CSINode registration for %q, observed %d", nodeName, matches)
	}
	return nil
}

// ValidateManagedLVMStorageClass returns the exact recognized class name and
// rejects any default/provisioner/VG/XFS/thick/shared/policy drift.
func ValidateManagedLVMStorageClass(data []byte) (string, error) {
	var storageClass struct {
		Metadata struct {
			Name        string            `json:"name"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Provisioner          string            `json:"provisioner"`
		ReclaimPolicy        string            `json:"reclaimPolicy"`
		VolumeBindingMode    string            `json:"volumeBindingMode"`
		AllowVolumeExpansion *bool             `json:"allowVolumeExpansion"`
		Parameters           map[string]string `json:"parameters"`
	}
	if err := json.Unmarshal(data, &storageClass); err != nil {
		return "", fmt.Errorf("decode managed LVM StorageClass contract: %w", err)
	}
	shared := "no"
	if storageClass.Metadata.Name == AgentPoolWorkspaceStorageClass {
		shared = "yes"
	} else if storageClass.Metadata.Name != PlatformDataStorageClass {
		return "", fmt.Errorf("unsupported managed StorageClass %q", storageClass.Metadata.Name)
	}
	expected := map[string]string{"storage": "lvm", "vgpattern": "^iterabase-data$", "fsType": "xfs", "thinProvision": "no", "shared": shared}
	if storageClass.Provisioner != AgentPoolWorkspaceProvisioner || storageClass.ReclaimPolicy != "Delete" ||
		storageClass.VolumeBindingMode != "WaitForFirstConsumer" || storageClass.AllowVolumeExpansion == nil || *storageClass.AllowVolumeExpansion ||
		!reflect.DeepEqual(storageClass.Parameters, expected) || storageClass.Metadata.Annotations[defaultClassAnnotation] == "true" ||
		storageClass.Metadata.Annotations[betaDefaultClassAnnotation] == "true" {
		return "", fmt.Errorf("StorageClass %q does not match the exact non-default thick XFS LVM contract", storageClass.Metadata.Name)
	}
	return storageClass.Metadata.Name, nil
}

func (cluster *Cluster) runKubectl(ctx context.Context, timeout time.Duration, outputName string, args ...string) (process.Result, error) {
	result, err := cluster.executor.Run(ctx, process.Command{
		Name: "kubectl", Args: append([]string{"--kubeconfig", cluster.Kubeconfig}, args...),
		Timeout: timeout, OutputName: outputName,
	})
	if err != nil && result.Output != "" {
		return result, fmt.Errorf("%w\n%s", err, result.Output)
	}
	return result, err
}
