package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
)

func storageTestObjects(pool *v1alpha1.AgentPool, mode, className, provisioner string) []client.Object {
	reclaim := corev1.PersistentVolumeReclaimRetain
	expand := true
	classUID := types.UID("class-uid")
	class := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: className, UID: classUID},
		Provisioner: provisioner, ReclaimPolicy: &reclaim, AllowVolumeExpansion: &expand,
	}
	contract := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-rwx-storage-contract", Namespace: pool.Namespace, Labels: map[string]string{storageContractLabel: "true"}},
		Data:       map[string]string{"contractVersion": storageContractVersion, "mode": mode, "storageClassName": className},
	}
	attestation := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "iterabase-rwx-conformance", Namespace: pool.Namespace, Labels: map[string]string{storageConformanceLabel: "true"}},
		Data: map[string]string{
			"contractVersion": storageContractVersion, "storageClassName": className,
			"storageClassUID": string(classUID), "provisioner": provisioner,
			"result": "pass", "validatedAt": "2026-08-25T00:00:00Z",
		},
	}
	classCopy := className
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: sandboxPVCName(pool), Namespace: pool.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName: &classCopy,
			VolumeName:       "pool-pv",
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-pv"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: className, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			AccessModes:            []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Capacity:               corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: provisioner, VolumeHandle: "pool-volume"}},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	return []client.Object{class, contract, attestation, pvc, pv}
}

func storageTestReconciler(t *testing.T, objects ...client.Object) *AgentPoolReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentPool{}).WithObjects(objects...).Build()
	return &AgentPoolReconciler{Client: c, Scheme: scheme, APIReader: c}
}

func TestAssessExternalRWXStorageRequiresLiveClassBoundConformance(t *testing.T) {
	pool := validAgentPool("external-pool", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.Sandbox.StorageClassName = "customer-rwx"
	objects := storageTestObjects(pool, storageModeExternal, "customer-rwx", "customer.csi.example")
	r := storageTestReconciler(t, objects...)

	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.Ready)
	assert.True(t, assessment.CanMount)
	assert.Equal(t, storageReasonReady, assessment.Reason)
	assert.Equal(t, "pool-pv", assessment.PVName)
}

func TestAssessRWXStorageFailsClosedWithoutConformance(t *testing.T) {
	pool := validAgentPool("pending-pool", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.Sandbox.StorageClassName = "customer-rwx"
	objects := storageTestObjects(pool, storageModeExternal, "customer-rwx", "customer.csi.example")
	objects = append(objects[:2], objects[3:]...) // remove the attestation
	r := storageTestReconciler(t, objects...)

	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.False(t, assessment.Ready)
	assert.False(t, assessment.CanMount)
	assert.Equal(t, storageReasonConformancePending, assessment.Reason)
	assert.Contains(t, assessment.Message, "live conformance attestation")
}

func TestAssessRWXStorageRejectsStaleClassUIDAttestation(t *testing.T) {
	pool := validAgentPool("stale-pool", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.Sandbox.StorageClassName = "customer-rwx"
	objects := storageTestObjects(pool, storageModeExternal, "customer-rwx", "customer.csi.example")
	objects[2].(*corev1.ConfigMap).Data["storageClassUID"] = "recreated-class"
	r := storageTestReconciler(t, objects...)

	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.Equal(t, storageReasonConformanceFailed, assessment.Reason)
	assert.Contains(t, assessment.Message, "stale/mismatched")
}

func TestAssessRWXStorageRejectsCapacityBelowClaimRequest(t *testing.T) {
	pool := validAgentPool("full-pool", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.Sandbox.StorageClassName = "customer-rwx"
	objects := storageTestObjects(pool, storageModeExternal, "customer-rwx", "customer.csi.example")
	objects[4].(*corev1.PersistentVolume).Spec.Capacity[corev1.ResourceStorage] = resource.MustParse("1Gi")
	r := storageTestReconciler(t, objects...)

	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.False(t, assessment.Ready)
	assert.Equal(t, storageReasonCapacity, assessment.Reason)
	assert.Contains(t, assessment.Message, "physical headroom")
}

func TestAssessManagedRWXStorageRejectsDegradedBackend(t *testing.T) {
	pool := validAgentPool("managed-pool", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.Sandbox.StorageClassName = managedLonghornStorageClass
	objects := storageTestObjects(pool, storageModeManagedLonghorn, managedLonghornStorageClass, managedLonghornProvisioner)
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"name": "pool-volume", "namespace": managedLonghornNamespace},
		"status":   map[string]any{"robustness": "degraded", "state": "attached"},
	}}
	objects = append(objects, volume)
	r := storageTestReconciler(t, objects...)

	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.False(t, assessment.Ready)
	assert.False(t, assessment.CanMount)
	assert.Equal(t, storageReasonBackendDegraded, assessment.Reason)
	assert.Contains(t, assessment.Message, "replica/node/disk capacity")
}

func TestManagedRWXStorageRequiresReadyShareManagerWhenAttached(t *testing.T) {
	pool := validAgentPool("managed-pool", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.Sandbox.StorageClassName = managedLonghornStorageClass
	objects := storageTestObjects(pool, storageModeManagedLonghorn, managedLonghornStorageClass, managedLonghornProvisioner)
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"name": "pool-volume", "namespace": managedLonghornNamespace},
		"status":   map[string]any{"robustness": "healthy", "state": "attached"},
	}}
	objects = append(objects, volume)
	r := storageTestReconciler(t, objects...)

	assessment := r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.Ready, "initial attachment may proceed while the share-manager becomes Ready")
	failure := r.managedLonghornVolumeHealth(context.Background(), "pool-volume", true)
	require.NotNil(t, failure)
	assert.Equal(t, storageReasonShareManagerDown, failure.Reason)

	shareManager := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "share-manager-pool-volume", Namespace: managedLonghornNamespace, Labels: map[string]string{longhornShareManagerComponentKey: longhornShareManagerComponent}},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	require.NoError(t, r.Create(context.Background(), shareManager))
	assessment = r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.Ready)
	assert.Nil(t, r.managedLonghornVolumeHealth(context.Background(), "pool-volume", true))
}

