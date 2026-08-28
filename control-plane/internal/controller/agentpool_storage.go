package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
)

const (
	storageContractVersion           = "HOR-469/v1"
	storageContractLabel             = "platform.iterabase.com/storage-contract"
	storageConformanceLabel          = "platform.iterabase.com/storage-conformance"
	storageModeManagedLonghorn       = "managed-longhorn"
	storageModeExternal              = "external"
	managedLonghornStorageClass      = "iterabase-rwx"
	managedLonghornProvisioner       = "driver.longhorn.io"
	managedLonghornNamespace         = "longhorn-system"
	longhornShareManagerComponent    = "share-manager"
	longhornShareManagerComponentKey = "longhorn.io/component"
)

const (
	storageReasonClassMissing                   = "StorageClassMissing"
	storageReasonClassMismatch                  = "StorageClassMismatch"
	storageReasonConformancePending             = "StorageConformancePending"
	storageReasonConformanceFailed              = "StorageConformanceFailed"
	storageReasonPVCProvisioning                = "PVCProvisioning"
	storageReasonPVCExpansionFailed             = "PVCExpansionFailed"
	storageReasonPVCUnavailable                 = "PVCUnavailable"
	storageReasonMountRootUnsafe                = "MountRootUnsafe"
	storageReasonInitialConvergence             = "InitialStorageConvergence"
	storageReasonBackendDegraded                = "BackendDegraded"
	storageReasonShareManagerDown               = "ShareManagerUnavailable"
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

// assessAgentPoolStorage intentionally keeps the ordered fail-closed predicate
// chain visible so each stable condition reason maps to one observed resource.
//
//nolint:gocyclo
func (r *AgentPoolReconciler) assessAgentPoolStorage(ctx context.Context, pool *v1alpha1.AgentPool) agentPoolStorageAssessment {
	if pool.Spec.Sandbox.AccessMode != corev1.ReadWriteMany {
		return agentPoolStorageAssessment{Ready: true, CanMount: true, Reason: storageReasonReady, Message: "single-worker RWO storage uses the legacy bounded deployment contract"}
	}

	assessment := agentPoolStorageAssessment{ClassName: pool.Spec.Sandbox.StorageClassName}
	contract, failure := r.storageContract(ctx, pool)
	if failure != nil {
		return *failure
	}
	assessment.Mode = contract["mode"]
	if contract["storageClassName"] != assessment.ClassName {
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("AgentPool declares StorageClass %q but the installation storage contract requires %q; update the overlay without mutating or recreating the existing claim", assessment.ClassName, contract["storageClassName"])
		return assessment
	}

	var class storagev1.StorageClass
	if err := r.Get(ctx, types.NamespacedName{Name: assessment.ClassName}, &class); err != nil {
		assessment.Reason = storageReasonClassMissing
		assessment.Message = fmt.Sprintf("StorageClass %q is unavailable; install the managed RWX companion or restore the exact conforming external class", assessment.ClassName)
		if !errors.IsNotFound(err) {
			assessment.Message = fmt.Sprintf("read StorageClass %q: %v", assessment.ClassName, err)
		}
		return assessment
	}
	if class.ReclaimPolicy == nil || *class.ReclaimPolicy != corev1.PersistentVolumeReclaimRetain || class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion {
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("StorageClass %q must use reclaimPolicy=Retain and allowVolumeExpansion=true (observed reclaim=%v expansion=%v)", assessment.ClassName, pointerValue(class.ReclaimPolicy), pointerValue(class.AllowVolumeExpansion))
		return assessment
	}
	if class.Provisioner == "" {
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("StorageClass %q has no provisioner", assessment.ClassName)
		return assessment
	}
	if assessment.Mode == storageModeManagedLonghorn && (assessment.ClassName != managedLonghornStorageClass || class.Provisioner != managedLonghornProvisioner) {
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("managed storage requires %s with provisioner %s (observed class=%s provisioner=%s)", managedLonghornStorageClass, managedLonghornProvisioner, assessment.ClassName, class.Provisioner)
		return assessment
	}

	conformance, failure := r.storageConformance(ctx, &class)
	if failure != nil {
		failure.Mode = assessment.Mode
		failure.ClassName = assessment.ClassName
		return *failure
	}
	_ = conformance
	assessment.CanMount = true

	var pvc corev1.PersistentVolumeClaim
	pvcName := sandboxPVCName(pool)
	if err := r.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: pvcName}, &pvc); err != nil {
		assessment.Reason = storageReasonPVCProvisioning
		assessment.Message = fmt.Sprintf("PVC %s/%s has not been created yet", pool.Namespace, pvcName)
		return assessment
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != assessment.ClassName || len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		assessment.CanMount = false
		assessment.Reason = storageReasonClassMismatch
		assessment.Message = fmt.Sprintf("PVC %s/%s does not retain the declared class/access mode (required class=%s access=ReadWriteMany)", pool.Namespace, pvcName, assessment.ClassName)
		return assessment
	}
	if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" {
		assessment.Reason = storageReasonPVCProvisioning
		assessment.Message = fmt.Sprintf("PVC %s/%s phase=%s; inspect PVC, StorageClass, CSI, node, and provisioning events", pool.Namespace, pvcName, pvc.Status.Phase)
		return assessment
	}
	assessment.PVName = pvc.Spec.VolumeName
	requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	capacity := pvc.Status.Capacity[corev1.ResourceStorage]
	if capacity.IsZero() || capacity.Cmp(requested) < 0 {
		assessment.Reason = storageReasonPVCExpansionFailed
		assessment.Message = fmt.Sprintf("PVC %s/%s requested %s but reports usable capacity %s; wait for controller and filesystem expansion or inspect expansion events", pool.Namespace, pvcName, requested.String(), capacity.String())
		return assessment
	}
	for _, condition := range pvc.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			assessment.Reason = storageReasonPVCProvisioning
			assessment.Message = fmt.Sprintf("PVC %s/%s still reports condition %s=%s; do not schedule workers until expansion/provisioning settles", pool.Namespace, pvcName, condition.Type, condition.Status)
			return assessment
		}
	}

	var pv corev1.PersistentVolume
	if err := r.Get(ctx, types.NamespacedName{Name: assessment.PVName}, &pv); err != nil {
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = fmt.Sprintf("bound PVC %s/%s references unavailable PV %q: %v", pool.Namespace, pvcName, assessment.PVName, err)
		return assessment
	}
	if pv.Status.Phase != corev1.VolumeBound || pv.Spec.StorageClassName != assessment.ClassName || pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain || !containsAccessMode(pv.Spec.AccessModes, corev1.ReadWriteMany) || pv.Spec.CSI == nil {
		assessment.Reason = storageReasonPVCUnavailable
		assessment.Message = fmt.Sprintf("PV %s must remain Bound, class=%s, RWX Filesystem CSI, and Retain (observed phase=%s class=%s reclaim=%s)", pv.Name, assessment.ClassName, pv.Status.Phase, pv.Spec.StorageClassName, pv.Spec.PersistentVolumeReclaimPolicy)
		return assessment
	}
	pvCapacity := pv.Spec.Capacity[corev1.ResourceStorage]
	if pvCapacity.Cmp(requested) < 0 {
		assessment.Reason = storageReasonCapacity
		assessment.Message = fmt.Sprintf("PV %s capacity %s is below PVC request %s; prove physical headroom and complete expansion", pv.Name, pvCapacity.String(), requested.String())
		return assessment
	}
	assessment.VolumeHandle = pv.Spec.CSI.VolumeHandle
	if assessment.Mode == storageModeManagedLonghorn {
		if pv.Spec.CSI.Driver != managedLonghornProvisioner || assessment.VolumeHandle == "" {
			assessment.Reason = storageReasonClassMismatch
			assessment.Message = fmt.Sprintf("managed PV %s must use CSI driver %s and a non-empty volume handle", pv.Name, managedLonghornProvisioner)
			return assessment
		}
		if failure := r.managedLonghornVolumeHealth(ctx, pool, assessment.VolumeHandle, false); failure != nil {
			failure.Mode = assessment.Mode
			failure.ClassName = assessment.ClassName
			failure.PVName = assessment.PVName
			failure.VolumeHandle = assessment.VolumeHandle
			return *failure
		}
	}
	assessment.Ready = true
	assessment.Reason = storageReasonReady
	assessment.Message = fmt.Sprintf("StorageReady: class=%s pvc=%s/%s pv=%s mode=%s conformance=%s", assessment.ClassName, pool.Namespace, pvcName, assessment.PVName, assessment.Mode, conformance["validatedAt"])
	return assessment
}

