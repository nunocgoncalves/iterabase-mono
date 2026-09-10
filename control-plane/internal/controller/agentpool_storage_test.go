package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/gateway"
)

func storagePool() *v1alpha1.AgentPool {
	return &v1alpha1.AgentPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "iterabase-system"},
		Spec: v1alpha1.AgentPoolSpec{
			Replicas: 3,
			Sandbox: v1alpha1.SandboxSpec{
				StorageClassName: agentPoolWorkspaceStorageClass,
				AccessMode:       corev1.ReadWriteOnce,
				Size:             resource.MustParse("10Gi"),
			},
		},
	}
}

func lvmAgentPoolClass() *storagev1.StorageClass {
	reclaim := corev1.PersistentVolumeReclaimDelete
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	expand := false
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: agentPoolWorkspaceStorageClass, Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "false"}},
		Provisioner: agentPoolWorkspaceProvisioner, ReclaimPolicy: &reclaim,
		VolumeBindingMode: &binding, AllowVolumeExpansion: &expand,
		Parameters: map[string]string{
			"storage": "lvm", "vgpattern": "^iterabase-data$", "fsType": "xfs", "thinProvision": "no", "shared": "yes",
		},
	}
}

func pendingWorkspacePVC(pool *v1alpha1.AgentPool) *corev1.PersistentVolumeClaim {
	class := agentPoolWorkspaceStorageClass
	filesystem := corev1.PersistentVolumeFilesystem
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: sandboxPVCName(pool), Namespace: pool.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &filesystem,
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
}

func boundWorkspaceObjects(pool *v1alpha1.AgentPool) []client.Object {
	pvc := pendingWorkspacePVC(pool)
	pvc.Spec.VolumeName = "pool-pv"
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}
	filesystem := corev1.PersistentVolumeFilesystem
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-pv"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName:              agentPoolWorkspaceStorageClass,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:                    &filesystem,
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: agentPoolWorkspaceProvisioner, FSType: "xfs", VolumeHandle: "pvc-volume-1",
				VolumeAttributes: map[string]string{"openebs.io/volgroup": agentPoolWorkspaceVolumeGroup},
			}},
			NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "openebs.io/nodename", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1"}},
			}}}}},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "local.openebs.io/v1alpha1", "kind": "LVMVolume",
		"metadata": map[string]any{"name": "pvc-volume-1", "namespace": pool.Namespace},
		"spec": map[string]any{
			"capacity": "10Gi", "ownerNodeID": "node-1", "shared": "yes", "thinProvision": "no",
			"vgPattern": "^iterabase-data$", "volGroup": agentPoolWorkspaceVolumeGroup,
		},
		"status": map[string]any{"state": "Ready"},
	}}
	volume.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMVolume"})
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "local.openebs.io/v1alpha1", "kind": "LVMNode",
		"metadata": map[string]any{"name": "node-1", "namespace": pool.Namespace},
		"volumeGroups": []any{map[string]any{
			"name": agentPoolWorkspaceVolumeGroup, "uuid": "vg-uuid", "missingPvCount": int64(0), "thinPools": []any{},
		}},
	}}
	node.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMNode"})
	return []client.Object{lvmAgentPoolClass(), pvc, pv, volume, node}
}

func storageReconciler(t *testing.T, objects ...client.Object) *AgentPoolReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	return &AgentPoolReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Scheme: scheme}
}

type staticWorkspaceCapacityReader struct {
	status  gateway.WorkspaceCapacityStatus
	err     error
	poolKey string
}

func (r *staticWorkspaceCapacityReader) WorkspaceCapacityStatus(_ context.Context, poolKey string) (gateway.WorkspaceCapacityStatus, error) {
	r.poolKey = poolKey
	return r.status, r.err
}

func TestWorkspaceCapacityConditionTransitionsPerPoolAndSurvivesReplacementBand(t *testing.T) {
	now := time.Now()
	reader := &staticWorkspaceCapacityReader{}
	r := &AgentPoolReconciler{CapacityReader: reader}
	pool := storagePool()

	reader.status = gateway.WorkspaceCapacityStatus{Observed: true, FreeBytes: 24, CapacityBytes: 100, FreeRatio: 0.24, Warning: true, ObservedAt: &now}
	notice := r.setWorkspaceCapacityCondition(context.Background(), pool)
	assert.Equal(t, "iterabase-system/pool", reader.poolKey)
	condition := meta.FindStatusCondition(pool.Status.Conditions, storageConditionWorkspaceCapacityHealthy)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonWorkspaceCapacityWarning, condition.Reason)
	assert.Contains(t, notice, "credits remain open")

	reader.status = gateway.WorkspaceCapacityStatus{Observed: true, FreeBytes: 20, CapacityBytes: 100, FreeRatio: 0.20, Warning: true, CreditGated: true, ObservedAt: &now}
	r.setWorkspaceCapacityCondition(context.Background(), pool)
	condition = meta.FindStatusCondition(pool.Status.Conditions, storageConditionWorkspaceCapacityHealthy)
	require.NotNil(t, condition)
	assert.Equal(t, storageReasonWorkspaceCapacityGated, condition.Reason)

	reader.status = gateway.WorkspaceCapacityStatus{Observed: true, FreeBytes: 24, CapacityBytes: 100, FreeRatio: 0.24, Warning: true, CreditGated: true, ObservedAt: &now}
	r.setWorkspaceCapacityCondition(context.Background(), pool)
	condition = meta.FindStatusCondition(pool.Status.Conditions, storageConditionWorkspaceCapacityHealthy)
	require.NotNil(t, condition)
	assert.Equal(t, storageReasonWorkspaceCapacityGated, condition.Reason)

	reader.status = gateway.WorkspaceCapacityStatus{Observed: true, FreeBytes: 25, CapacityBytes: 100, FreeRatio: 0.25, ObservedAt: &now}
	assert.Empty(t, r.setWorkspaceCapacityCondition(context.Background(), pool))
	condition = meta.FindStatusCondition(pool.Status.Conditions, storageConditionWorkspaceCapacityHealthy)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
}

