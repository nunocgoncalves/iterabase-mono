package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
)

const (
	agentPoolWorkspaceStorageClass = "iterabase-agentpool-local-path"
	agentPoolWorkspaceProvisioner  = "rancher.io/local-path"
	agentPoolWorkspaceMount        = "/var/lib/iterabase/agentpool-workspaces"
	storageModeLocalPathRWO        = "local-path-rwo"
)

const (
	storageReasonClassMissing                   = "StorageClassMissing"
	storageReasonClassMismatch                  = "StorageClassMismatch"
	storageReasonPVCProvisioning                = "PVCProvisioning"
	storageReasonPVCExpansionFailed             = "PVCExpansionFailed"
	storageReasonPVCUnavailable                 = "PVCUnavailable"
	storageReasonMountRootUnsafe                = "MountRootUnsafe"
	storageReasonCapacity                       = "CapacityInsufficient"
	storageReasonRecoveryPending                = "StorageRecoveryPending"
	storageReasonFreshWorkersReady              = "FreshWorkersReady"
	storageReasonReady                          = "StorageReady"
	storageReasonOperationalReadinessReached    = "ReadyWorkersObserved"
	storageConditionReady                       = "StorageReady"
	storageConditionOperationalReadinessReached = "OperationalReadinessReached"
	storageConditionWorkerReplacementPending    = "StorageWorkerReplacementPending"
)

type agentPoolStorageAssessment struct {
	Ready              bool
	CanMount           bool
	Reason             string
	Message            string
	Mode               string
	ClassName          string
	PVName             string
	VolumeHandle       string
	ReplacementPending bool
}

