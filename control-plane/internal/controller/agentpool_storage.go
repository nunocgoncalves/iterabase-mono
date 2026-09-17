package controller

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/gateway"
)

const (
	agentPoolWorkspaceStorageClass = "iterabase-agentpool-lvm-xfs"
	agentPoolWorkspaceProvisioner  = "local.csi.openebs.io"
	agentPoolWorkspaceVolumeGroup  = managedOpenEBSVolumeGroup
	storageModeOpenEBSLVMRWO       = "openebs-lvm-xfs-rwo"
)

const (
	storageReasonClassMissing                   = "StorageClassMissing"
	storageReasonClassMismatch                  = "StorageClassMismatch"
	storageReasonPVCProvisioning                = "PVCProvisioning"
	storageReasonPVCExpansionPending            = "PVCExpansionPending"
	storageReasonPVCExpansionFailed             = "PVCExpansionFailed"
	storageReasonPVCShrinkRefused               = "PVCShrinkRefused"
	storageReasonPVCUnavailable                 = "PVCUnavailable"
	storageReasonMountRootUnsafe                = "MountRootUnsafe"
	storageReasonCapacity                       = "CapacityInsufficient"
	storageReasonRecoveryPending                = "StorageRecoveryPending"
	storageReasonFreshWorkersReady              = "FreshWorkersReady"
	storageReasonReady                          = "StorageReady"
	storageReasonOperationalReadinessReached    = "ReadyWorkersObserved"
	storageReasonWorkspaceCapacityHealthy       = "WorkspaceCapacityHealthy"
	storageReasonWorkspaceCapacityWarning       = "WorkspaceCapacityWarning"
	storageReasonWorkspaceCapacityGated         = "WorkspaceCapacityGateActive"
	storageReasonWorkspaceCapacityUnknown       = "WorkspaceCapacityUnknown"
	storageConditionReady                       = "StorageReady"
	storageConditionOperationalReadinessReached = "OperationalReadinessReached"
	storageConditionWorkerReplacementPending    = "StorageWorkerReplacementPending"
	storageConditionWorkspaceCapacityHealthy    = "WorkspaceCapacityHealthy"
)

const workspaceCapacityObservationFreshness = time.Minute

type agentPoolStorageAssessment struct {
	Ready               bool
	CanMount            bool
	ConfirmedUnsafe     bool
	ObservationUnknown  bool
	ObservationErr      error
	Reason              string
	Message             string
	Mode                string
	ClassName           string
	PVName              string
	VolumeHandle        string
	ReplacementPending  bool
	SafeResize          bool
	ResizeStartedAt     *time.Time
	ResizeBaselineBytes uint64
}

func storageQuiescenceRequired(assessment agentPoolStorageAssessment) bool {
	return assessment.ConfirmedUnsafe && !assessment.ObservationUnknown
}

