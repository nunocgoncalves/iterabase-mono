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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
)

func managedModelBackend() *v1alpha1.ModelBackend {
	return &v1alpha1.ModelBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "iterabase-system", UID: types.UID("modelbackend-uid")},
		Spec: v1alpha1.ModelBackendSpec{
			Kind: "vLLM", Model: "Qwen/Qwen3.5-0.8B",
			PersistentVolumes: []v1alpha1.ModelBackendPersistentVolumeSpec{
				{Name: "hf-cache", MountPath: "/data/hf-cache", StorageClassName: modelBackendStorageClass, Size: resource.MustParse("10Gi")},
				{Name: "lmcache", MountPath: "/cache", StorageClassName: modelBackendStorageClass, Size: resource.MustParse("2Gi")},
			},
		},
	}
}

func lvmModelBackendClass() *storagev1.StorageClass {
	reclaim := corev1.PersistentVolumeReclaimDelete
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	expand := true
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: modelBackendStorageClass, Annotations: map[string]string{
			"storageclass.kubernetes.io/is-default-class": "false",
		}},
		Provisioner: modelBackendStorageProvisioner, ReclaimPolicy: &reclaim,
		VolumeBindingMode: &binding, AllowVolumeExpansion: &expand,
		Parameters: map[string]string{
			"storage": "lvm", "vgpattern": "^iterabase-data$", "fsType": "xfs", "thinProvision": "no", "shared": "no",
		},
	}
}

func modelBackendStorageReconciler(t *testing.T, objects ...client.Object) *ModelBackendReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	return &ModelBackendReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Scheme: scheme,
	}
}

func TestValidateModelBackendPersistentVolumes(t *testing.T) {
	valid := managedModelBackend()
	valid.Spec.Volumes = []corev1.Volume{{
		Name:         "container-tmp",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/iterabase/container-tmp"}},
	}}
	valid.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "container-tmp", MountPath: "/runtime-tmp"}}
	require.NoError(t, validateModelBackendPersistentVolumes(valid), "unrelated disposable container-tmp hostPath remains supported")

	cases := []struct {
		name   string
		mutate func(*v1alpha1.ModelBackend)
		want   string
	}{
		{name: "duplicate name", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].Name = "hf-cache" }, want: "duplicated"},
		{name: "duplicate path", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].MountPath = "/data/hf-cache" }, want: "collides"},
		{name: "nested path collision", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].MountPath = "/data/hf-cache/l2" }, want: "collides"},
		{name: "relative path", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].MountPath = "cache" }, want: "canonical absolute"},
		{name: "reserved path", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].MountPath = "/dev/shm/cache" }, want: "/dev/shm"},
		{name: "reserved name", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].Name = "dshm" }, want: "reserved"},
		{name: "alternate class", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].StorageClassName = "local-path" }, want: "alternate"},
		{name: "zero size", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.PersistentVolumes[1].Size = resource.MustParse("0") }, want: "positive"},
		{name: "replicas", mutate: func(mb *v1alpha1.ModelBackend) { two := int32(2); mb.Spec.Replicas = &two }, want: "exactly one"},
		{name: "external", mutate: func(mb *v1alpha1.ModelBackend) { mb.Spec.Kind = "external" }, want: "only for kind vLLM"},
		{name: "raw mount collision", mutate: func(mb *v1alpha1.ModelBackend) {
			mb.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "raw", MountPath: "/cache"}}
		}, want: "collides"},
		{name: "raw mount collision with ephemeral fallback and no declarations", mutate: func(mb *v1alpha1.ModelBackend) {
			mb.Spec.PersistentVolumes = nil
			mb.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "raw", MountPath: defaultModelCachePath}}
		}, want: "ephemeral HF cache"},
		{name: "raw mount collision with ephemeral fallback and unrelated declaration", mutate: func(mb *v1alpha1.ModelBackend) {
			mb.Spec.PersistentVolumes = mb.Spec.PersistentVolumes[1:]
			mb.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "raw", MountPath: defaultModelCachePath}}
		}, want: "ephemeral HF cache"},
		{name: "managed nested path collision with ephemeral fallback", mutate: func(mb *v1alpha1.ModelBackend) {
			mb.Spec.PersistentVolumes = mb.Spec.PersistentVolumes[:1]
			mb.Spec.PersistentVolumes[0].MountPath = defaultModelCachePath + "/nested"
		}, want: "ephemeral HF cache"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mb := managedModelBackend().DeepCopy()
			tc.mutate(mb)
			require.ErrorContains(t, validateModelBackendPersistentVolumes(mb), tc.want)
		})
	}
}

