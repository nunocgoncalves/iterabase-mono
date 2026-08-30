package controller

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
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

type workerDeleteFailureClient struct {
	client.Client
}

func (c *workerDeleteFailureClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*corev1.Pod); ok {
		return stderrors.New("simulated worker delete failure")
	}
	return c.Client.Delete(ctx, obj, opts...)
}

type asynchronousPodDeleteClient struct {
	client.Client
	terminating map[client.ObjectKey]metav1.Time
}

func (c *asynchronousPodDeleteClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*corev1.Pod); !ok {
		return c.Client.Delete(ctx, obj, opts...)
	}
	var pod corev1.Pod
	key := client.ObjectKeyFromObject(obj)
	if err := c.Client.Get(ctx, key, &pod); err != nil {
		return err
	}
	if _, ok := c.terminating[key]; !ok {
		c.terminating[key] = metav1.Now()
	}
	return nil
}

func (c *asynchronousPodDeleteClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if pod, ok := obj.(*corev1.Pod); ok {
		if timestamp, terminating := c.terminating[key]; terminating {
			pod.DeletionTimestamp = &timestamp
		}
	}
	return nil
}

func (c *asynchronousPodDeleteClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.Client.List(ctx, list, opts...); err != nil {
		return err
	}
	if pods, ok := list.(*corev1.PodList); ok {
		for i := range pods.Items {
			key := client.ObjectKeyFromObject(&pods.Items[i])
			if timestamp, terminating := c.terminating[key]; terminating {
				pods.Items[i].DeletionTimestamp = &timestamp
			}
		}
	}
	return nil
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

func TestAssessManagedRWXUnknownTransitionIsInitialOnlyBeforeOperationalReadiness(t *testing.T) {
	for _, state := range []string{"detached", "attached"} {
		t.Run(state, func(t *testing.T) {
			pool := validAgentPool("managed-pool", "platform")
			pool.Spec.CredentialBindings = nil
			pool.Spec.Sandbox.StorageClassName = managedLonghornStorageClass
			objects := storageTestObjects(pool, storageModeManagedLonghorn, managedLonghornStorageClass, managedLonghornProvisioner)
			objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
				"metadata": map[string]any{"name": "pool-volume", "namespace": managedLonghornNamespace},
				"status":   map[string]any{"robustness": "unknown", "state": state},
			}})
			r := storageTestReconciler(t, objects...)

			assessment := r.assessAgentPoolStorage(context.Background(), pool)
			assert.False(t, assessment.Ready)
			assert.True(t, assessment.CanMount)
			assert.Equal(t, storageReasonInitialConvergence, assessment.Reason)
			assert.Contains(t, assessment.Message, "retain the desired workers")

			pool.Status.Conditions = []metav1.Condition{{
				Type: storageConditionOperationalReadinessReached, Status: metav1.ConditionTrue,
				Reason: storageReasonOperationalReadinessReached,
			}}
			assessment = r.assessAgentPoolStorage(context.Background(), pool)
			assert.False(t, assessment.Ready)
			assert.False(t, assessment.CanMount)
			assert.Equal(t, storageReasonBackendDegraded, assessment.Reason)

			pool.Status.Conditions = append(pool.Status.Conditions, metav1.Condition{
				Type: storageConditionWorkerReplacementPending, Status: metav1.ConditionTrue,
				Reason: storageReasonRecoveryPending,
			})
			assessment = r.assessAgentPoolStorage(context.Background(), pool)
			assert.False(t, assessment.Ready)
			assert.False(t, assessment.CanMount)
			assert.Equal(t, storageReasonBackendDegraded, assessment.Reason)
		})
	}
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
	failure := r.managedLonghornVolumeHealth(context.Background(), pool, "pool-volume", true)
	require.NotNil(t, failure)
	assert.Equal(t, storageReasonShareManagerDown, failure.Reason)

	shareManager := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "share-manager-pool-volume", Namespace: managedLonghornNamespace, Labels: map[string]string{longhornShareManagerComponentKey: longhornShareManagerComponent}},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	require.NoError(t, r.Create(context.Background(), shareManager))
	assessment = r.assessAgentPoolStorage(context.Background(), pool)
	assert.True(t, assessment.Ready)
	assert.Nil(t, r.managedLonghornVolumeHealth(context.Background(), pool, "pool-volume", true))
}