func TestWorkspaceCapacityConditionFailsClosedOnUnavailableObservation(t *testing.T) {
	reader := &staticWorkspaceCapacityReader{err: errors.New("database unavailable")}
	r := &AgentPoolReconciler{CapacityReader: reader}
	pool := storagePool()
	notice := r.setWorkspaceCapacityCondition(context.Background(), pool)
	condition := meta.FindStatusCondition(pool.Status.Conditions, storageConditionWorkspaceCapacityHealthy)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionUnknown, condition.Status)
	assert.Equal(t, storageReasonWorkspaceCapacityUnknown, condition.Reason)
	assert.Contains(t, notice, "fails closed")
}

func TestAssessAgentPoolStorageAllowsInitialWaitForFirstConsumer(t *testing.T) {
	pool := storagePool()
	r := storageReconciler(t, lvmAgentPoolClass(), pendingWorkspacePVC(pool))
	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.CanMount)
	assert.False(t, assessment.Ready)
	assert.Equal(t, storageReasonPVCProvisioning, assessment.Reason)
	assert.Contains(t, assessment.Message, "WaitForFirstConsumer")
}

func TestAssessAgentPoolStorageAcceptsBoundOpenEBSLVMVolume(t *testing.T) {
	pool := storagePool()
	r := storageReconciler(t, boundWorkspaceObjects(pool)...)
	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.CanMount)
	assert.True(t, assessment.Ready)
	assert.Equal(t, storageReasonReady, assessment.Reason)
	assert.Equal(t, "pvc-volume-1", assessment.VolumeHandle)
	assert.Contains(t, assessment.Message, "shared=yes")
	assert.Contains(t, assessment.Message, "vg=iterabase-data")
}

type storageQueryClient struct {
	client.Client
	getByKind  map[string]int
	listByKind map[string]int
	failKind   string
	failErr    error
}

func (c *storageQueryClient) Get(ctx context.Context, key types.NamespacedName, object client.Object, opts ...client.GetOption) error {
	kind := object.GetObjectKind().GroupVersionKind().Kind
	c.getByKind[kind]++
	if c.failKind != "" && kind == c.failKind {
		return c.failErr
	}
	return c.Client.Get(ctx, key, object, opts...)
}

func (c *storageQueryClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	kind := list.GetObjectKind().GroupVersionKind().Kind
	c.listByKind[kind]++
	return c.Client.List(ctx, list, opts...)
}

func TestAssessAgentPoolStorageUsesDirectSingleOpenEBSObservations(t *testing.T) {
	pool := storagePool()
	r := storageReconciler(t, boundWorkspaceObjects(pool)...)
	queries := &storageQueryClient{Client: r.Client, getByKind: map[string]int{}, listByKind: map[string]int{}}
	r.Client = queries
	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	require.True(t, assessment.Ready, "%+v", assessment)
	assert.Equal(t, 1, queries.getByKind["LVMVolume"])
	assert.Equal(t, 1, queries.getByKind["LVMNode"])
	assert.Zero(t, queries.listByKind["LVMVolumeList"])
	assert.Zero(t, queries.listByKind["LVMNodeList"])
}

func TestStorageObservationErrorFailsClosedWithoutAuthorizingQuiescence(t *testing.T) {
	pool := storagePool()
	r := storageReconciler(t, boundWorkspaceObjects(pool)...)
	queries := &storageQueryClient{
		Client: r.Client, getByKind: map[string]int{}, listByKind: map[string]int{},
		failKind: "LVMVolume", failErr: errors.New("cache transport unavailable"),
	}
	r.Client = queries
	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.False(t, assessment.Ready)
	assert.False(t, assessment.CanMount)
	assert.True(t, assessment.ObservationUnknown)
	assert.False(t, assessment.ConfirmedUnsafe)
	assert.False(t, storageQuiescenceRequired(assessment), "unknown observations must retain healthy workers while withdrawing readiness")
	assert.ErrorIs(t, assessment.ObservationErr, queries.failErr)

	unsafe := assessment
	unsafe.ObservationUnknown = false
	unsafe.ConfirmedUnsafe = true
	assert.True(t, storageQuiescenceRequired(unsafe), "positively observed drift must quiesce")
}