func TestValidateModelBackendStorageClassRequiresExactExpandableGeneralClass(t *testing.T) {
	assert.Empty(t, validateModelBackendStorageClass(lvmModelBackendClass()))
	for _, tc := range []struct {
		name   string
		mutate func(*storagev1.StorageClass)
	}{
		{name: "non-expandable", mutate: func(class *storagev1.StorageClass) { no := false; class.AllowVolumeExpansion = &no }},
		{name: "default", mutate: func(class *storagev1.StorageClass) {
			class.Annotations["storageclass.kubernetes.io/is-default-class"] = "true"
		}},
		{name: "shared", mutate: func(class *storagev1.StorageClass) { class.Parameters["shared"] = "yes" }},
		{name: "thin", mutate: func(class *storagev1.StorageClass) { class.Parameters["thinProvision"] = "yes" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			class := lvmModelBackendClass()
			tc.mutate(class)
			assert.NotEmpty(t, validateModelBackendStorageClass(class))
		})
	}
}

func TestModelBackendPodStorageUsesPVCsAndEphemeralHFFallback(t *testing.T) {
	plain := &v1alpha1.ModelBackend{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "ns"}}
	volumes, mounts := modelBackendPodStorage(plain)
	require.Len(t, volumes, 1)
	require.NotNil(t, volumes[0].EmptyDir)
	assert.Nil(t, volumes[0].HostPath)
	assert.Equal(t, hfCacheVolumeName, volumes[0].Name)
	require.Len(t, mounts, 1)
	assert.Equal(t, defaultModelCachePath, mounts[0].MountPath)

	managed := managedModelBackend()
	volumes, mounts = modelBackendPodStorage(managed)
	require.Len(t, volumes, 2)
	require.Len(t, mounts, 2)
	for _, volume := range volumes {
		assert.Nil(t, volume.HostPath)
		require.NotNil(t, volume.PersistentVolumeClaim)
	}
	assert.Equal(t, "/data/hf-cache", mounts[0].MountPath)
	assert.Equal(t, "/cache", mounts[1].MountPath)
	assert.NotEqual(t, hfCacheVolumeName, mounts[0].Name, "a declared HF path must replace the ephemeral fallback")

	before := buildDeploymentSpec(managed, defaultServingPort)
	grown := managed.DeepCopy()
	grown.Spec.PersistentVolumes[0].Size = resource.MustParse("20Gi")
	after := buildDeploymentSpec(grown, defaultServingPort)
	assert.Equal(t, before, after, "a size-only change must preserve the serving pod template for online XFS growth")
}

func TestReconcileModelBackendStorageFailsClosedWhenClassIsMissing(t *testing.T) {
	mb := managedModelBackend()
	assessment, err := modelBackendStorageReconciler(t).reconcileModelBackendStorage(context.Background(), mb)
	require.NoError(t, err)
	assert.False(t, assessment.Ready)
	assert.False(t, assessment.CanSchedule)
	assert.Contains(t, assessment.Message, "is missing")
}

func TestReconcileModelBackendStorageCreatesDeterministicOwnedWFFCClaims(t *testing.T) {
	mb := managedModelBackend()
	r := modelBackendStorageReconciler(t, lvmModelBackendClass())

	assessment, err := r.reconcileModelBackendStorage(context.Background(), mb)
	require.NoError(t, err)
	assert.False(t, assessment.Ready)
	assert.True(t, assessment.CanSchedule, "WFFC claims must not deadlock creation of the first serving pod")

	var claims corev1.PersistentVolumeClaimList
	require.NoError(t, r.List(context.Background(), &claims, client.InNamespace(mb.Namespace)))
	require.Len(t, claims.Items, 2)
	seen := map[string]bool{}
	for i := range claims.Items {
		claim := &claims.Items[i]
		seen[claim.Name] = true
		assert.True(t, modelBackendControllerOwner(claim, mb))
		assert.Equal(t, modelBackendStorageClass, *claim.Spec.StorageClassName)
		assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, claim.Spec.AccessModes)
		require.NotNil(t, claim.Spec.VolumeMode)
		assert.Equal(t, corev1.PersistentVolumeFilesystem, *claim.Spec.VolumeMode)
		assert.NotEmpty(t, claim.Annotations[modelBackendClaimSetAnnotation])
	}
	assert.True(t, seen[modelBackendPVCName(mb, "hf-cache")])
	assert.True(t, seen[modelBackendPVCName(mb, "lmcache")])
}