func TestStorageWasOperationallyReadyUsesDurableMarkerAndLegacyAggregate(t *testing.T) {
	storageReady := metav1.Condition{Type: storageConditionReady, Status: metav1.ConditionTrue, Reason: storageReasonReady}
	operationalReadinessReached := metav1.Condition{
		Type: storageConditionOperationalReadinessReached, Status: metav1.ConditionTrue,
		Reason: storageReasonOperationalReadinessReached,
	}
	tests := []struct {
		name          string
		ready         bool
		readyReplicas int32
		conditions    []metav1.Condition
		want          bool
	}{
		{name: "storage predicates only", conditions: []metav1.Condition{storageReady}},
		{name: "scaled to zero", ready: true, conditions: []metav1.Condition{storageReady}},
		{name: "workers without storage", ready: true, readyReplicas: 2},
		{name: "legacy operational aggregate", ready: true, readyReplicas: 2, conditions: []metav1.Condition{storageReady}, want: true},
		{name: "durable marker after transient status", conditions: []metav1.Condition{storageReady, operationalReadinessReached}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := validAgentPool("managed-pool", "platform")
			pool.Status.Ready = tt.ready
			pool.Status.ReadyReplicas = tt.readyReplicas
			pool.Status.Conditions = tt.conditions
			assert.Equal(t, tt.want, storageWasOperationallyReady(pool))
		})
	}
}

func TestReconcileManagedRWXInitialUnknownDetachedToAttachedConvergesWithoutWorkerChurn(t *testing.T) {
	pool := validAgentPool("managed-pool", "platform")
	pool.UID = types.UID("pool-uid")
	pool.Finalizers = []string{agentPoolFinalizer}
	pool.Generation = 1
	pool.Spec.CredentialBindings = nil
	pool.Spec.GatewayGrants = nil
	pool.Spec.Sandbox.StorageClassName = managedLonghornStorageClass

	objects := storageTestObjects(pool, storageModeManagedLonghorn, managedLonghornStorageClass, managedLonghornProvisioner)
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"name": "pool-volume", "namespace": managedLonghornNamespace},
		"status":   map[string]any{"robustness": "unknown", "state": "detached"},
	}}
	workers := []*corev1.Pod{
		buildWorkerPod(pool, workerName(pool, 0), workerPodTemplateHash(pool, workerName(pool, 0))),
		buildWorkerPod(pool, workerName(pool, 1), workerPodTemplateHash(pool, workerName(pool, 1))),
	}
	ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pool.Spec.Identity.CASecretRef.Name, Namespace: pool.Namespace}}
	objects = append(objects, pool, volume, workers[0], workers[1], ca)
	r := storageTestReconciler(t, objects...)
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}}

	for range 2 {
		result, err := r.Reconcile(context.Background(), request)
		require.NoError(t, err)
		assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
		for _, worker := range workers {
			var retained corev1.Pod
			require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &retained), "initial workers must remain across repeated unknown/detached reconciles")
		}
	}

	var got v1alpha1.AgentPool
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready)
	assert.Zero(t, got.Status.ReadyReplicas)
	condition := meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonInitialConvergence, condition.Reason)
	assert.Contains(t, got.Status.Message, "initial convergence")
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, storageConditionOperationalReadinessReached))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending))

	require.NoError(t, unstructured.SetNestedField(volume.Object, "attached", "status", "state"))
	require.NoError(t, r.Update(context.Background(), volume))
	for range 2 {
		result, err := r.Reconcile(context.Background(), request)
		require.NoError(t, err)
		assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
		for _, worker := range workers {
			var retained corev1.Pod
			require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &retained), "initial workers must remain while attached robustness is still unknown")
		}
	}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	condition = meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonInitialConvergence, condition.Reason)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, storageConditionOperationalReadinessReached))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending))

	require.NoError(t, unstructured.SetNestedField(volume.Object, "healthy", "status", "robustness"))
	require.NoError(t, unstructured.SetNestedField(volume.Object, "attached", "status", "state"))
	require.NoError(t, r.Update(context.Background(), volume))
	for _, worker := range workers {
		var readyWorker corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &readyWorker))
		readyWorker.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		require.NoError(t, r.Status().Update(context.Background(), &readyWorker))
	}

	result, err := r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready)
	assert.Equal(t, int32(2), got.Status.ReadyReplicas)
	condition = meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonShareManagerDown, condition.Reason)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, storageConditionOperationalReadinessReached))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending))
	for _, worker := range workers {
		var retained corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &retained), "initial workers must remain while the first share-manager becomes Ready")
	}

	require.NoError(t, r.Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "share-manager-pool-volume",
			Namespace: managedLonghornNamespace,
			Labels:    map[string]string{longhornShareManagerComponentKey: longhornShareManagerComponent},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}))
	result, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.True(t, got.Status.Ready)
	assert.Equal(t, int32(2), got.Status.ReadyReplicas)
	condition = meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.Equal(t, storageReasonReady, condition.Reason)
	operationalCondition := meta.FindStatusCondition(got.Status.Conditions, storageConditionOperationalReadinessReached)
	require.NotNil(t, operationalCondition)
	assert.Equal(t, metav1.ConditionTrue, operationalCondition.Status)
}

