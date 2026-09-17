package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
)

const (
	modelBackendStorageClass            = "iterabase-lvm-xfs"
	modelBackendStorageProvisioner      = "local.csi.openebs.io"
	modelBackendStorageVolumeGroup      = "iterabase-data"
	modelBackendClaimSetAnnotation      = "platform.iterabase.com/modelbackend-claim-set"
	modelBackendDeclarationAnnotation   = "platform.iterabase.com/modelbackend-volume-name"
	modelBackendMountPathAnnotation     = "platform.iterabase.com/modelbackend-mount-path"
	modelBackendClaimLabel              = "platform.iterabase.com/modelbackend-storage"
	modelBackendManagedVolumeNamePrefix = "managed-"
)

type modelBackendStorageAssessment struct {
	Ready       bool
	CanSchedule bool
	Message     string
}

type modelBackendStorageIdentity struct {
	Name             string `json:"name"`
	MountPath        string `json:"mountPath"`
	StorageClassName string `json:"storageClassName"`
}

// validateModelBackendPersistentVolumes rejects declarations that Kubernetes
// cannot safely materialize without shadowing controller or explicit pod
// mounts. PVC state performs the durable post-provisioning immutability check.
//
//nolint:gocyclo // each declaration/name/path/class/replica predicate has a distinct refusal.
func validateModelBackendPersistentVolumes(mb *v1alpha1.ModelBackend) error {
	if len(mb.Spec.PersistentVolumes) == 0 {
		return nil
	}
	if mb.Spec.Kind != "vLLM" {
		return fmt.Errorf("spec.persistentVolumes is supported only for kind vLLM in this release")
	}
	replicas := int32(1)
	if mb.Spec.Replicas != nil {
		replicas = *mb.Spec.Replicas
	}
	if replicas != 1 {
		return fmt.Errorf("spec.persistentVolumes requires exactly one replica; managed serving claims do not support replica/HA shapes")
	}

	names := make(map[string]struct{}, len(mb.Spec.PersistentVolumes))
	paths := make(map[string]struct{}, len(mb.Spec.PersistentVolumes))
	podVolumeNames := make(map[string]struct{}, len(mb.Spec.PersistentVolumes))
	for i, volume := range mb.Spec.PersistentVolumes {
		if problems := validation.IsDNS1123Label(volume.Name); len(problems) > 0 {
			return fmt.Errorf("spec.persistentVolumes[%d].name %q must be a DNS label: %s", i, volume.Name, strings.Join(problems, ", "))
		}
		if volume.Name == devShmVolumeName {
			return fmt.Errorf("spec.persistentVolumes[%d].name %q is reserved for controller-managed /dev/shm", i, volume.Name)
		}
		if _, duplicate := names[volume.Name]; duplicate {
			return fmt.Errorf("spec.persistentVolumes[%d].name %q is duplicated", i, volume.Name)
		}
		names[volume.Name] = struct{}{}
		if !path.IsAbs(volume.MountPath) || path.Clean(volume.MountPath) != volume.MountPath || volume.MountPath == "/" {
			return fmt.Errorf("spec.persistentVolumes[%d].mountPath %q must be a canonical absolute non-root path", i, volume.MountPath)
		}
		if volume.MountPath == devShmMountPath || strings.HasPrefix(volume.MountPath, devShmMountPath+"/") {
			return fmt.Errorf("spec.persistentVolumes[%d].mountPath %q collides with controller-managed /dev/shm", i, volume.MountPath)
		}
		for existing := range paths {
			if modelBackendMountPathsCollide(existing, volume.MountPath) {
				return fmt.Errorf("spec.persistentVolumes[%d].mountPath %q collides with %q", i, volume.MountPath, existing)
			}
		}
		paths[volume.MountPath] = struct{}{}
		if volume.StorageClassName != modelBackendStorageClass {
			return fmt.Errorf("spec.persistentVolumes[%d].storageClassName must be %q; alternate, default, and BYO classes are unsupported", i, modelBackendStorageClass)
		}
		if volume.Size.Sign() <= 0 {
			return fmt.Errorf("spec.persistentVolumes[%d].size must be a positive Kubernetes quantity", i)
		}
		podVolumeNames[modelBackendManagedVolumeName(volume.Name)] = struct{}{}
	}
	for _, volume := range mb.Spec.Volumes {
		if _, collision := podVolumeNames[volume.Name]; collision {
			return fmt.Errorf("spec.volumes name %q collides with a controller-managed persistent volume", volume.Name)
		}
	}
	for _, mount := range mb.Spec.VolumeMounts {
		for managedPath := range paths {
			if modelBackendMountPathsCollide(managedPath, mount.MountPath) {
				return fmt.Errorf("spec.volumeMounts path %q collides with managed path %q", mount.MountPath, managedPath)
			}
		}
		if len(mb.Spec.PersistentVolumes) == 0 && mount.MountPath == defaultModelCachePath {
			return fmt.Errorf("spec.volumeMounts path %q collides with the controller-managed ephemeral HF cache", mount.MountPath)
		}
	}
	return nil
}