func TestAssessAgentPoolStorageRejectsCSIAndOpenEBSIdentityDrift(t *testing.T) {
	pool := storagePool()
	for _, tc := range []struct {
		name   string
		mutate func([]client.Object)
		want   string
	}{
		{name: "host path", mutate: func(objects []client.Object) {
			pv := objects[2].(*corev1.PersistentVolume)
			pv.Spec.CSI = nil
			pv.Spec.HostPath = &corev1.HostPathVolumeSource{Path: "/var/lib/rancher/k3s/storage"}
		}, want: "OpenEBS CSI"},
		{name: "wrong fs", mutate: func(objects []client.Object) { objects[2].(*corev1.PersistentVolume).Spec.CSI.FSType = "ext4" }, want: "fsType=xfs"},
		{name: "wrong vg", mutate: func(objects []client.Object) {
			objects[2].(*corev1.PersistentVolume).Spec.CSI.VolumeAttributes["openebs.io/volgroup"] = "root"
		}, want: "volgroup=iterabase-data"},
		{name: "thin volume", mutate: func(objects []client.Object) {
			_ = unstructured.SetNestedField(objects[3].(*unstructured.Unstructured).Object, "yes", "spec", "thinProvision")
		}, want: "thin=yes"},
		{name: "wrong node", mutate: func(objects []client.Object) {
			_ = unstructured.SetNestedField(objects[3].(*unstructured.Unstructured).Object, "node-2", "spec", "ownerNodeID")
		}, want: "topology"},
		{name: "extra topology", mutate: func(objects []client.Object) {
			pv := objects[2].(*corev1.PersistentVolume)
			pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions = append(
				pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions,
				corev1.NodeSelectorRequirement{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1"}},
			)
		}, want: "topology"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := boundWorkspaceObjects(pool)
			tc.mutate(objects)
			assessment := storageReconciler(t, objects...).assessAgentPoolStorage(context.Background(), pool)
			assert.False(t, assessment.CanMount)
			assert.Equal(t, storageReasonPVCUnavailable, assessment.Reason)
			assert.Contains(t, assessment.Message, tc.want)
		})
	}
}

func TestValidateAgentPoolStorageClassExactContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*storagev1.StorageClass)
	}{
		{name: "wrong provisioner", mutate: func(c *storagev1.StorageClass) { c.Provisioner = "rancher.io/local-path" }},
		{name: "default", mutate: func(c *storagev1.StorageClass) { c.Annotations["storageclass.kubernetes.io/is-default-class"] = "true" }},
		{name: "expandable", mutate: func(c *storagev1.StorageClass) { yes := true; c.AllowVolumeExpansion = &yes }},
		{name: "retain", mutate: func(c *storagev1.StorageClass) {
			retain := corev1.PersistentVolumeReclaimRetain
			c.ReclaimPolicy = &retain
		}},
		{name: "immediate", mutate: func(c *storagev1.StorageClass) {
			immediate := storagev1.VolumeBindingImmediate
			c.VolumeBindingMode = &immediate
		}},
		{name: "wrong filesystem", mutate: func(c *storagev1.StorageClass) { c.Parameters["fsType"] = "ext4" }},
		{name: "thin", mutate: func(c *storagev1.StorageClass) { c.Parameters["thinProvision"] = "yes" }},
		{name: "unshared", mutate: func(c *storagev1.StorageClass) { c.Parameters["shared"] = "no" }},
		{name: "alternate vg", mutate: func(c *storagev1.StorageClass) { c.Parameters["vgpattern"] = ".*" }},
	}
	assert.Empty(t, validateAgentPoolStorageClass(lvmAgentPoolClass()))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class := lvmAgentPoolClass()
			tc.mutate(class)
			assert.NotEmpty(t, validateAgentPoolStorageClass(class))
		})
	}
}

func TestAssessAgentPoolStorageRejectsAccessAndClassDrift(t *testing.T) {
	pool := storagePool()
	pool.Spec.Sandbox.AccessMode = corev1.ReadWriteMany
	assessment := storageReconciler(t).assessAgentPoolStorage(context.Background(), pool)
	assert.False(t, assessment.CanMount)
	assert.Contains(t, assessment.Message, "no V2 fallback")

	pool = storagePool()
	pool.Spec.Sandbox.StorageClassName = "local-path"
	assessment = storageReconciler(t).assessAgentPoolStorage(context.Background(), pool)
	assert.False(t, assessment.CanMount)
	assert.Contains(t, assessment.Message, "no V2 fallback")
}
