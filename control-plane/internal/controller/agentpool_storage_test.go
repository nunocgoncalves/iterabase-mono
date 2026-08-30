package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
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

func localPathClass() *storagev1.StorageClass {
	reclaim := corev1.PersistentVolumeReclaimDelete
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	expand := false
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: agentPoolWorkspaceStorageClass, Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "false"}},
		Provisioner: agentPoolWorkspaceProvisioner, ReclaimPolicy: &reclaim,
		VolumeBindingMode: &binding, AllowVolumeExpansion: &expand,
	}
}

func pendingWorkspacePVC(pool *v1alpha1.AgentPool) *corev1.PersistentVolumeClaim {
	class := agentPoolWorkspaceStorageClass
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: sandboxPVCName(pool), Namespace: pool.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
}

func boundWorkspaceObjects(pool *v1alpha1.AgentPool, path string) []client.Object {
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
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path}},
			NodeAffinity:                  &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1"}}}}}}},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	return []client.Object{localPathClass(), pvc, pv}
}

func storageReconciler(t *testing.T, objects ...client.Object) *AgentPoolReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	return &AgentPoolReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), Scheme: scheme}
}

func TestAssessAgentPoolStorageAllowsInitialWaitForFirstConsumer(t *testing.T) {
	pool := storagePool()
	r := storageReconciler(t, localPathClass(), pendingWorkspacePVC(pool))
	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.CanMount)
	assert.False(t, assessment.Ready)
	assert.Equal(t, storageReasonPVCProvisioning, assessment.Reason)
	assert.Contains(t, assessment.Message, "WaitForFirstConsumer")
}

func TestAssessAgentPoolStorageAcceptsBoundDedicatedRWOPath(t *testing.T) {
	pool := storagePool()
	r := storageReconciler(t, boundWorkspaceObjects(pool, agentPoolWorkspaceMount+"/pvc-pool")...)
	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.CanMount)
	assert.True(t, assessment.Ready)
	assert.Equal(t, storageReasonReady, assessment.Reason)
	assert.Equal(t, agentPoolWorkspaceMount+"/pvc-pool", assessment.VolumeHandle)
	assert.Contains(t, assessment.Message, "ReadWriteOnce")
}

func TestAssessAgentPoolStorageRejectsRootOrDefaultPathFallback(t *testing.T) {
	pool := storagePool()
	for _, path := range []string{"/var/lib/rancher/k3s/storage/pvc-pool", agentPoolWorkspaceMount, agentPoolWorkspaceMount + "/../escape"} {
		t.Run(path, func(t *testing.T) {
			r := storageReconciler(t, boundWorkspaceObjects(pool, path)...)
			assessment := r.assessAgentPoolStorage(context.Background(), pool)
			assert.False(t, assessment.CanMount)
			assert.Equal(t, storageReasonPVCUnavailable, assessment.Reason)
			assert.Contains(t, assessment.Message, "fallback is refused")
		})
	}
}

func TestValidateAgentPoolStorageClassExactContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*storagev1.StorageClass)
	}{
		{name: "wrong provisioner", mutate: func(c *storagev1.StorageClass) { c.Provisioner = "driver.longhorn.io" }},
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
		{name: "path parameter", mutate: func(c *storagev1.StorageClass) { c.Parameters = map[string]string{"nodePath": "/tmp"} }},
	}
	assert.Empty(t, validateAgentPoolStorageClass(localPathClass()))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class := localPathClass()
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
