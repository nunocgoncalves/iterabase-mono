package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
)

const managedSingleNodeValues = `storage:
  rwx:
    mode: managed-longhorn
    storageClassName: iterabase-rwx
    managedLonghorn:
      topology: single-node
`

func TestRWXStorageSubstrateRepository(t *testing.T) {
	repository, err := rwxStorageSubstrateRepository("oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform")
	require.NoError(t, err)
	assert.Equal(t, "oci://ghcr.io/nunocgoncalves/iterabase-charts/rwx-storage-substrate", repository)
	_, err = rwxStorageSubstrateRepository("oci://example.invalid/custom-platform")
	require.ErrorContains(t, err, "must end in /iterabase-platform")
}

func TestResolveStorageSelectionMergesClientOverride(t *testing.T) {
	o := &fakeOverlayer{readFileValues: map[string]string{
		"values.yaml": `storage:
  rwx:
    mode: external
    storageClassName: customer-a
`,
		"values.client.yaml": managedSingleNodeValues,
	}}
	selection, err := resolveStorageSelection(context.Background(), o, "/overlay")
	require.NoError(t, err)
	assert.Equal(t, storageSelection{
		Mode: storageModeManagedLonghorn, StorageClassName: managedStorageClass, Topology: config.ModeSingleNode,
	}, selection)
}