func TestReconcileModelBackendStorageNeverAdoptsOrReplacesAndOnlyGrows(t *testing.T) {
	ctx := context.Background()
	mb := managedModelBackend()
	hf := mb.Spec.PersistentVolumes[0]
	claimSet, err := modelBackendClaimSetDigest(mb.Spec.PersistentVolumes)
	require.NoError(t, err)
	filesystem := corev1.PersistentVolumeFilesystem
	foreign := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: modelBackendPVCName(mb, hf.Name), Namespace: mb.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &hf.StorageClassName, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &filesystem,
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: hf.Size}},
		},
	}
	r := modelBackendStorageReconciler(t, lvmModelBackendClass(), foreign)
	assessment, err := r.reconcileModelBackendStorage(ctx, mb)
	require.NoError(t, err)
	assert.False(t, assessment.CanSchedule)
	assert.Contains(t, assessment.Message, "refusing to adopt")
	var untouched corev1.PersistentVolumeClaim
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(foreign), &untouched))
	assert.Nil(t, metav1.GetControllerOf(&untouched))

	controller := true
	owned := foreign.DeepCopy()
	owned.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: v1alpha1.GroupVersion.String(), Kind: "ModelBackend", Name: mb.Name, UID: mb.UID, Controller: &controller,
	}}
	owned.Annotations = map[string]string{
		modelBackendClaimSetAnnotation: claimSet, modelBackendDeclarationAnnotation: hf.Name, modelBackendMountPathAnnotation: hf.MountPath,
	}
	owned.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("5Gi")
	owned.Spec.VolumeName = "existing-pv"
	owned.Status.Phase = corev1.ClaimBound
	r = modelBackendStorageReconciler(t, lvmModelBackendClass(), owned)
	assessment, err = r.reconcileModelBackendClaim(ctx, mb, hf, claimSet)
	require.NoError(t, err)
	assert.False(t, assessment.Ready)
	assert.True(t, assessment.CanSchedule)
	var grown corev1.PersistentVolumeClaim
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(owned), &grown))
	grownRequest := grown.Spec.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, "10Gi", grownRequest.String())
	assert.Equal(t, owned.UID, grown.UID, "growth must patch the same claim")

	shrink := hf
	shrink.Size = resource.MustParse("4Gi")
	assessment, err = r.reconcileModelBackendClaim(ctx, mb, shrink, claimSet)
	require.NoError(t, err)
	assert.False(t, assessment.CanSchedule)
	assert.Contains(t, assessment.Message, "shrink is unsupported")
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(owned), &grown))
	grownRequest = grown.Spec.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, "10Gi", grownRequest.String())
}

func TestReconcileModelBackendStorageDefersGrowthUntilWFFCBinding(t *testing.T) {
	ctx := context.Background()
	mb := managedModelBackend()
	mb.Spec.PersistentVolumes = mb.Spec.PersistentVolumes[:1]
	declaration := mb.Spec.PersistentVolumes[0]
	claimSet, err := modelBackendClaimSetDigest(mb.Spec.PersistentVolumes)
	require.NoError(t, err)
	controller := true
	filesystem := corev1.PersistentVolumeFilesystem
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: modelBackendPVCName(mb, declaration.Name), Namespace: mb.Namespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ModelBackend", Name: mb.Name, UID: mb.UID, Controller: &controller}},
			Annotations:     map[string]string{modelBackendClaimSetAnnotation: claimSet, modelBackendDeclarationAnnotation: declaration.Name, modelBackendMountPathAnnotation: declaration.MountPath},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &declaration.StorageClassName, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &filesystem,
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	r := modelBackendStorageReconciler(t, lvmModelBackendClass(), pvc)
	assessment, err := r.reconcileModelBackendClaim(ctx, mb, declaration, claimSet)
	require.NoError(t, err)
	assert.True(t, assessment.CanSchedule)
	assert.Contains(t, assessment.Message, "WaitForFirstConsumer")
	var unchanged corev1.PersistentVolumeClaim
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(pvc), &unchanged))
	request := unchanged.Spec.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, "5Gi", request.String(), "an unbound WFFC claim must bind before in-place growth")
}