func (r *AgentPoolReconciler) storageContract(ctx context.Context, pool *v1alpha1.AgentPool) (map[string]string, *agentPoolStorageAssessment) {
	var contracts corev1.ConfigMapList
	if err := r.List(ctx, &contracts, client.InNamespace(pool.Namespace), client.MatchingLabels{storageContractLabel: "true"}); err != nil {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformanceFailed, Message: fmt.Sprintf("read installation RWX storage contract in namespace %s: %v", pool.Namespace, err)}
	}
	if len(contracts.Items) == 0 {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformancePending, Message: fmt.Sprintf("no chart-owned RWX storage contract exists in namespace %s; reconcile the platform chart before AgentPools", pool.Namespace)}
	}
	if len(contracts.Items) != 1 {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformanceFailed, Message: fmt.Sprintf("namespace %s has %d RWX storage contracts; retain exactly one platform release authority", pool.Namespace, len(contracts.Items))}
	}
	data := contracts.Items[0].Data
	if data["contractVersion"] != storageContractVersion {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformanceFailed, Message: fmt.Sprintf("RWX storage contract %s/%s has unsupported version %q; require %s", pool.Namespace, contracts.Items[0].Name, data["contractVersion"], storageContractVersion)}
	}
	if data["mode"] != storageModeManagedLonghorn && data["mode"] != storageModeExternal {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformanceFailed, Message: fmt.Sprintf("RWX storage contract %s/%s has invalid mode %q", pool.Namespace, contracts.Items[0].Name, data["mode"])}
	}
	return data, nil
}

