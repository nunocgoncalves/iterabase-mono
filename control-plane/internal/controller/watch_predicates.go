package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// generationChangedPredicates filters a primary watch to spec
// (metadata.generation) changes. Status-only updates — including the
// controller's own status write — must not enqueue a reconcile, otherwise an
// unconditional status write re-triggers the controller through its own watch
// (HOR-559).
//
// Cross-resource and owned-object watches that carry the convergence signal stay
// unfiltered (for example the ModelBackend → Model map watch, or
// Owns(Deployment) for ModelBackend). A controller that filters its primary
// watch must keep a bounded fallback requeue on every path that owns observed
// status which cannot be re-derived from the spec alone (workload health,
// managed storage convergence, external-backend static status). Paths whose
// status is a deterministic function of the spec — stub kinds and structural
// validation rejections — intentionally rely on the next spec event instead of
// retrying unchanged input every health interval.
func generationChangedPredicates() []predicate.Predicate {
	return []predicate.Predicate{predicate.GenerationChangedPredicate{}}
}