// assessAgentPoolStorage validates the fixed chart-owned OpenEBS LVM contract.
// A Pending WaitForFirstConsumer claim remains mount-capable so the first worker
// can schedule and trigger binding; every bound identity/path check is fail
// closed before an established worker set is retained.
//
//nolint:gocyclo // ordered fail-closed predicates intentionally map to stable condition reasons.
func (r *AgentPoolReconciler) assessAgentPoolStorage(ctx context.Context, pool *v1alpha1.AgentPool) agentPoolStorageAssessment {
	assessment := agentPoolStorageAssessment{
		Mode: storageModeOpenEBSLVMRWO, ClassName: pool.Spec.Sandbox.StorageClassName,
	}
	if assessment.ClassName != agentPoolWorkspaceStorageClass || pool.Spec.Sandbox.AccessMode != corev1.ReadWriteOnce {
		assessment.ConfirmedUnsafe = true
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("AgentPool storage must remain class=%s access=ReadWriteOnce (observed class=%s access=%s); alternate/default/RWX storage has no V2 fallback", agentPoolWorkspaceStorageClass, assessment.ClassName, pool.Spec.Sandbox.AccessMode)
		return assessment
	}

	var class storagev1.StorageClass
	if err := r.Get(ctx, types.NamespacedName{Name: assessment.ClassName}, &class); err != nil {
		assessment.Reason = storageReasonClassMissing
		if errors.IsNotFound(err) {
			assessment.ConfirmedUnsafe = true
			assessment.Message = fmt.Sprintf("StorageClass %q is unavailable; reapply the pinned LVM storage substrate before reconciling AgentPools", assessment.ClassName)
		} else {
			assessment.ObservationUnknown = true
			assessment.ObservationErr = err
			assessment.Message = fmt.Sprintf("read StorageClass %q: %v", assessment.ClassName, err)
		}
		return assessment
	}
	if failure := validateAgentPoolStorageClass(&class); failure != "" {
		assessment.ConfirmedUnsafe = true
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = failure
		return assessment
	}
	assessment.CanMount = true

	var pvc corev1.PersistentVolumeClaim
	pvcName := sandboxPVCName(pool)
	if err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: pvcName}, &pvc); err != nil {
		assessment.Reason = storageReasonPVCProvisioning
		if errors.IsNotFound(err) {
			assessment.Message = fmt.Sprintf("PVC %s/%s has not been created yet", pool.Namespace, pvcName)
		} else {
			assessment.CanMount = false
			assessment.ObservationUnknown = true
			assessment.ObservationErr = err
			assessment.Message = fmt.Sprintf("read PVC %s/%s: %v", pool.Namespace, pvcName, err)
		}
		return assessment
	}
	owner := metav1.GetControllerOf(&pvc)
	if owner == nil || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Kind != "AgentPool" || owner.Name != pool.Name || owner.UID != pool.UID {
		assessment.CanMount = false
		assessment.ConfirmedUnsafe = true
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = fmt.Sprintf("PVC %s/%s must remain controller-owned by AgentPool %s uid=%s; refusing adoption or ownership mutation", pool.Namespace, pvcName, pool.Name, pool.UID)
		return assessment
	}
	pvcVolumeMode := corev1.PersistentVolumeFilesystem
	if pvc.Spec.VolumeMode != nil {
		pvcVolumeMode = *pvc.Spec.VolumeMode
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != assessment.ClassName || len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce || pvc.Spec.VolumeMode == nil || pvcVolumeMode != corev1.PersistentVolumeFilesystem {
		assessment.CanMount = false
		assessment.ConfirmedUnsafe = true
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("PVC %s/%s must retain class=%s access=ReadWriteOnce volumeMode=Filesystem; bound identity changes require explicit settlement and recreation", pool.Namespace, pvcName, assessment.ClassName)
		return assessment
	}
	requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	desired := pool.Spec.Sandbox.Size
	if requested.Cmp(desired) < 0 {
		assessment.SafeResize = true
		assessment.Reason = storageReasonPVCExpansionPending
		assessment.Message = fmt.Sprintf("PVC %s/%s request %s is growing in place to %s; retain mounted workers and withhold fresh credits until OpenEBS/XFS/statfs convergence", pool.Namespace, pvcName, requested.String(), desired.String())
		return assessment
	}
	if requested.Cmp(desired) > 0 {
		assessment.Reason = storageReasonPVCShrinkRefused
		assessment.Message = fmt.Sprintf("PVC %s/%s shrink is unsupported (current request %s desired %s); preserve the claim, workers, and bytes", pool.Namespace, pvcName, requested.String(), desired.String())
		return assessment
	}
	if resizeStarted := pvc.Annotations[agentPoolResizeStartedAnnotation]; resizeStarted != "" {
		started, err := time.Parse(time.RFC3339Nano, resizeStarted)
		if err != nil {
			assessment.ConfirmedUnsafe = true
			assessment.CanMount = false
			assessment.Reason = storageReasonPVCExpansionFailed
			assessment.Message = fmt.Sprintf("PVC %s/%s has malformed controller resize evidence; preserve the claim and inspect annotation %s", pool.Namespace, pvcName, agentPoolResizeStartedAnnotation)
			return assessment
		}
		baselineText := pvc.Annotations[agentPoolResizeBaselineCapacityAnnotation]
		baseline, err := strconv.ParseUint(baselineText, 10, 64)
		if err != nil || baseline == 0 {
			assessment.ConfirmedUnsafe = true
			assessment.CanMount = false
			assessment.Reason = storageReasonPVCExpansionFailed
			assessment.Message = fmt.Sprintf("PVC %s/%s has malformed controller filesystem-capacity baseline evidence; preserve the claim and inspect annotation %s", pool.Namespace, pvcName, agentPoolResizeBaselineCapacityAnnotation)
			return assessment
		}
		assessment.ResizeStartedAt = &started
		assessment.ResizeBaselineBytes = baseline
		assessment.SafeResize = true
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
		assessment.Message = fmt.Sprintf("PVC %s/%s phase=%s; inspect OpenEBS LVM controller/node, iterabase-data capacity, topology, and claim events", pool.Namespace, pvcName, pvc.Status.Phase)
		return assessment
	}
	assessment.PVName = pvc.Spec.VolumeName
	capacity := pvc.Status.Capacity[corev1.ResourceStorage]
	if capacity.IsZero() || capacity.Cmp(requested) < 0 {
		if assessment.SafeResize {
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = fmt.Sprintf("PVC %s/%s request is %s but reported capacity is %s; retain mounted workers for online XFS growth", pool.Namespace, pvcName, requested.String(), capacity.String())
			return assessment
		}
		assessment.Reason = storageReasonCapacity
		assessment.Message = fmt.Sprintf("PVC %s/%s requested thick XFS capacity %s but reports %s; inspect OpenEBS provisioning and iterabase-data free capacity", pool.Namespace, pvcName, requested.String(), capacity.String())
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
		if errors.IsNotFound(err) {
			assessment.ConfirmedUnsafe = true
		} else {
			assessment.ObservationUnknown = true
			assessment.ObservationErr = err
		}
		return assessment
	}
	if failure := validateAgentPoolPV(&pv, assessment.ClassName); failure != "" {
		assessment.CanMount = false
		assessment.ConfirmedUnsafe = true
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = failure
		return assessment
	}
	if failure, resizePending, unknown, err := r.validateAgentPoolLVMVolume(ctx, pool.Namespace, &pv, desired); failure != "" {
		if resizePending && assessment.SafeResize {
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = failure
			return assessment
		}
		assessment.CanMount = false
		assessment.ConfirmedUnsafe = !unknown
		assessment.ObservationUnknown = unknown
		assessment.ObservationErr = err
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = failure
		return assessment
	}
	pvCapacity := pv.Spec.Capacity[corev1.ResourceStorage]
	if pvCapacity.Cmp(requested) < 0 {
		if assessment.SafeResize {
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = fmt.Sprintf("PV %s capacity %s is growing in place to PVC request %s; retain mounted workers", pv.Name, pvCapacity.String(), requested.String())
			return assessment
		}
		assessment.CanMount = false
		assessment.ConfirmedUnsafe = true
		assessment.Reason = storageReasonCapacity
		assessment.Message = fmt.Sprintf("PV %s planning capacity %s is below PVC request %s", pv.Name, pvCapacity.String(), requested.String())
		return assessment
	}
	assessment.VolumeHandle = pv.Spec.CSI.VolumeHandle
	if assessment.ResizeStartedAt != nil {
		assessment.SafeResize = true
		if r.CapacityReader == nil {
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = fmt.Sprintf("PVC %s/%s infrastructure resize converged; waiting for a fresh mounted-filesystem statfs observation after %s", pool.Namespace, pvcName, assessment.ResizeStartedAt.Format(time.RFC3339Nano))
			return assessment
		}
		status, err := r.CapacityReader.WorkspaceCapacityStatus(ctx, pool.Namespace+"/"+pool.Name)
		if err != nil {
			assessment.ObservationUnknown = true
			assessment.ObservationErr = err
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = fmt.Sprintf("PVC %s/%s infrastructure resize converged but the required post-resize statfs observation is unavailable: %v", pool.Namespace, pvcName, err)
			return assessment
		}
		if !workspaceCapacityStatusValid(status) || status.ObservedAt == nil || !status.ObservedAt.After(*assessment.ResizeStartedAt) || time.Since(*status.ObservedAt) > workspaceCapacityObservationFreshness || status.CapacityBytes <= assessment.ResizeBaselineBytes {
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = fmt.Sprintf("PVC %s/%s infrastructure resize converged; waiting for a fresh valid mounted-filesystem statfs capacity greater than the pre-resize %d bytes after %s", pool.Namespace, pvcName, assessment.ResizeBaselineBytes, assessment.ResizeStartedAt.Format(time.RFC3339Nano))
			return assessment
		}
		if err := r.completeAgentPoolResize(ctx, &pvc); err != nil {
			assessment.ObservationUnknown = true
			assessment.ObservationErr = err
			assessment.Reason = storageReasonPVCExpansionPending
			assessment.Message = fmt.Sprintf("PVC %s/%s resize converged but completion evidence could not be persisted; retaining mounted workers and fresh-credit closure: %v", pool.Namespace, pvcName, err)
			return assessment
		}
		assessment.ResizeStartedAt = nil
		assessment.ResizeBaselineBytes = 0
	}
	assessment.Ready = true
	assessment.SafeResize = false
	assessment.Reason = storageReasonReady
	assessment.Message = fmt.Sprintf("StorageReady: class=%s provisioner=%s pvc=%s/%s pv=%s volume=%s vg=%s fs=xfs shared=yes access=ReadWriteOnce reclaim=Delete expansion=true", assessment.ClassName, agentPoolWorkspaceProvisioner, pool.Namespace, pvcName, assessment.PVName, assessment.VolumeHandle, agentPoolWorkspaceVolumeGroup)
	return assessment
}

