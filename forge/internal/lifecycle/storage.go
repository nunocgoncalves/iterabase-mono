package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/deployer"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/overlayer"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

const (
	rwxStorageSubstrateChart        = "rwx-storage-substrate"
	rwxStorageSubstrateFirstVersion = "0.3.23"
	rwxStorageNamespace             = "longhorn-system"
	storageModeManagedLonghorn      = "managed-longhorn"
	storageModeExternal             = "external"
	managedStorageClass             = "iterabase-rwx"
)

type storageSelection struct {
	Mode               string
	StorageClassName   string
	Topology           string
	InternalTLSEnabled bool
}

func defaultStorageSelection() storageSelection {
	return storageSelection{Mode: storageModeExternal, StorageClassName: "external-rwx-required"}
}

func rwxStorageSubstrateRequired(version string) (bool, error) {
	return chartVersionAtLeast(version, rwxStorageSubstrateFirstVersion)
}

func rwxStorageSubstrateRepository(platformRepository string) (string, error) {
	i := strings.LastIndex(platformRepository, "/")
	if i < 0 || platformRepository[i+1:] != "iterabase-platform" {
		return "", fmt.Errorf("platform chart repository %q must end in /iterabase-platform to resolve its RWX storage substrate companion", platformRepository)
	}
	return platformRepository[:i+1] + rwxStorageSubstrateChart, nil
}

func rwxStorageSubstrateRelease(platformRelease string) string {
	return platformRelease + "-rwx-storage"
}

func resolveStorageSelection(ctx context.Context, o overlayer.Overlayer, overlayDest string) (storageSelection, error) {
	selection := defaultStorageSelection()
	if overlayDest == "" {
		return selection, nil
	}
	if o == nil {
		return selection, fmt.Errorf("resolve storage values: overlay reader is not configured")
	}

	merged := map[string]any{}
	for _, path := range []string{"values.yaml", "values.client.yaml"} {
		content, err := o.ReadFile(ctx, overlayDest, path)
		if err != nil {
			return selection, fmt.Errorf("resolve storage values from %s: %w", path, err)
		}
		values := map[string]any{}
		if err := yaml.Unmarshal([]byte(content), &values); err != nil {
			return selection, fmt.Errorf("decode storage values from %s: %w", path, err)
		}
		mergeValues(merged, values)
	}

	global, _ := merged["global"].(map[string]any)
	if internalTLS, _ := global["internalTLS"].(map[string]any); internalTLS != nil {
		if enabled, exists := internalTLS["enabled"]; exists {
			value, ok := enabled.(bool)
			if !ok {
				return selection, fmt.Errorf("global.internalTLS.enabled must be a boolean")
			}
			selection.InternalTLSEnabled = value
		}
	}

	storage, _ := merged["storage"].(map[string]any)
	rwx, _ := storage["rwx"].(map[string]any)
	if rwx == nil {
		return selection, nil
	}
	if mode, ok := rwx["mode"].(string); ok {
		selection.Mode = mode
	}
	if class, ok := rwx["storageClassName"].(string); ok {
		selection.StorageClassName = class
	}
	if managed, ok := rwx["managedLonghorn"].(map[string]any); ok {
		if topology, ok := managed["topology"].(string); ok {
			selection.Topology = topology
		}
	}
	if err := validateStorageSelection(selection); err != nil {
		return selection, err
	}
	return selection, nil
}

func mergeValues(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcIsMap := value.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeValues(dstMap, srcMap)
			continue
		}
		dst[key] = value
	}
}

func validateStorageSelection(selection storageSelection) error {
	switch selection.Mode {
	case storageModeManagedLonghorn:
		if selection.StorageClassName != managedStorageClass {
			return fmt.Errorf("managed-longhorn requires storage.rwx.storageClassName=%s", managedStorageClass)
		}
		if selection.Topology != config.ModeSingleNode && selection.Topology != "three-node" {
			return fmt.Errorf("managed-longhorn requires storage.rwx.managedLonghorn.topology=single-node or three-node")
		}
	case storageModeExternal:
		if selection.StorageClassName == "" {
			return fmt.Errorf("external storage requires an exact storage.rwx.storageClassName")
		}
		if selection.Topology != "" {
			return fmt.Errorf("external storage rejects storage.rwx.managedLonghorn settings")
		}
	default:
		return fmt.Errorf("storage.rwx.mode must be managed-longhorn or external")
	}
	return nil
}