func TestReconcileManagedRWXEstablishedUnknownDetachedQuiescesAndRecoversWithFreshWorkers(t *testing.T) {
	pool := validAgentPool("managed-pool", "platform")
	pool.UID = types.UID("pool-uid")
	pool.Finalizers = []string{agentPoolFinalizer}
	pool.Generation = 1
	pool.Spec.CredentialBindings = nil
	pool.Spec.GatewayGrants = nil
	pool.Spec.Sandbox.StorageClassName = managedLonghornStorageClass
	pool.Status.Ready = true
	pool.Status.ReadyReplicas = 2
	pool.Status.Conditions = []metav1.Condition{
		{
			Type: "StorageReady", Status: metav1.ConditionTrue, Reason: storageReasonReady,
			ObservedGeneration: 1,
		},
		{
			Type: storageConditionOperationalReadinessReached, Status: metav1.ConditionTrue,
			Reason: storageReasonOperationalReadinessReached, ObservedGeneration: 1,
		},
	}

	objects := storageTestObjects(pool, storageModeManagedLonghorn, managedLonghornStorageClass, managedLonghornProvisioner)
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"name": "pool-volume", "namespace": managedLonghornNamespace},
		"status":   map[string]any{"robustness": "unknown", "state": "detached"},
	}}
	workers := []*corev1.Pod{
		buildWorkerPod(pool, workerName(pool, 0), workerPodTemplateHash(pool, workerName(pool, 0))),
		buildWorkerPod(pool, workerName(pool, 1), workerPodTemplateHash(pool, workerName(pool, 1))),
	}
	for _, worker := range workers {
		worker.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pool.Spec.Identity.CASecretRef.Name, Namespace: pool.Namespace}}
	objects = append(objects, pool, volume, workers[0], workers[1], ca)
	r := storageTestReconciler(t, objects...)
	baseClient := r.Client
	asyncClient := &asynchronousPodDeleteClient{Client: baseClient, terminating: make(map[client.ObjectKey]metav1.Time)}
	r.Client = asyncClient
	r.APIReader = r.Client

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.NoError(t, err)
	for _, worker := range workers {
		var terminating corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &terminating))
		assert.False(t, terminating.DeletionTimestamp.IsZero(), "affected workers must enter asynchronous termination after established unknown/detached storage")
		assert.False(t, podIsReady(&terminating), "terminating Ready=True workers must not retain scheduling credit")
	}
	var got v1alpha1.AgentPool
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready)
	assert.Zero(t, got.Status.ReadyReplicas)
	condition := meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonBackendDegraded, condition.Reason)
	operationalCondition := meta.FindStatusCondition(got.Status.Conditions, storageConditionOperationalReadinessReached)
	require.NotNil(t, operationalCondition)
	assert.Equal(t, metav1.ConditionTrue, operationalCondition.Status)
	replacementCondition := meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)

	shareManager := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "share-manager-pool-volume",
			Namespace: managedLonghornNamespace,
			Labels:    map[string]string{longhornShareManagerComponentKey: longhornShareManagerComponent},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	require.NoError(t, r.Create(context.Background(), shareManager))
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready, "a share-manager alone must not reopen the established pool while affected workers terminate")
	assert.Zero(t, got.Status.ReadyReplicas)
	replacementCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)
	for _, worker := range workers {
		var terminating corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &terminating))
		require.Len(t, terminating.Status.Conditions, 1)
		assert.Equal(t, corev1.ConditionTrue, terminating.Status.Conditions[0].Status, "asynchronous deletion may preserve the old Ready condition")
		require.NoError(t, baseClient.Delete(context.Background(), &terminating))
		delete(asyncClient.terminating, client.ObjectKeyFromObject(worker))
		var removed corev1.Pod
		assert.True(t, apierrors.IsNotFound(r.Get(context.Background(), client.ObjectKeyFromObject(worker), &removed)))
	}
	var recoveredShareManager corev1.Pod
	require.NoError(t, baseClient.Get(context.Background(), client.ObjectKeyFromObject(shareManager), &recoveredShareManager))
	require.NoError(t, baseClient.Delete(context.Background(), &recoveredShareManager))

	detachedVolume := &unstructured.Unstructured{}
	detachedVolume.SetAPIVersion("longhorn.io/v1beta2")
	detachedVolume.SetKind("Volume")
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(volume), detachedVolume))
	require.NoError(t, unstructured.SetNestedField(detachedVolume.Object, "detached", "status", "state"))
	require.NoError(t, r.Update(context.Background(), detachedVolume))

	for range 2 {
		result, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
		require.NoError(t, err)
		assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
		for _, worker := range workers {
			var absent corev1.Pod
			assert.True(t, apierrors.IsNotFound(r.Get(context.Background(), client.ObjectKeyFromObject(worker), &absent)), "established unknown/detached storage must not admit replacement workers")
		}
	}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready)
	assert.Zero(t, got.Status.ReadyReplicas)
	condition = meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonBackendDegraded, condition.Reason)
	replacementCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)

	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(volume), detachedVolume))
	require.NoError(t, unstructured.SetNestedField(detachedVolume.Object, "healthy", "status", "robustness"))
	require.NoError(t, r.Update(context.Background(), detachedVolume))
	result, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	for _, worker := range workers {
		var absent corev1.Pod
		assert.True(t, apierrors.IsNotFound(r.Get(context.Background(), client.ObjectKeyFromObject(worker), &absent)), "healthy but detached storage must recover attachment before replacement workers are created")
	}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	condition = meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonRecoveryPending, condition.Reason)

	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(volume), detachedVolume))
	require.NoError(t, unstructured.SetNestedField(detachedVolume.Object, "attached", "status", "state"))
	require.NoError(t, r.Update(context.Background(), detachedVolume))
	require.NoError(t, r.Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "share-manager-pool-volume",
			Namespace: managedLonghornNamespace,
			Labels:    map[string]string{longhornShareManagerComponentKey: longhornShareManagerComponent},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}))
	result, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	for _, worker := range workers {
		var absent corev1.Pod
		assert.True(t, apierrors.IsNotFound(r.Get(context.Background(), client.ObjectKeyFromObject(worker), &absent)), "affected worker quiescing must complete as a distinct phase before replacement")
	}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	replacementCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)
	assert.Equal(t, storageReasonAffectedWorkersQuiesced, replacementCondition.Reason)

	result, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	for _, worker := range workers {
		var fresh corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &fresh), "fresh workers may be created only after attached backend and share-manager health")
	}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready)
	assert.Zero(t, got.Status.ReadyReplicas)
	replacementCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)
	assert.Equal(t, storageReasonAffectedWorkersQuiesced, replacementCondition.Reason)

	for _, worker := range workers {
		var fresh corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &fresh))
		fresh.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		require.NoError(t, r.Status().Update(context.Background(), &fresh))
	}

	result, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.True(t, got.Status.Ready)
	assert.Equal(t, int32(2), got.Status.ReadyReplicas)
	replacementCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionFalse, replacementCondition.Status)
	assert.Equal(t, storageReasonFreshWorkersReady, replacementCondition.Reason)
}