func (r *AgentPoolReconciler) completeAgentPoolResize(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	base := pvc.DeepCopy()
	delete(pvc.Annotations, agentPoolResizeStartedAnnotation)
	delete(pvc.Annotations, agentPoolResizeBaselineCapacityAnnotation)
	return r.Patch(ctx, pvc, client.MergeFrom(base))
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
	expectedParameters := map[string]string{
		"storage":       "lvm",
		"vgpattern":     "^iterabase-data$",
		"fsType":        "xfs",
		"thinProvision": "no",
		"shared":        "yes",
	}
	if class.Name != agentPoolWorkspaceStorageClass || class.Provisioner != agentPoolWorkspaceProvisioner || binding != storagev1.VolumeBindingWaitForFirstConsumer || reclaim != corev1.PersistentVolumeReclaimDelete || class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion || !reflect.DeepEqual(class.Parameters, expectedParameters) || storageClassIsDefault(class) {
		return fmt.Sprintf("StorageClass %q must be non-default provisioner=%s binding=WaitForFirstConsumer reclaim=Delete expansion=true parameters=%v (observed provisioner=%s binding=%s reclaim=%s expansion=%v default=%v parameters=%v)", class.Name, agentPoolWorkspaceProvisioner, expectedParameters, class.Provisioner, binding, reclaim, pointerValue(class.AllowVolumeExpansion), storageClassIsDefault(class), class.Parameters)
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
	if pv.Status.Phase != corev1.VolumeBound || pv.Spec.StorageClassName != className || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete || len(pv.Spec.AccessModes) != 1 || pv.Spec.AccessModes[0] != corev1.ReadWriteOnce || pv.Spec.VolumeMode == nil || volumeMode != corev1.PersistentVolumeFilesystem || pv.Spec.CSI == nil {
		return fmt.Sprintf("PV %s must remain Bound, class=%s, ReadWriteOnce Filesystem OpenEBS CSI, and Delete (observed phase=%s class=%s access=%v reclaim=%s)", pv.Name, className, pv.Status.Phase, pv.Spec.StorageClassName, pv.Spec.AccessModes, pv.Spec.PersistentVolumeReclaimPolicy)
	}
	if pv.Spec.CSI.Driver != agentPoolWorkspaceProvisioner || pv.Spec.CSI.FSType != "xfs" || pv.Spec.CSI.VolumeHandle == "" || pv.Spec.CSI.VolumeAttributes["openebs.io/volgroup"] != agentPoolWorkspaceVolumeGroup {
		return fmt.Sprintf("PV %s must use driver=%s fsType=xfs volumeHandle=<OpenEBS volume> openebs.io/volgroup=%s (observed driver=%s fsType=%s handle=%s attributes=%v)", pv.Name, agentPoolWorkspaceProvisioner, agentPoolWorkspaceVolumeGroup, pv.Spec.CSI.Driver, pv.Spec.CSI.FSType, pv.Spec.CSI.VolumeHandle, pv.Spec.CSI.VolumeAttributes)
	}
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil || len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) != 1 {
		return fmt.Sprintf("PV %s lacks one exact local node-affinity term required by one-node RWO", pv.Name)
	}
	return ""
}