func uninstallRWXStorageBeforeDestroy(ctx context.Context, cfg *config.Cluster, d deployer.Deployer) error {
	if d == nil || cfg.Spec.Chart.Version == "" {
		return nil
	}
	ch := cfg.Spec.Chart
	required, err := rwxStorageSubstrateRequired(ch.Version)
	if err != nil {
		return fmt.Errorf("determine managed RWX storage lifecycle: %w", err)
	}
	if !required {
		return nil
	}
	// This must precede Flux, overlay, platform, certificate, GPU, and cluster
	// teardown. A failed pre-delete hook is a hard refusal: the complete
	// installation remains available for operator disposition.
	if err := d.UninstallChart(ctx, rwxStorageSubstrateRelease(ch.Release), rwxStorageNamespace); err != nil {
		return fmt.Errorf("managed RWX storage uninstall refused; cluster preserved before platform teardown: %w", err)
	}
	return nil
}

func applyRWXStorageSubstrate(
	ctx context.Context,
	cfg *config.Cluster,
	p provisioner.Provisioner,
	d deployer.Deployer,
	o overlayer.Overlayer,
	opts ApplyOpts,
	res *Result,
	overlayDest string,
) error {
	selection, err := resolveStorageSelection(ctx, o, overlayDest)
	if err != nil {
		return err
	}
	res.RWXStorageMode = selection.Mode
	if selection.Mode == storageModeExternal || d == nil || opts.SkipChart || cfg.Spec.Chart.Version == "" {
		return nil
	}
	if selection.Topology != config.ModeSingleNode {
		return fmt.Errorf("forge currently owns the approved single-node reference substrate only; managed topology %q must use the documented direct three-node chart procedure", selection.Topology)
	}
	required, err := rwxStorageSubstrateRequired(cfg.Spec.Chart.Version)
	if err != nil {
		return err
	}
	if !required {
		return fmt.Errorf("managed-longhorn requires platform chart %s or newer", rwxStorageSubstrateFirstVersion)
	}
	if err := p.EnsureRWXStoragePrerequisites(ctx); err != nil {
		auditFail(cfg, "apply-rwx-storage-prerequisites", err)
		return fmt.Errorf("managed RWX host prerequisites: %w", err)
	}
	res.RWXStoragePrerequisitesReady = true

	repository, err := rwxStorageSubstrateRepository(cfg.Spec.Chart.Repository)
	if err != nil {
		return err
	}
	// The companion receives only the approved semantic selection. Passing the
	// complete overlay here would expose the upstream longhorn.* namespace as an
	// unsupported customer tuning surface even though the platform release may
	// legitimately consume those same files for unrelated product values.
	dopts := deployer.ApplyOpts{
		Release:    rwxStorageSubstrateRelease(cfg.Spec.Chart.Release),
		Repository: repository,
		Version:    cfg.Spec.Chart.Version,
		Namespace:  rwxStorageNamespace,
		Timeout:    "65m",
		Values: []string{
			"storage.rwx.mode=" + selection.Mode,
			"storage.rwx.storageClassName=" + selection.StorageClassName,
			"storage.rwx.managedLonghorn.topology=" + selection.Topology,
			"global.internalTLS.enabled=" + strconv.FormatBool(selection.InternalTLSEnabled),
			"validation.attestationNamespace=" + cfg.Spec.Chart.Namespace,
		},
	}
	if err := d.Apply(ctx, dopts); err != nil {
		auditFail(cfg, "apply-rwx-storage-substrate", err)
		return fmt.Errorf("RWX storage substrate: %w", err)
	}
	res.RWXStorageSubstrateApplied = true
	return nil
}