func TestReconcileModelBackendStorageRejectsClaimSetMutation(t *testing.T) {
	ctx := context.Background()
	mb := managedModelBackend()
	r := modelBackendStorageReconciler(t, lvmModelBackendClass())
	_, err := r.reconcileModelBackendStorage(ctx, mb)
	require.NoError(t, err)

	changed := mb.DeepCopy()
	changed.Spec.PersistentVolumes[1].MountPath = "/different-cache"
	assessment, err := r.reconcileModelBackendStorage(ctx, changed)
	require.NoError(t, err)
	assert.False(t, assessment.CanSchedule)
	assert.Contains(t, assessment.Message, "immutable")

	removed := mb.DeepCopy()
	removed.Spec.PersistentVolumes = nil
	assessment, err = r.reconcileModelBackendStorage(ctx, removed)
	require.NoError(t, err)
	assert.False(t, assessment.CanSchedule)
	assert.Contains(t, assessment.Message, "cannot be removed")
}

func TestReconcileModelBackendStorageAcceptsConvergedOpenEBSClaims(t *testing.T) {
	ctx := context.Background()
	mb := managedModelBackend()
	mb.Spec.PersistentVolumes = mb.Spec.PersistentVolumes[:1]
	declaration := mb.Spec.PersistentVolumes[0]
	claimSet, err := modelBackendClaimSetDigest(mb.Spec.PersistentVolumes)
	require.NoError(t, err)
	controller := true
	filesystem := corev1.PersistentVolumeFilesystem
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: modelBackendPVCName(mb, declaration.Name), Namespace: mb.Namespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ModelBackend", Name: mb.Name, UID: mb.UID, Controller: &controller}},
			Annotations:     map[string]string{modelBackendClaimSetAnnotation: claimSet, modelBackendDeclarationAnnotation: declaration.Name, modelBackendMountPathAnnotation: declaration.MountPath},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &declaration.StorageClassName, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &filesystem, VolumeName: "pv-hf",
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: declaration.Size}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: declaration.Size}},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-hf"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: modelBackendStorageClass, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &filesystem,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: declaration.Size},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: modelBackendStorageProvisioner, FSType: "xfs", VolumeHandle: "volume-hf",
				VolumeAttributes: map[string]string{"openebs.io/volgroup": modelBackendStorageVolumeGroup},
			}},
			NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "openebs.io/nodename", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1"}}}}}}},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	volume := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "local.openebs.io/v1alpha1", "kind": "LVMVolume",
		"metadata": map[string]any{"name": "volume-hf", "namespace": mb.Namespace},
		"spec":     map[string]any{"capacity": "10Gi", "ownerNodeID": "node-1", "shared": "no", "thinProvision": "no", "vgPattern": "^iterabase-data$", "volGroup": modelBackendStorageVolumeGroup},
		"status":   map[string]any{"state": "Ready"},
	}}
	volume.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMVolume"})
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "local.openebs.io/v1alpha1", "kind": "LVMNode",
		"metadata":     map[string]any{"name": "node-1", "namespace": mb.Namespace},
		"volumeGroups": []any{map[string]any{"name": modelBackendStorageVolumeGroup, "uuid": "vg-uuid", "missingPvCount": int64(0), "thinPools": []any{}}},
	}}
	node.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMNode"})

	r := modelBackendStorageReconciler(t, lvmModelBackendClass(), pvc, pv, volume, node)
	assessment, err := r.reconcileModelBackendStorage(ctx, mb)
	require.NoError(t, err)
	assert.True(t, assessment.Ready, "%+v", assessment)
	assert.True(t, assessment.CanSchedule)
}