func modelBackendMountPathsCollide(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func modelBackendClaimSetDigest(volumes []v1alpha1.ModelBackendPersistentVolumeSpec) (string, error) {
	identities := make([]modelBackendStorageIdentity, 0, len(volumes))
	for _, volume := range volumes {
		identities = append(identities, modelBackendStorageIdentity{
			Name: volume.Name, MountPath: volume.MountPath, StorageClassName: volume.StorageClassName,
		})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].Name < identities[j].Name })
	encoded, err := json.Marshal(identities)
	if err != nil {
		return "", fmt.Errorf("encode ModelBackend claim identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func modelBackendPVCName(mb *v1alpha1.ModelBackend, declarationName string) string {
	return boundedDNSLabel(mb.Name+"-"+declarationName, 63)
}

func modelBackendManagedVolumeName(declarationName string) string {
	return boundedDNSLabel(modelBackendManagedVolumeNamePrefix+declarationName, 63)
}

func boundedDNSLabel(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(digest[:5])
	return strings.TrimRight(value[:limit-len(suffix)-1], "-") + "-" + suffix
}

func modelBackendControllerOwner(pvc *corev1.PersistentVolumeClaim, mb *v1alpha1.ModelBackend) bool {
	owner := metav1.GetControllerOf(pvc)
	return owner != nil && owner.APIVersion == v1alpha1.GroupVersion.String() && owner.Kind == "ModelBackend" &&
		owner.Name == mb.Name && owner.UID == mb.UID
}

// reconcileModelBackendStorage creates every declaration once and mutates only
// an existing request upward. Pending WFFC claims remain schedulable so the
// serving pod can trigger initial binding and mounted XFS growth.
//
//nolint:gocyclo // ordered class/set/claim convergence predicates intentionally fail closed.
func (r *ModelBackendReconciler) reconcileModelBackendStorage(ctx context.Context, mb *v1alpha1.ModelBackend) (modelBackendStorageAssessment, error) {
	assessment := modelBackendStorageAssessment{Ready: true, CanSchedule: true}
	if len(mb.Spec.PersistentVolumes) == 0 {
		var claims corev1.PersistentVolumeClaimList
		if err := r.List(ctx, &claims, client.InNamespace(mb.Namespace)); err != nil {
			return assessment, fmt.Errorf("list ModelBackend claims: %w", err)
		}
		for i := range claims.Items {
			claim := &claims.Items[i]
			if modelBackendControllerOwner(claim, mb) {
				assessment.Ready = false
				assessment.CanSchedule = false
				assessment.Message = fmt.Sprintf("managed claim set is immutable after provisioning; owned PVC %s/%s cannot be removed from spec.persistentVolumes", claim.Namespace, claim.Name)
				return assessment, nil
			}
		}
		return assessment, nil
	}

	var class storagev1.StorageClass
	if err := r.Get(ctx, types.NamespacedName{Name: modelBackendStorageClass}, &class); err != nil {
		assessment.Ready = false
		assessment.CanSchedule = false
		if errors.IsNotFound(err) {
			assessment.Message = fmt.Sprintf("managed StorageClass %q is missing; reapply the pinned LVM storage substrate", modelBackendStorageClass)
			return assessment, nil
		}
		return assessment, fmt.Errorf("read managed StorageClass %q: %w", modelBackendStorageClass, err)
	}
	if failure := validateModelBackendStorageClass(&class); failure != "" {
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = failure
		return assessment, nil
	}

	claimSet, err := modelBackendClaimSetDigest(mb.Spec.PersistentVolumes)
	if err != nil {
		return assessment, err
	}
	expectedNames := make(map[string]v1alpha1.ModelBackendPersistentVolumeSpec, len(mb.Spec.PersistentVolumes))
	for _, volume := range mb.Spec.PersistentVolumes {
		expectedNames[modelBackendPVCName(mb, volume.Name)] = volume
	}
	var claims corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &claims, client.InNamespace(mb.Namespace)); err != nil {
		return assessment, fmt.Errorf("list ModelBackend claims: %w", err)
	}
	for i := range claims.Items {
		claim := &claims.Items[i]
		if !modelBackendControllerOwner(claim, mb) {
			continue
		}
		if _, expected := expectedNames[claim.Name]; !expected || claim.Annotations[modelBackendClaimSetAnnotation] != claimSet {
			assessment.Ready = false
			assessment.CanSchedule = false
			assessment.Message = fmt.Sprintf("managed claim set is immutable after provisioning; owned PVC %s/%s does not match the declared names, paths, and classes", claim.Namespace, claim.Name)
			return assessment, nil
		}
	}

	for _, declaration := range mb.Spec.PersistentVolumes {
		claimAssessment, reconcileErr := r.reconcileModelBackendClaim(ctx, mb, declaration, claimSet)
		if reconcileErr != nil {
			return assessment, reconcileErr
		}
		if !claimAssessment.CanSchedule {
			return claimAssessment, nil
		}
		if !claimAssessment.Ready && assessment.Ready {
			assessment = claimAssessment
		}
	}
	return assessment, nil
}