func (r *AgentPoolReconciler) storageConformance(ctx context.Context, class *storagev1.StorageClass) (map[string]string, *agentPoolStorageAssessment) {
	var attestations corev1.ConfigMapList
	if err := r.List(ctx, &attestations, client.MatchingLabels{storageConformanceLabel: "true"}); err != nil {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformanceFailed, Message: fmt.Sprintf("read RWX conformance attestations for StorageClass %q: %v", class.Name, err)}
	}
	var stale []string
	for i := range attestations.Items {
		attestation := &attestations.Items[i]
		if attestation.Data["storageClassName"] != class.Name {
			continue
		}
		if attestation.Data["contractVersion"] == storageContractVersion &&
			attestation.Data["storageClassUID"] == string(class.UID) &&
			attestation.Data["provisioner"] == class.Provisioner &&
			attestation.Data["result"] == "pass" {
			return attestation.Data, nil
		}
		stale = append(stale, attestation.Namespace+"/"+attestation.Name)
	}
	if len(stale) > 0 {
		return nil, &agentPoolStorageAssessment{Reason: storageReasonConformanceFailed, Message: fmt.Sprintf("StorageClass %q has stale/mismatched conformance evidence %s; rerun the same-release disposable gate against class UID %s", class.Name, strings.Join(stale, ","), class.UID)}
	}
	return nil, &agentPoolStorageAssessment{Reason: storageReasonConformancePending, Message: fmt.Sprintf("StorageClass %q has no %s live conformance attestation bound to class UID %s; run docs/architecture/validation/hor-424-rwx-conformance.sh", class.Name, storageContractVersion, class.UID)}
}

