package kind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
)

// Forge-owned AgentPool workspace substrate identities. Kind uses the same
// fail-closed StorageClass contract while production disk/path isolation stays
// owned and reconciled by Forge.
const (
	AgentPoolWorkspaceStorageClass = "iterabase-agentpool-local-path"
	AgentPoolWorkspaceProvisioner  = "rancher.io/local-path"
	agentPoolWorkspacePath         = "/var/lib/iterabase/agentpool-workspaces"
	defaultClassAnnotation         = "storageclass.kubernetes.io/is-default-class"
	betaDefaultClassAnnotation     = "storageclass.beta.kubernetes.io/is-default-class"
)

const agentPoolWorkspaceStorageClassManifest = `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: iterabase-agentpool-local-path
  annotations:
    storageclass.kubernetes.io/is-default-class: "false"
provisioner: rancher.io/local-path
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: false
`

// AgentPoolWorkspaceStorageClassManifest returns the exact declarative class
// used by Kind AgentPool scenarios. It intentionally has no parameters and
// never aliases or mutates the default local-path class.
func AgentPoolWorkspaceStorageClassManifest() string {
	return agentPoolWorkspaceStorageClassManifest
}

// ConfigureAgentPoolLocalPathStorage switches Kind's pinned single-path
// provisioner to the dedicated synthetic workspace path, applies the exact
// non-default Forge-owned class, reloads the provisioner, and verifies the
// resulting contract. Call it only after platform-default claims are bound and
// before creating an AgentPool.
func (cluster *Cluster) ConfigureAgentPoolLocalPathStorage(ctx context.Context) error {
	if cluster == nil || cluster.executor == nil || cluster.Kubeconfig == "" {
		return fmt.Errorf("kind AgentPool storage requires an initialized cluster")
	}
	configJSON := `{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":["` + agentPoolWorkspacePath + `"]}]}`
	if _, err := cluster.runKubectl(ctx, 30*time.Second, "kind-agentpool-storage-config-"+cluster.Name+".log",
		"patch", "configmap/local-path-config", "-n", "local-path-storage", "--type=merge", "-p", fmt.Sprintf(`{"data":{"config.json":%q}}`, configJSON)); err != nil {
		return fmt.Errorf("configure Kind AgentPool local-path isolation: %w", err)
	}

	manifest, err := os.CreateTemp(cluster.tempDir, "iterabase-agentpool-storageclass-*.yaml")
	if err != nil {
		return fmt.Errorf("create AgentPool StorageClass manifest: %w", err)
	}
	manifestPath := manifest.Name()
	defer func() { _ = os.Remove(manifestPath) }()
	if _, err := manifest.WriteString(agentPoolWorkspaceStorageClassManifest); err != nil {
		_ = manifest.Close()
		return fmt.Errorf("write AgentPool StorageClass manifest: %w", err)
	}
	if err := manifest.Close(); err != nil {
		return fmt.Errorf("close AgentPool StorageClass manifest: %w", err)
	}
	if _, err := cluster.runKubectl(ctx, 30*time.Second, "kind-agentpool-storageclass-"+cluster.Name+".log", "apply", "-f", manifestPath); err != nil {
		return fmt.Errorf("apply AgentPool StorageClass: %w", err)
	}
	if _, err := cluster.runKubectl(ctx, 2*time.Minute, "kind-agentpool-provisioner-restart-"+cluster.Name+".log",
		"rollout", "restart", "deployment/local-path-provisioner", "-n", "local-path-storage"); err != nil {
		return fmt.Errorf("restart Kind local-path provisioner: %w", err)
	}
	if _, err := cluster.runKubectl(ctx, 2*time.Minute, "kind-agentpool-provisioner-ready-"+cluster.Name+".log",
		"rollout", "status", "deployment/local-path-provisioner", "-n", "local-path-storage", "--timeout=90s"); err != nil {
		return fmt.Errorf("wait for Kind local-path provisioner: %w", err)
	}
	storageClass, err := cluster.runKubectl(ctx, 30*time.Second, "kind-agentpool-storageclass-contract-"+cluster.Name+".json",
		"get", "storageclass", AgentPoolWorkspaceStorageClass, "-o", "json")
	if err != nil {
		return fmt.Errorf("read AgentPool StorageClass contract: %w", err)
	}
	if err := ValidateAgentPoolWorkspaceStorageClass([]byte(storageClass.Output)); err != nil {
		return err
	}
	config, err := cluster.runKubectl(ctx, 30*time.Second, "kind-agentpool-storage-config-contract-"+cluster.Name+".txt",
		"get", "configmap/local-path-config", "-n", "local-path-storage", "-o", `jsonpath={.data.config\.json}`)
	if err != nil {
		return fmt.Errorf("read Kind local-path configuration: %w", err)
	}
	if strings.TrimSpace(config.Output) != configJSON {
		return fmt.Errorf("kind AgentPool local-path configuration does not use the dedicated workspace path")
	}
	return nil
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

// ValidateAgentPoolWorkspaceStorageClass rejects any default, provisioner,
// policy, expansion, or parameter drift from the Forge-owned contract.
func ValidateAgentPoolWorkspaceStorageClass(data []byte) error {
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
		return fmt.Errorf("decode AgentPool StorageClass contract: %w", err)
	}
	if storageClass.Metadata.Name != AgentPoolWorkspaceStorageClass ||
		storageClass.Provisioner != AgentPoolWorkspaceProvisioner ||
		storageClass.ReclaimPolicy != "Delete" ||
		storageClass.VolumeBindingMode != "WaitForFirstConsumer" ||
		storageClass.AllowVolumeExpansion == nil || *storageClass.AllowVolumeExpansion ||
		len(storageClass.Parameters) != 0 ||
		storageClass.Metadata.Annotations[defaultClassAnnotation] != "false" {
		return fmt.Errorf("AgentPool StorageClass does not match the exact non-default local-path RWO contract")
	}
	if _, exists := storageClass.Metadata.Annotations[betaDefaultClassAnnotation]; exists {
		return fmt.Errorf("AgentPool StorageClass carries obsolete default-class metadata")
	}
	return nil
}