func TestReconcileManagedRWXEstablishedPoolKeepsFailClosedAfterTransientStatus(t *testing.T) {
	pool := validAgentPool("managed-pool", "platform")
	pool.UID = types.UID("pool-uid")
	pool.Finalizers = []string{agentPoolFinalizer}
	pool.Generation = 1
	pool.Spec.CredentialBindings = nil
	pool.Spec.GatewayGrants = nil
	pool.Spec.Sandbox.StorageClassName = managedLonghornStorageClass
	pool.Status.Ready = true
	pool.Status.ReadyReplicas = 2
	pool.Status.ObservedGeneration = 1
	pool.Status.Conditions = []metav1.Condition{{
		Type: storageConditionReady, Status: metav1.ConditionTrue, Reason: storageReasonReady,
		ObservedGeneration: 1,
	}}

	objects := storageTestObjects(pool, storageModeManagedLonghorn, managedLonghornStorageClass, managedLonghornProvisioner)
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"name": "pool-volume", "namespace": managedLonghornNamespace},
		"status":   map[string]any{"robustness": "healthy", "state": "attached"},
	}}
	workers := []*corev1.Pod{
		buildWorkerPod(pool, workerName(pool, 0), workerPodTemplateHash(pool, workerName(pool, 0))),
		buildWorkerPod(pool, workerName(pool, 1), workerPodTemplateHash(pool, workerName(pool, 1))),
	}
	objects = append(objects, pool, volume, workers[0], workers[1])
	r := storageTestReconciler(t, objects...)
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}}

	result, err := r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	var transient v1alpha1.AgentPool
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &transient))
	assert.False(t, transient.Status.Ready)
	assert.Zero(t, transient.Status.ReadyReplicas)
	assert.Contains(t, transient.Status.Message, "dependency:")
	operationalCondition := meta.FindStatusCondition(transient.Status.Conditions, storageConditionOperationalReadinessReached)
	require.NotNil(t, operationalCondition)
	assert.Equal(t, metav1.ConditionTrue, operationalCondition.Status)
	assert.Equal(t, int64(1), operationalCondition.ObservedGeneration)

	require.NoError(t, r.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: pool.Spec.Identity.CASecretRef.Name, Namespace: pool.Namespace},
	}))
	result, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	for _, worker := range workers {
		var retained corev1.Pod
		require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(worker), &retained), "affected workers remain only while the share-manager recovers")
		assert.True(t, retained.DeletionTimestamp.IsZero())
	}
	var got v1alpha1.AgentPool
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	assert.False(t, got.Status.Ready)
	assert.Zero(t, got.Status.ReadyReplicas)
	condition := meta.FindStatusCondition(got.Status.Conditions, storageConditionReady)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonRecoveryPending, condition.Reason)
	operationalCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionOperationalReadinessReached)
	require.NotNil(t, operationalCondition)
	assert.Equal(t, metav1.ConditionTrue, operationalCondition.Status)
	replacementCondition := meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)
	assert.Equal(t, storageReasonRecoveryPending, replacementCondition.Reason)

	require.NoError(t, r.Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "share-manager-pool-volume",
			Namespace: managedLonghornNamespace,
			Labels:    map[string]string{longhornShareManagerComponentKey: longhornShareManagerComponent},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}))
	result, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, healthRequeueInterval, result.RequeueAfter)
	for _, worker := range workers {
		var removed corev1.Pod
		assert.True(t, apierrors.IsNotFound(r.Get(context.Background(), client.ObjectKeyFromObject(worker), &removed)), "backend recovery must precede affected worker quiescing")
	}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(pool), &got))
	replacementCondition = meta.FindStatusCondition(got.Status.Conditions, storageConditionWorkerReplacementPending)
	require.NotNil(t, replacementCondition)
	assert.Equal(t, metav1.ConditionTrue, replacementCondition.Status)
	assert.Equal(t, storageReasonAffectedWorkersQuiesced, replacementCondition.Reason)
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