//nolint:gocyclo // each ownership, immutable identity, growth, PVC, PV, and OpenEBS predicate is actionable.
func (r *ModelBackendReconciler) reconcileModelBackendClaim(ctx context.Context, mb *v1alpha1.ModelBackend, declaration v1alpha1.ModelBackendPersistentVolumeSpec, claimSet string) (modelBackendStorageAssessment, error) {
	assessment := modelBackendStorageAssessment{Ready: true, CanSchedule: true}
	key := types.NamespacedName{Namespace: mb.Namespace, Name: modelBackendPVCName(mb, declaration.Name)}
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, key, &pvc); err != nil {
		if !errors.IsNotFound(err) {
			return assessment, fmt.Errorf("read managed PVC %s: %w", key, err)
		}
		filesystem := corev1.PersistentVolumeFilesystem
		pvc = corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: key.Name, Namespace: key.Namespace,
				Labels: map[string]string{modelBackendClaimLabel: boundedDNSLabel(mb.Name, 63)},
				Annotations: map[string]string{
					modelBackendClaimSetAnnotation:    claimSet,
					modelBackendDeclarationAnnotation: declaration.Name,
					modelBackendMountPathAnnotation:   declaration.MountPath,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: &declaration.StorageClassName,
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				VolumeMode:       &filesystem,
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: declaration.Size,
				}},
			},
		}
		if err := controllerutil.SetControllerReference(mb, &pvc, r.Scheme); err != nil {
			return assessment, err
		}
		if err := r.Create(ctx, &pvc); err != nil {
			if errors.IsAlreadyExists(err) {
				assessment.Ready = false
				assessment.CanSchedule = false
				assessment.Message = fmt.Sprintf("refusing to adopt unrelated PVC %s", key)
				return assessment, nil
			}
			return assessment, fmt.Errorf("create managed PVC %s: %w", key, err)
		}
		assessment.Ready = false
		assessment.Message = fmt.Sprintf("managed PVC %s created and waiting for its first serving consumer", key)
		return assessment, nil
	}

	if !modelBackendControllerOwner(&pvc, mb) {
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = fmt.Sprintf("refusing to adopt unrelated PVC %s", key)
		return assessment, nil
	}
	if pvc.Annotations[modelBackendClaimSetAnnotation] != claimSet ||
		pvc.Annotations[modelBackendDeclarationAnnotation] != declaration.Name ||
		pvc.Annotations[modelBackendMountPathAnnotation] != declaration.MountPath {
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = fmt.Sprintf("managed PVC %s identity is immutable; declaration name, mount path, or claim set changed", key)
		return assessment, nil
	}
	filesystem := corev1.PersistentVolumeFilesystem
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != declaration.StorageClassName ||
		len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce ||
		pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != filesystem {
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = fmt.Sprintf("managed PVC %s must retain class=%s access=ReadWriteOnce volumeMode=Filesystem; replacement is forbidden", key, declaration.StorageClassName)
		return assessment, nil
	}
	current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	switch current.Cmp(declaration.Size) {
	case 1:
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = fmt.Sprintf("managed PVC %s shrink is unsupported (current request %s, desired %s); the claim and bytes were preserved", key, current.String(), declaration.Size.String())
		return assessment, nil
	case -1:
		if (pvc.Status.Phase == corev1.ClaimPending || pvc.Status.Phase == "") && pvc.Spec.VolumeName == "" {
			assessment.Ready = false
			assessment.Message = fmt.Sprintf("managed PVC %s must bind at its existing request %s before growing in place to %s; the serving pod may schedule to satisfy WaitForFirstConsumer", key, current.String(), declaration.Size.String())
			return assessment, nil
		}
		base := pvc.DeepCopy()
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = declaration.Size
		if err := r.Patch(ctx, &pvc, client.MergeFrom(base)); err != nil {
			return assessment, fmt.Errorf("grow managed PVC %s from %s to %s: %w", key, current.String(), declaration.Size.String(), err)
		}
		assessment.Ready = false
		assessment.Message = fmt.Sprintf("managed PVC %s is growing in place from %s to %s; serving remains unroutable until PVC/PV/OpenEBS/XFS convergence", key, current.String(), declaration.Size.String())
		return assessment, nil
	}

	if (pvc.Status.Phase == corev1.ClaimPending || pvc.Status.Phase == "") && pvc.Spec.VolumeName == "" {
		assessment.Ready = false
		assessment.Message = fmt.Sprintf("managed PVC %s is waiting for its first serving consumer under WaitForFirstConsumer", key)
		return assessment, nil
	}
	if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" {
		assessment.Ready = false
		assessment.Message = fmt.Sprintf("managed PVC %s phase=%s; inspect OpenEBS provisioning, topology, and iterabase-data capacity", key, pvc.Status.Phase)
		return assessment, nil
	}
	capacity := pvc.Status.Capacity[corev1.ResourceStorage]
	if capacity.Cmp(declaration.Size) < 0 {
		assessment.Ready = false
		assessment.Message = fmt.Sprintf("managed PVC %s request is %s but reported capacity is %s; preserving the serving pod for mounted XFS growth", key, declaration.Size.String(), capacity.String())
		return assessment, nil
	}
	for _, condition := range pvc.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			assessment.Ready = false
			assessment.Message = fmt.Sprintf("managed PVC %s reports resize/provisioning condition %s; preserving the serving pod until convergence", key, condition.Type)
			return assessment, nil
		}
	}

	var pv corev1.PersistentVolume
	if err := r.Get(ctx, types.NamespacedName{Name: pvc.Spec.VolumeName}, &pv); err != nil {
		assessment.Ready = false
		assessment.CanSchedule = false
		if errors.IsNotFound(err) {
			assessment.Message = fmt.Sprintf("managed PVC %s references missing PV %q", key, pvc.Spec.VolumeName)
			return assessment, nil
		}
		return assessment, fmt.Errorf("read managed PV %q: %w", pvc.Spec.VolumeName, err)
	}
	if failure := validateModelBackendPV(&pv, declaration.StorageClassName, declaration.Size); failure != "" {
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = failure
		return assessment, nil
	}
	if failure, err := r.validateModelBackendLVMVolume(ctx, mb.Namespace, &pv, declaration.Size); failure != "" || err != nil {
		assessment.Ready = false
		assessment.CanSchedule = false
		assessment.Message = failure
		return assessment, err
	}
	return assessment, nil
}