// assessAgentPoolStorage validates the fixed Forge-owned local-path contract.
// A Pending WaitForFirstConsumer claim remains mount-capable so the first worker
// can schedule and trigger binding; every bound identity/path check is fail
// closed before an established worker set is retained.
//
//nolint:gocyclo // ordered fail-closed predicates intentionally map to stable condition reasons.
func (r *AgentPoolReconciler) assessAgentPoolStorage(ctx context.Context, pool *v1alpha1.AgentPool) agentPoolStorageAssessment {
	assessment := agentPoolStorageAssessment{
		Mode: storageModeLocalPathRWO, ClassName: pool.Spec.Sandbox.StorageClassName,
	}
	if assessment.ClassName != agentPoolWorkspaceStorageClass || pool.Spec.Sandbox.AccessMode != corev1.ReadWriteOnce {
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("AgentPool storage must remain class=%s access=ReadWriteOnce (observed class=%s access=%s); alternate/default/RWX storage has no V2 fallback", agentPoolWorkspaceStorageClass, assessment.ClassName, pool.Spec.Sandbox.AccessMode)
		return assessment
	}

	var class storagev1.StorageClass
	if err := r.Get(ctx, types.NamespacedName{Name: assessment.ClassName}, &class); err != nil {
		assessment.Reason = storageReasonClassMissing
		assessment.Message = fmt.Sprintf("StorageClass %q is unavailable; reapply Forge's dedicated local-path configuration before reconciling AgentPools", assessment.ClassName)
		if !errors.IsNotFound(err) {
			assessment.Message = fmt.Sprintf("read StorageClass %q: %v", assessment.ClassName, err)
		}
		return assessment
	}
	if failure := validateAgentPoolStorageClass(&class); failure != "" {
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = failure
		return assessment
	}
	assessment.CanMount = true

	var pvc corev1.PersistentVolumeClaim
	pvcName := sandboxPVCName(pool)
	if err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: pvcName}, &pvc); err != nil {
		assessment.Reason = storageReasonPVCProvisioning
		assessment.Message = fmt.Sprintf("PVC %s/%s has not been created yet", pool.Namespace, pvcName)
		return assessment
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != assessment.ClassName || len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		assessment.CanMount = false
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("PVC %s/%s must retain class=%s access=ReadWriteOnce; bound class/access changes require explicit settlement and recreation", pool.Namespace, pvcName, assessment.ClassName)
		return assessment
	}
	if (pvc.Status.Phase == corev1.ClaimPending || pvc.Status.Phase == "") && pvc.Spec.VolumeName == "" {
		if pool.Spec.Replicas == 0 {
			assessment.Ready = true
			assessment.Reason = storageReasonReady
			assessment.Message = fmt.Sprintf("StorageReady: scaled-to-zero PVC %s/%s is intentionally unbound under WaitForFirstConsumer", pool.Namespace, pvcName)
			return assessment
		}
		assessment.Reason = storageReasonPVCProvisioning
		assessment.Message = fmt.Sprintf("PVC %s/%s is waiting for its first consumer on the dedicated class; create workers to trigger WaitForFirstConsumer binding", pool.Namespace, pvcName)
		return assessment
	}
	if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" {
		assessment.Reason = storageReasonPVCProvisioning
		assessment.Message = fmt.Sprintf("PVC %s/%s phase=%s; inspect local-path provisioner, node, and claim events", pool.Namespace, pvcName, pvc.Status.Phase)
		return assessment
	}
	assessment.PVName = pvc.Spec.VolumeName
	requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	capacity := pvc.Status.Capacity[corev1.ResourceStorage]
	if capacity.IsZero() || capacity.Cmp(requested) < 0 {
		assessment.Reason = storageReasonCapacity
		assessment.Message = fmt.Sprintf("PVC %s/%s requested planning size %s but reports capacity %s; inspect provisioning identity (local-path does not provide a hard quota or expansion)", pool.Namespace, pvcName, requested.String(), capacity.String())
		return assessment
	}
	for _, condition := range pvc.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			assessment.Reason = storageReasonPVCProvisioning
			assessment.Message = fmt.Sprintf("PVC %s/%s reports condition %s=%s; keep fresh credits closed until the claim settles", pool.Namespace, pvcName, condition.Type, condition.Status)
			return assessment
		}
	}

	var pv corev1.PersistentVolume
	if err := r.Get(ctx, types.NamespacedName{Name: assessment.PVName}, &pv); err != nil {
		assessment.CanMount = false
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = fmt.Sprintf("bound PVC %s/%s references unavailable PV %q: %v", pool.Namespace, pvcName, assessment.PVName, err)
		return assessment
	}
	if failure := validateAgentPoolPV(&pv, assessment.ClassName); failure != "" {
		assessment.CanMount = false
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = failure
		return assessment
	}
	pvCapacity := pv.Spec.Capacity[corev1.ResourceStorage]
	if pvCapacity.Cmp(requested) < 0 {
		assessment.CanMount = false
		assessment.Reason = storageReasonCapacity
		assessment.Message = fmt.Sprintf("PV %s planning capacity %s is below PVC request %s", pv.Name, pvCapacity.String(), requested.String())
		return assessment
	}
	assessment.VolumeHandle = pv.Spec.HostPath.Path
	assessment.Ready = true
	assessment.Reason = storageReasonReady
	assessment.Message = fmt.Sprintf("StorageReady: class=%s provisioner=%s pvc=%s/%s pv=%s path=%s access=ReadWriteOnce reclaim=Delete expansion=false", assessment.ClassName, agentPoolWorkspaceProvisioner, pool.Namespace, pvcName, assessment.PVName, assessment.VolumeHandle)
	return assessment
}

func validateAgentPoolStorageClass(class *storagev1.StorageClass) string {
	binding := storagev1.VolumeBindingImmediate
	if class.VolumeBindingMode != nil {
		binding = *class.VolumeBindingMode
	}
	reclaim := corev1.PersistentVolumeReclaimDelete
	if class.ReclaimPolicy != nil {
		reclaim = *class.ReclaimPolicy
	}
	if class.Name != agentPoolWorkspaceStorageClass || class.Provisioner != agentPoolWorkspaceProvisioner || binding != storagev1.VolumeBindingWaitForFirstConsumer || reclaim != corev1.PersistentVolumeReclaimDelete || (class.AllowVolumeExpansion != nil && *class.AllowVolumeExpansion) || len(class.Parameters) != 0 || storageClassIsDefault(class) {
		return fmt.Sprintf("StorageClass %q must be non-default provisioner=%s binding=WaitForFirstConsumer reclaim=Delete expansion=false with no alternate path parameters (observed provisioner=%s binding=%s reclaim=%s expansion=%v default=%v parameters=%v)", class.Name, agentPoolWorkspaceProvisioner, class.Provisioner, binding, reclaim, pointerValue(class.AllowVolumeExpansion), storageClassIsDefault(class), class.Parameters)
	}
	return ""
}