func TestEnsurePVCRefusesShrinkWithoutRecreation(t *testing.T) {
	pool := validAgentPool("shrink", "platform")
	pool.UID = types.UID("pool-uid")
	pool.Spec.Sandbox.AccessMode = corev1.ReadWriteOnce
	pool.Spec.Sandbox.Size = resource.MustParse("5Gi")
	class := pool.Spec.Sandbox.StorageClassName
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: sandboxPVCName(pool), Namespace: pool.Namespace, UID: types.UID("pvc-uid"),
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
	}
	r := storageTestReconciler(t, pool, pvc)
	err := r.ensurePVC(context.Background(), pool)
	require.ErrorContains(t, err, "shrink")
	var preserved corev1.PersistentVolumeClaim
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, &preserved))
	assert.Equal(t, types.UID("pvc-uid"), preserved.UID)
	preservedRequest := preserved.Spec.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, "10Gi", preservedRequest.String())
}

func TestQuiesceWorkersDeletesSchedulingCreditAfterStorageLoss(t *testing.T) {
	pool := validAgentPool("lost-storage", "platform")
	pods := []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: workerName(pool, 0), Namespace: pool.Namespace, Labels: poolLabels(pool)}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: workerName(pool, 1), Namespace: pool.Namespace, Labels: poolLabels(pool)}},
	}
	r := storageTestReconciler(t, pods...)
	require.NoError(t, r.quiesceWorkers(context.Background(), pool))
	var remaining corev1.PodList
	require.NoError(t, r.List(context.Background(), &remaining, client.InNamespace(pool.Namespace)))
	assert.Empty(t, remaining.Items)
}

func TestWorkerStorageFailureSurfacesMountRootDiagnostics(t *testing.T) {
	pool := validAgentPool("unsafe-root", "platform")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: workerName(pool, 0), Namespace: pool.Namespace, Labels: poolLabels(pool)},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "supervisor", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1, Reason: "Error", Message: "mount root owned by 65534:65534",
			}},
		}}},
	}
	r := storageTestReconciler(t, pod)
	message := workerStorageFailure(context.Background(), r.Client, pool)
	assert.Contains(t, message, "root-squashed")
	assert.Contains(t, message, "unsafe-root-worker-0")
}

func TestStorageReadyConditionTransitionIsStable(t *testing.T) {
	pool := validAgentPool("condition", "platform")
	pool.Generation = 4
	assessment := &agentPoolStorageAssessment{Ready: false, Reason: storageReasonPVCUnavailable, Message: "PVC lost"}
	r := storageTestReconciler(t, pool)
	require.NoError(t, r.patchStatus(context.Background(), pool, false, 0, assessment.Message, false, assessment))
	var got v1alpha1.AgentPool
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}, &got))
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, "StorageReady", got.Status.Conditions[0].Type)
	assert.Equal(t, storageReasonPVCUnavailable, got.Status.Conditions[0].Reason)
	assert.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status)
	assert.WithinDuration(t, time.Now(), got.Status.Conditions[0].LastTransitionTime.Time, time.Minute)
}