func validateModelBackendStorageClass(class *storagev1.StorageClass) string {
	binding := storagev1.VolumeBindingImmediate
	if class.VolumeBindingMode != nil {
		binding = *class.VolumeBindingMode
	}
	reclaim := corev1.PersistentVolumeReclaimDelete
	if class.ReclaimPolicy != nil {
		reclaim = *class.ReclaimPolicy
	}
	expected := map[string]string{
		"storage": "lvm", "vgpattern": "^iterabase-data$", "fsType": "xfs", "thinProvision": "no", "shared": "no",
	}
	if class.Name != modelBackendStorageClass || class.Provisioner != modelBackendStorageProvisioner ||
		binding != storagev1.VolumeBindingWaitForFirstConsumer || reclaim != corev1.PersistentVolumeReclaimDelete ||
		class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion || !reflect.DeepEqual(class.Parameters, expected) || storageClassIsDefault(class) {
		return fmt.Sprintf("StorageClass %q must be non-default expandable provisioner=%s binding=WaitForFirstConsumer reclaim=Delete parameters=%v", class.Name, modelBackendStorageProvisioner, expected)
	}
	return ""
}

func validateModelBackendPV(pv *corev1.PersistentVolume, className string, desired resource.Quantity) string {
	if failure := validateAgentPoolPV(pv, className); failure != "" {
		return strings.ReplaceAll(failure, "AgentPool", "ModelBackend")
	}
	capacity := pv.Spec.Capacity[corev1.ResourceStorage]
	if capacity.Cmp(desired) < 0 {
		return fmt.Sprintf("ModelBackend PV %s capacity %s is below desired request %s", pv.Name, capacity.String(), desired.String())
	}
	return ""
}