func (r *AgentPoolReconciler) validateAgentPoolLVMVolume(ctx context.Context, namespace string, pv *corev1.PersistentVolume, desired resource.Quantity) (failure string, resizePending bool, observationUnknown bool, observationErr error) {
	observation, issue := observeManagedOpenEBSVolume(ctx, r.Client, namespace, pv)
	if issue != nil {
		return issue.Message, false, issue.Unknown, issue.Err
	}
	if observation.Shared != "yes" {
		return fmt.Sprintf("LVMVolume %s/%s must retain shared=yes for same-node AgentPool workers (observed shared=%s)", observation.Namespace, observation.Name, observation.Shared), false, false, nil
	}
	if observation.State != "Ready" {
		return fmt.Sprintf("LVMVolume %s/%s resize state is %s; retain mounted workers only while a controller-recorded resize remains outstanding", observation.Namespace, observation.Name, observation.State), true, false, nil
	}
	if observation.Capacity.Cmp(desired) < 0 {
		return fmt.Sprintf("LVMVolume %s/%s capacity %s is growing in place to %s; retain mounted workers", observation.Namespace, observation.Name, observation.Capacity.String(), desired.String()), true, false, nil
	}
	return "", false, false, nil
}

// setWorkspaceCapacityCondition projects one durable pool-PVC gate into the
// matching AgentPool's actionable condition. Ready/StorageReady continue
// to describe pod/PVC mount health; this independent condition explains why
// fresh dispatch credit is open, warning, gated, or unavailable.
func workspaceCapacityStatusValid(status gateway.WorkspaceCapacityStatus) bool {
	if !status.Observed || status.CapacityBytes == 0 || status.FreeBytes > status.CapacityBytes || math.IsNaN(status.FreeRatio) || math.IsInf(status.FreeRatio, 0) {
		return false
	}
	computed := float64(status.FreeBytes) / float64(status.CapacityBytes)
	return math.Abs(computed-status.FreeRatio) <= 0.000001
}

