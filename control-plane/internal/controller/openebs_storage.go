package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const managedOpenEBSVolumeGroup = "iterabase-data"

type managedOpenEBSVolumeObservation struct {
	Namespace    string
	Name         string
	Shared       string
	State        string
	OwnerNode    string
	CapacityText string
	Capacity     resource.Quantity
}

type managedOpenEBSObservationIssue struct {
	Message string
	Unknown bool
	Err     error
}

// observeManagedOpenEBSVolume parses and validates the identity shared by all
// controller-owned OpenEBS LVM claims. Callers retain policy for shared mode,
// desired capacity, and readiness during an in-progress resize.
//
//nolint:gocyclo // each OpenEBS object/field identity predicate has a distinct actionable failure.
func observeManagedOpenEBSVolume(ctx context.Context, reader client.Reader, namespace string, pv *corev1.PersistentVolume) (managedOpenEBSVolumeObservation, *managedOpenEBSObservationIssue) {
	observation := managedOpenEBSVolumeObservation{}
	volumeHandle := ""
	if pv.Spec.CSI != nil {
		volumeHandle = pv.Spec.CSI.VolumeHandle
	}
	volume := &unstructured.Unstructured{}
	volume.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMVolume"})
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: volumeHandle}, volume); err != nil {
		return observation, &managedOpenEBSObservationIssue{
			Message: fmt.Sprintf("PV %s OpenEBS LVMVolume %s is unavailable: %v", pv.Name, volumeHandle, err),
			Unknown: !errors.IsNotFound(err),
			Err:     errorUnlessNotFound(err),
		}
	}

	volGroup, _, _ := unstructured.NestedString(volume.Object, "spec", "volGroup")
	vgPattern, _, _ := unstructured.NestedString(volume.Object, "spec", "vgPattern")
	shared, _, _ := unstructured.NestedString(volume.Object, "spec", "shared")
	thin, _, _ := unstructured.NestedString(volume.Object, "spec", "thinProvision")
	ownerNode, _, _ := unstructured.NestedString(volume.Object, "spec", "ownerNodeID")
	state, _, _ := unstructured.NestedString(volume.Object, "status", "state")
	capacityText, _, _ := unstructured.NestedString(volume.Object, "spec", "capacity")
	capacity, capacityErr := resource.ParseQuantity(capacityText)
	observation = managedOpenEBSVolumeObservation{
		Namespace: volume.GetNamespace(), Name: volume.GetName(), Shared: shared, State: state,
		OwnerNode: ownerNode, CapacityText: capacityText, Capacity: capacity,
	}
	if volGroup != managedOpenEBSVolumeGroup || vgPattern != "^iterabase-data$" || thin != "no" || ownerNode == "" || capacityErr != nil {
		return observation, &managedOpenEBSObservationIssue{Message: fmt.Sprintf(
			"LVMVolume %s/%s must retain thick vg=%s pattern=^iterabase-data$ with one owner node and readable capacity (observed state=%s vg=%s pattern=%s shared=%s thin=%s node=%s capacity=%s)",
			volume.GetNamespace(), volume.GetName(), managedOpenEBSVolumeGroup, state, volGroup, vgPattern, shared, thin, ownerNode, capacityText,
		)}
	}
	if !pvHasExactNodeTopology(pv, ownerNode) {
		return observation, &managedOpenEBSObservationIssue{Message: fmt.Sprintf("PV %s node topology does not match LVMVolume owner node %s", pv.Name, ownerNode)}
	}

	node := &unstructured.Unstructured{}
	node.SetGroupVersionKind(schema.GroupVersionKind{Group: "local.openebs.io", Version: "v1alpha1", Kind: "LVMNode"})
	if err := reader.Get(ctx, types.NamespacedName{Namespace: volume.GetNamespace(), Name: ownerNode}, node); err != nil {
		return observation, &managedOpenEBSObservationIssue{
			Message: fmt.Sprintf("OpenEBS LVMNode %s is unavailable in namespace %s: %v", ownerNode, volume.GetNamespace(), err),
			Unknown: !errors.IsNotFound(err),
			Err:     errorUnlessNotFound(err),
		}
	}
	groups, found, err := unstructured.NestedSlice(node.Object, "volumeGroups")
	if err != nil || !found {
		return observation, &managedOpenEBSObservationIssue{Message: fmt.Sprintf("LVMNode %s/%s has no readable volumeGroups", node.GetNamespace(), ownerNode)}
	}
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok || group["name"] != managedOpenEBSVolumeGroup {
			continue
		}
		uuid, _ := group["uuid"].(string)
		thinPools, _ := group["thinPools"].([]any)
		if uuid == "" || nestedNumber(group["missingPvCount"]) != 0 || len(thinPools) != 0 {
			return observation, &managedOpenEBSObservationIssue{Message: fmt.Sprintf("LVMNode %s/%s VG %s must have a UUID, no missing PVs, and no thin pools", node.GetNamespace(), ownerNode, managedOpenEBSVolumeGroup)}
		}
		return observation, nil
	}
	return observation, &managedOpenEBSObservationIssue{Message: fmt.Sprintf("LVMNode %s/%s does not report VG %s", node.GetNamespace(), ownerNode, managedOpenEBSVolumeGroup)}
}

func errorUnlessNotFound(err error) error {
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// OpenEBS publishes PV accessible topology with its driver key; the additional
// allowed hostname key is a CSINode registration capability, not a PV term.
func pvHasExactNodeTopology(pv *corev1.PersistentVolume, node string) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return false
	}
	terms := pv.Spec.NodeAffinity.Required.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchFields) != 0 || len(terms[0].MatchExpressions) != 1 {
		return false
	}
	expression := terms[0].MatchExpressions[0]
	return expression.Key == "openebs.io/nodename" && expression.Operator == corev1.NodeSelectorOpIn &&
		len(expression.Values) == 1 && expression.Values[0] == node
}

func nestedNumber(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	default:
		return -1
	}
}