func (r *AgentPoolReconciler) managedLonghornVolumeHealth(ctx context.Context, pool *v1alpha1.AgentPool, volumeHandle string, requireAttached bool) *agentPoolStorageAssessment {
	volume := &unstructured.Unstructured{}
	volume.SetAPIVersion("longhorn.io/v1beta2")
	volume.SetKind("Volume")
	if err := r.Get(ctx, types.NamespacedName{Namespace: managedLonghornNamespace, Name: volumeHandle}, volume); err != nil {
		return &agentPoolStorageAssessment{Reason: storageReasonBackendDegraded, Message: fmt.Sprintf("Longhorn volume %s/%s is unavailable: %v; inspect Longhorn volume, engine, replica, node/disk, and CSI events", managedLonghornNamespace, volumeHandle, err)}
	}
	robustness, _, _ := unstructured.NestedString(volume.Object, "status", "robustness")
	state, _, _ := unstructured.NestedString(volume.Object, "status", "state")
	if robustness == "unknown" && state == "detached" && !storageWasOperationallyReady(pool) {
		return &agentPoolStorageAssessment{
			CanMount: true,
			Reason:   storageReasonInitialConvergence,
			Message: fmt.Sprintf(
				"Longhorn volume %s/%s is in initial convergence with robustness=%q state=%q; retain the desired workers to drive first attachment while AgentPool readiness stays closed until the backend, share-manager, and workers are Ready",
				managedLonghornNamespace, volumeHandle, robustness, state,
			),
		}
	}
	if robustness != "healthy" {
		return &agentPoolStorageAssessment{Reason: storageReasonBackendDegraded, Message: fmt.Sprintf("Longhorn volume %s/%s robustness=%q state=%q; restore replica/node/disk capacity before replacing workers", managedLonghornNamespace, volumeHandle, robustness, state)}
	}
	if requireAttached && state != "attached" {
		return &agentPoolStorageAssessment{Reason: storageReasonRecoveryPending, Message: fmt.Sprintf("Longhorn volume %s/%s is healthy but state=%q; wait for a fresh worker attachment before scheduling", managedLonghornNamespace, volumeHandle, state)}
	}
	if requireAttached && state == "attached" && !r.shareManagerReady(ctx, volumeHandle) {
		return &agentPoolStorageAssessment{Reason: storageReasonShareManagerDown, Message: fmt.Sprintf("Longhorn share-manager for volume %s is unavailable; restore backend/share-manager health, then use fresh workers without replay", volumeHandle)}
	}
	return nil
}

func (r *AgentPoolReconciler) shareManagerReady(ctx context.Context, volumeHandle string) bool {
	var pods corev1.PodList
	selector := labels.SelectorFromSet(labels.Set{longhornShareManagerComponentKey: longhornShareManagerComponent})
	if err := r.List(ctx, &pods, client.InNamespace(managedLonghornNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return false
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Name != "share-manager-"+volumeHandle && pod.Labels["longhorn.io/share-manager"] != volumeHandle {
			continue
		}
		if podIsReady(pod) {
			return true
		}
	}
	return false
}

func containsAccessMode(modes []corev1.PersistentVolumeAccessMode, wanted corev1.PersistentVolumeAccessMode) bool {
	for _, mode := range modes {
		if mode == wanted {
			return true
		}
	}
	return false
}

// storageWasOperationallyReady distinguishes an established pool from initial
// storage convergence. OperationalReadinessReached is a durable latch: once a
// worker has been Ready with healthy storage, unrelated transient status updates
// must not disarm the post-readiness fail-closed replacement path. The aggregate
// fallback arms pre-HOR-523 pools on their next status patch.
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

// storageWorkerReplacementPending distinguishes affected clients that must stay
// quiesced from the fresh worker set that is allowed to drive a recovered RWX
// volume from detached to attached before it can report Ready.
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
				return fmt.Sprintf("worker %s/%s container %s exited during mount-root validation (reason=%s message=%s); inspect pod mount events and refuse root-squashed/foreign-owner/unsafe-mode storage", pod.Namespace, pod.Name, status.Name, status.State.Terminated.Reason, status.State.Terminated.Message)
			}
			if status.State.Waiting != nil {
				reason := status.State.Waiting.Reason
				if reason == "CreateContainerError" || reason == "CrashLoopBackOff" || reason == "RunContainerError" {
					return fmt.Sprintf("worker %s/%s container %s cannot validate/mount storage (reason=%s message=%s); inspect PVC, CSI mount, root ownership/mode, and node events", pod.Namespace, pod.Name, status.Name, reason, status.State.Waiting.Message)
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