func TestReconcileReadyPoolRejectsPVCMutationWithCurrentStorageCondition(t *testing.T) {
	tests := []struct {
		name       string
		mutatePool func(*v1alpha1.AgentPool)
		wantReason string
	}{
		{
			name: "shrink",
			mutatePool: func(pool *v1alpha1.AgentPool) {
				pool.Spec.Sandbox.Size = resource.MustParse("5Gi")
			},
			wantReason: storageReasonPVCExpansionFailed,
		},
		{
			name: "immutable class change",
			mutatePool: func(pool *v1alpha1.AgentPool) {
				pool.Spec.Sandbox.StorageClassName = "replacement-class"
			},
			wantReason: storageReasonClassMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := validAgentPool("mutation", "platform")
			pool.Spec.CredentialBindings = nil
			pool.Spec.GatewayGrants = nil
			pool.Spec.Sandbox.Size = resource.MustParse("10Gi")
			pool.Finalizers = []string{agentPoolFinalizer}
			pool.Generation = 2
			pool.Status.Ready = true
			pool.Status.ReadyReplicas = 1
			pool.Status.Conditions = []metav1.Condition{{
				Type: "StorageReady", Status: metav1.ConditionTrue, Reason: storageReasonReady,
				ObservedGeneration: 1,
			}}
			class := pool.Spec.Sandbox.StorageClassName
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: sandboxPVCName(pool), Namespace: pool.Namespace, UID: "pvc-uid",
					CreationTimestamp: metav1.NewTime(time.Now()),
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &class,
					AccessModes:      []corev1.PersistentVolumeAccessMode{pool.Spec.Sandbox.AccessMode},
					Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
				},
			}
			worker := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: workerName(pool, 0), Namespace: pool.Namespace, Labels: poolLabels(pool)},
				Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
					Type: corev1.PodReady, Status: corev1.ConditionTrue,
				}}},
			}
			ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pool.Spec.Identity.CASecretRef.Name, Namespace: pool.Namespace}}
			tt.mutatePool(pool)
			r := storageTestReconciler(t, pool, pvc, worker, ca)

			result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
			require.NoError(t, err)
			assert.False(t, result.Requeue)

			var got v1alpha1.AgentPool
			require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}, &got))
			assert.False(t, got.Status.Ready)
			assert.Zero(t, got.Status.ReadyReplicas)
			assert.Equal(t, got.Generation, got.Status.ObservedGeneration)
			condition := meta.FindStatusCondition(got.Status.Conditions, "StorageReady")
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, tt.wantReason, condition.Reason)
			assert.Contains(t, condition.Message, "scheduling credit was removed before worker quiescing")

			var removed corev1.Pod
			err = r.Get(context.Background(), types.NamespacedName{Name: worker.Name, Namespace: worker.Namespace}, &removed)
			assert.True(t, apierrors.IsNotFound(err), "ready worker must be quiesced after a rejected storage mutation")
		})
	}
}