func (r *AgentPoolReconciler) setWorkspaceCapacityCondition(ctx context.Context, pool *v1alpha1.AgentPool) string {
	if r.CapacityReader == nil {
		return ""
	}
	condition := metav1.Condition{
		Type:               storageConditionWorkspaceCapacityHealthy,
		Status:             metav1.ConditionUnknown,
		Reason:             storageReasonWorkspaceCapacityUnknown,
		ObservedGeneration: pool.Generation,
	}
	status, err := r.CapacityReader.WorkspaceCapacityStatus(ctx, pool.Namespace+"/"+pool.Name)
	if err != nil {
		condition.Message = fmt.Sprintf("workspace capacity observation is unavailable; dispatch fails closed until this pool PVC is observed: %v", err)
		meta.SetStatusCondition(&pool.Status.Conditions, condition)
		return condition.Message
	}
	if !status.Observed || status.ObservedAt == nil || time.Since(*status.ObservedAt) > workspaceCapacityObservationFreshness {
		condition.Message = "workspace capacity observation is missing or stale; inspect harness/dispatch health before expecting fresh credits"
		meta.SetStatusCondition(&pool.Status.Conditions, condition)
		return condition.Message
	}
	if !workspaceCapacityStatusValid(status) {
		condition.Message = "workspace capacity observation is invalid; dispatch fails closed until a valid actual-filesystem measurement arrives"
		meta.SetStatusCondition(&pool.Status.Conditions, condition)
		return condition.Message
	}
	observation := fmt.Sprintf("AgentPool PVC is %.1f%% free (%d of %d bytes)", status.FreeRatio*100, status.FreeBytes, status.CapacityBytes)
	switch {
	case status.CreditGated:
		condition.Status = metav1.ConditionFalse
		condition.Reason = storageReasonWorkspaceCapacityGated
		condition.Message = observation + "; all fresh dispatch credits for this pool are withheld until its PVC reaches at least 25% free"
	case status.Warning:
		condition.Status = metav1.ConditionFalse
		condition.Reason = storageReasonWorkspaceCapacityWarning
		condition.Message = observation + "; below the 25% warning threshold but this pool's fresh credits remain open until the 20% floor"
	default:
		condition.Status = metav1.ConditionTrue
		condition.Reason = storageReasonWorkspaceCapacityHealthy
		condition.Message = observation + "; fresh dispatch credits are capacity-eligible"
	}
	meta.SetStatusCondition(&pool.Status.Conditions, condition)
	if condition.Status != metav1.ConditionTrue {
		return condition.Message
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
				return fmt.Sprintf("worker %s/%s container %s exited during workspace mount/I/O validation (reason=%s message=%s); inspect the AgentPool XFS PVC, OpenEBS LVMVolume/PV topology, ownership, and per-pool capacity", pod.Namespace, pod.Name, status.Name, status.State.Terminated.Reason, status.State.Terminated.Message)
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