func storageClassIsDefault(class *storagev1.StorageClass) bool {
	return class.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || class.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true"
}

//nolint:gocyclo // each exact PV identity predicate has a distinct actionable refusal.
func validateAgentPoolPV(pv *corev1.PersistentVolume, className string) string {
	volumeMode := corev1.PersistentVolumeFilesystem
	if pv.Spec.VolumeMode != nil {
		volumeMode = *pv.Spec.VolumeMode
	}
	if pv.Status.Phase != corev1.VolumeBound || pv.Spec.StorageClassName != className || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete || len(pv.Spec.AccessModes) != 1 || pv.Spec.AccessModes[0] != corev1.ReadWriteOnce || volumeMode != corev1.PersistentVolumeFilesystem || pv.Spec.HostPath == nil {
		return fmt.Sprintf("PV %s must remain Bound, class=%s, ReadWriteOnce Filesystem hostPath, and Delete (observed phase=%s class=%s access=%v reclaim=%s)", pv.Name, className, pv.Status.Phase, pv.Spec.StorageClassName, pv.Spec.AccessModes, pv.Spec.PersistentVolumeReclaimPolicy)
	}
	if pv.Spec.HostPath.Type != nil && *pv.Spec.HostPath.Type != corev1.HostPathDirectoryOrCreate && *pv.Spec.HostPath.Type != corev1.HostPathDirectory {
		return fmt.Sprintf("PV %s hostPath type %s is not a directory", pv.Name, *pv.Spec.HostPath.Type)
	}
	path := pv.Spec.HostPath.Path
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || clean != path || path == agentPoolWorkspaceMount || !strings.HasPrefix(path, agentPoolWorkspaceMount+string(filepath.Separator)) {
		return fmt.Sprintf("PV %s path %q must resolve beneath dedicated workspace mount %s; root/default-path fallback is refused", pv.Name, path, agentPoolWorkspaceMount)
	}
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return fmt.Sprintf("PV %s lacks the local-path node affinity required by one-node RWO", pv.Name)
	}
	return ""
}

func storageWasOperationallyReady(pool *v1alpha1.AgentPool) bool {
	for _, condition := range pool.Status.Conditions {
		if condition.Type == storageConditionOperationalReadinessReached && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	if !pool.Status.Ready || pool.Status.ReadyReplicas == 0 {
		return false
	}
	for _, condition := range pool.Status.Conditions {
		if condition.Type == storageConditionReady {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

func storageWorkerReplacementPending(pool *v1alpha1.AgentPool) bool {
	for _, condition := range pool.Status.Conditions {
		if condition.Type == storageConditionWorkerReplacementPending {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

func (r *AgentPoolReconciler) quiesceWorkers(ctx context.Context, pool *v1alpha1.AgentPool) error {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(pool.Namespace), client.MatchingLabels(poolLabels(pool))); err != nil {
		return fmt.Errorf("list workers while quiescing storage-unready AgentPool: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete storage-unready worker %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}
	return nil
}

func workerStorageFailure(ctx context.Context, c client.Client, pool *v1alpha1.AgentPool) string {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(pool.Namespace), client.MatchingLabels(poolLabels(pool))); err != nil {
		return ""
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		statuses := append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...)
		statuses = append(statuses, pod.Status.ContainerStatuses...)
		for _, status := range statuses {
			if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
				return fmt.Sprintf("worker %s/%s container %s exited during workspace mount/I/O validation (reason=%s message=%s); inspect the dedicated receipt-matching ext4/XFS mount, local-path PV, ownership, and capacity", pod.Namespace, pod.Name, status.Name, status.State.Terminated.Reason, status.State.Terminated.Message)
			}
			if status.State.Waiting != nil {
				reason := status.State.Waiting.Reason
				if reason == "CreateContainerError" || reason == "CrashLoopBackOff" || reason == "RunContainerError" {
					return fmt.Sprintf("worker %s/%s container %s cannot validate/mount dedicated storage (reason=%s message=%s); inspect PVC/PV path, mount identity, ownership, free space, and node events", pod.Namespace, pod.Name, status.Name, reason, status.State.Waiting.Message)
				}
			}
		}
	}
	return ""
}

func pointerValue[T any](value *T) any {
	if value == nil {
		return "<nil>"
	}
	return *value
}