func TestReconcileReadyPoolRecordsStorageMutationBeforeQuiesceFailure(t *testing.T) {
	pool := validAgentPool("quiesce-failure", "platform")
	pool.Spec.CredentialBindings = nil
	pool.Spec.GatewayGrants = nil
	pool.Spec.Sandbox.Size = resource.MustParse("5Gi")
	pool.Finalizers = []string{agentPoolFinalizer}
	pool.Generation = 2
	pool.Status.Ready = true
	pool.Status.ReadyReplicas = 1
	pool.Status.Conditions = []metav1.Condition{{
		Type: "StorageReady", Status: metav1.ConditionTrue, Reason: storageReasonReady,
		ObservedGeneration: 1,
	}}
	class := pool.Spec.Sandbox.StorageClassName
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: sandboxPVCName(pool), Namespace: pool.Namespace, UID: "pvc-uid",
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class,
			AccessModes:      []corev1.PersistentVolumeAccessMode{pool.Spec.Sandbox.AccessMode},
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
	}
	worker := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: workerName(pool, 0), Namespace: pool.Namespace, Labels: poolLabels(pool)},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionTrue,
		}}},
	}
	ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pool.Spec.Identity.CASecretRef.Name, Namespace: pool.Namespace}}
	r := storageTestReconciler(t, pool, pvc, worker, ca)
	r.Client = &workerDeleteFailureClient{Client: r.Client}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}})
	require.ErrorContains(t, err, "simulated worker delete failure")

	var got v1alpha1.AgentPool
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}, &got))
	assert.False(t, got.Status.Ready)
	assert.Zero(t, got.Status.ReadyReplicas)
	condition := meta.FindStatusCondition(got.Status.Conditions, "StorageReady")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, storageReasonPVCExpansionFailed, condition.Reason)
	assert.Contains(t, condition.Message, "scheduling credit was removed before worker quiescing")

	var retained corev1.Pod
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: worker.Name, Namespace: worker.Namespace}, &retained), "the simulated delete failure should retain the worker while status remains fail-closed")
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