//nolint:gocyclo // exact LVMVolume/LVMNode identity predicates retain distinct failures.
func (r *ModelBackendReconciler) validateModelBackendLVMVolume(ctx context.Context, namespace string, pv *corev1.PersistentVolume, desired resource.Quantity) (string, error) {
	volume := &unstructured.Unstructured{}
	volume.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMVolume"})
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pv.Spec.CSI.VolumeHandle}, volume); err != nil {
		message := fmt.Sprintf("ModelBackend PV %s OpenEBS LVMVolume %s is unavailable: %v", pv.Name, pv.Spec.CSI.VolumeHandle, err)
		if errors.IsNotFound(err) {
			return message, nil
		}
		return message, err
	}
	volGroup, _, _ := unstructured.NestedString(volume.Object, "spec", "volGroup")
	vgPattern, _, _ := unstructured.NestedString(volume.Object, "spec", "vgPattern")
	shared, _, _ := unstructured.NestedString(volume.Object, "spec", "shared")
	thin, _, _ := unstructured.NestedString(volume.Object, "spec", "thinProvision")
	ownerNode, _, _ := unstructured.NestedString(volume.Object, "spec", "ownerNodeID")
	state, _, _ := unstructured.NestedString(volume.Object, "status", "state")
	capacityText, _, _ := unstructured.NestedString(volume.Object, "spec", "capacity")
	capacity, capacityErr := resource.ParseQuantity(capacityText)
	if volGroup != modelBackendStorageVolumeGroup || vgPattern != "^iterabase-data$" || shared != "no" || thin != "no" || ownerNode == "" || state != "Ready" || capacityErr != nil || capacity.Cmp(desired) < 0 {
		return fmt.Sprintf("ModelBackend LVMVolume %s/%s must be Ready thick shared=no vg=%s at capacity >=%s with one owner node (observed state=%s vg=%s shared=%s thin=%s capacity=%s)", volume.GetNamespace(), volume.GetName(), modelBackendStorageVolumeGroup, desired.String(), state, volGroup, shared, thin, capacityText), nil
	}
	if !pvHasExactNodeTopology(pv, ownerNode) {
		return fmt.Sprintf("ModelBackend PV %s node topology does not match LVMVolume owner node %s", pv.Name, ownerNode), nil
	}

	node := &unstructured.Unstructured{}
	node.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMNode"})
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ownerNode}, node); err != nil {
		message := fmt.Sprintf("ModelBackend OpenEBS LVMNode %s is unavailable in namespace %s: %v", ownerNode, namespace, err)
		if errors.IsNotFound(err) {
			return message, nil
		}
		return message, err
	}
	groups, found, err := unstructured.NestedSlice(node.Object, "volumeGroups")
	if err != nil || !found {
		return fmt.Sprintf("ModelBackend LVMNode %s/%s has no readable volumeGroups", node.GetNamespace(), ownerNode), nil
	}
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok || group["name"] != modelBackendStorageVolumeGroup {
			continue
		}
		uuid, _ := group["uuid"].(string)
		thinPools, _ := group["thinPools"].([]any)
		if uuid == "" || nestedNumber(group["missingPvCount"]) != 0 || len(thinPools) != 0 {
			return fmt.Sprintf("ModelBackend LVMNode %s/%s VG %s must have a UUID, no missing PVs, and no thin pools", node.GetNamespace(), ownerNode, modelBackendStorageVolumeGroup), nil
		}
		return "", nil
	}
	return fmt.Sprintf("ModelBackend LVMNode %s/%s does not report VG %s", node.GetNamespace(), ownerNode, modelBackendStorageVolumeGroup), nil
}