func TestResolveStorageSelectionRejectsContradictoryValues(t *testing.T) {
	tests := []struct {
		name   string
		values string
		want   string
	}{
		{name: "managed wrong class", values: `storage: {rwx: {mode: managed-longhorn, storageClassName: wrong, managedLonghorn: {topology: single-node}}}`, want: "storageClassName=iterabase-rwx"},
		{name: "external topology", values: `storage: {rwx: {mode: external, storageClassName: customer, managedLonghorn: {topology: single-node}}}`, want: "rejects"},
		{name: "unknown mode", values: `storage: {rwx: {mode: automatic, storageClassName: customer}}`, want: "managed-longhorn or external"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &fakeOverlayer{readFileValues: map[string]string{"values.yaml": tt.values, "values.client.yaml": ""}}
			_, err := resolveStorageSelection(context.Background(), o, "/overlay")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestApplyManagedRWXOrdersPrerequisitesAndCompanionBeforePlatform(t *testing.T) {
	useTempHome(t)
	cfg := testConfigWithChart()
	cfg.Spec.Chart.Version = rwxStorageSubstrateFirstVersion
	cfg.Spec.Overlay = config.Overlay{Repo: "file:///overlay", Ref: "master"}
	p := &fakeProv{pf: readyPf(), kubeconfig: []byte(minKubeconfig), readyAfterInstall: true}
	d := &fakeDeployer{}
	o := &fakeOverlayer{cloneCommit: "deadbeef", readFileValues: map[string]string{
		"values.yaml": managedSingleNodeValues, "values.client.yaml": "", "secrets.yaml": "",
	}}

	res, err := Apply(context.Background(), cfg, p, d, o, &fakeFluxer{}, ApplyOpts{
		ReadyTimeout: time.Second, ReadyInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, storageModeManagedLonghorn, res.RWXStorageMode)
	assert.True(t, res.RWXStoragePrerequisitesReady)
	assert.True(t, res.RWXStorageSubstrateApplied)
	assert.Equal(t, 1, p.ensureStorageDepsCalls)
	require.Len(t, d.applyCalls, 3)
	assert.Equal(t, "opo1-cert-manager", d.applyCalls[0].release)
	assert.Equal(t, "opo1-rwx-storage", d.applyCalls[1].release)
	assert.Equal(t, rwxStorageNamespace, d.applyCalls[1].namespace)
	assert.Equal(t, "oci://ghcr.io/nunocgoncalves/rwx-storage-substrate", d.applyCalls[1].repository)
	assert.Equal(t, []string{"validation.attestationNamespace=iterabase-system"}, d.applyCalls[1].values)
	assert.Equal(t, "65m", d.applyCalls[1].timeout)
	assert.Equal(t, "opo1", d.applyCalls[2].release)
}

func TestApplyExternalRWXInstallsNoBackendOrHostPackages(t *testing.T) {
	useTempHome(t)
	cfg := testConfigWithChart()
	cfg.Spec.Chart.Version = rwxStorageSubstrateFirstVersion
	cfg.Spec.Overlay = config.Overlay{Repo: "file:///overlay", Ref: "master"}
	p := &fakeProv{pf: readyPf(), kubeconfig: []byte(minKubeconfig), readyAfterInstall: true}
	d := &fakeDeployer{}
	o := &fakeOverlayer{cloneCommit: "deadbeef", readFileValues: map[string]string{
		"values.yaml":        `storage: {rwx: {mode: external, storageClassName: customer-production-rwx}}`,
		"values.client.yaml": "", "secrets.yaml": "",
	}}

	res, err := Apply(context.Background(), cfg, p, d, o, &fakeFluxer{}, ApplyOpts{
		ReadyTimeout: time.Second, ReadyInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, storageModeExternal, res.RWXStorageMode)
	assert.False(t, res.RWXStorageSubstrateApplied)
	assert.Zero(t, p.ensureStorageDepsCalls)
	require.Len(t, d.applyCalls, 2)
	assert.Equal(t, "opo1-cert-manager", d.applyCalls[0].release)
	assert.Equal(t, "opo1", d.applyCalls[1].release)
}

func TestDestroyManagedRWXRefusalPreservesCluster(t *testing.T) {
	cfg := testConfigWithChart()
	cfg.Spec.Chart.Version = rwxStorageSubstrateFirstVersion
	p := &fakeProv{pf: readyPf(), state: inSyncState()}
	p.pf.Installed = true
	d := &fakeDeployer{uninstallErrors: map[string]error{
		"opo1-rwx-storage": errors.New("retained iterabase-rwx PV remains"),
	}}

	err := Destroy(context.Background(), cfg, p, d, nil, nil)
	require.ErrorContains(t, err, "cluster preserved")
	assert.True(t, p.state.Installed)
	require.Equal(t, []uninstallCall{
		{release: "opo1", namespace: "iterabase-system"},
		{release: "opo1-rwx-storage", namespace: rwxStorageNamespace},
	}, d.uninstallCalls)
}

func TestDestroyManagedRWXAfterDispositionUsesReverseCompanionOrder(t *testing.T) {
	cfg := testConfigWithChart()
	cfg.Spec.Chart.Version = rwxStorageSubstrateFirstVersion
	p := &fakeProv{pf: readyPf(), state: inSyncState()}
	p.pf.Installed = true
	d := &fakeDeployer{}

	require.NoError(t, Destroy(context.Background(), cfg, p, d, nil, nil))
	assert.False(t, p.state.Installed)
	require.Equal(t, []uninstallCall{
		{release: "opo1", namespace: "iterabase-system"},
		{release: "opo1-rwx-storage", namespace: rwxStorageNamespace},
		{release: "opo1-cert-manager", namespace: "iterabase-system"},
	}, d.uninstallCalls)
}

func TestApplyManagedRWXPreservesClusterOnPrerequisiteFailure(t *testing.T) {
	useTempHome(t)
	cfg := testConfigWithChart()
	cfg.Spec.Chart.Version = rwxStorageSubstrateFirstVersion
	cfg.Spec.Overlay = config.Overlay{Repo: "file:///overlay", Ref: "master"}
	p := &fakeProv{
		pf: readyPf(), kubeconfig: []byte(minKubeconfig), readyAfterInstall: true,
		ensureStorageErr: errors.New("mount propagation is private"),
	}
	d := &fakeDeployer{}
	o := &fakeOverlayer{cloneCommit: "deadbeef", readFileValues: map[string]string{
		"values.yaml": managedSingleNodeValues, "values.client.yaml": "", "secrets.yaml": "",
	}}

	_, err := Apply(context.Background(), cfg, p, d, o, &fakeFluxer{}, ApplyOpts{
		ReadyTimeout: time.Second, ReadyInterval: 10 * time.Millisecond,
	})
	require.ErrorContains(t, err, "mount propagation is private")
	require.Len(t, d.applyCalls, 1, "certificate substrate may exist, but storage/platform must not mutate")
	assert.Equal(t, "opo1-cert-manager", d.applyCalls[0].release)
}
