package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
)

// TestGenerationChangedPredicates pins the HOR-559 watch contract shared by the
// Model and ModelBackend controllers: status-only updates (including the
// controller's own status writes) are filtered, while create/delete and spec
// (generation) changes still enqueue a reconcile.
func TestGenerationChangedPredicates(t *testing.T) {
	t.Run("Model", func(t *testing.T) {
		old := &v1alpha1.Model{
			ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default", Generation: 3},
			Status:     v1alpha1.ModelStatus{Available: true, ObservedGeneration: 3},
		}
		statusOnly := old.DeepCopy()
		statusOnly.Status.Available = false
		statusOnly.Status.Message = "changed"
		statusOnly.Status.LastChecked = &metav1.Time{Time: time.Now()}
		assertGenerationWatchContract(t, old, statusOnly)
	})

	t.Run("ModelBackend", func(t *testing.T) {
		old := &v1alpha1.ModelBackend{
			ObjectMeta: metav1.ObjectMeta{Name: "mb", Namespace: "default", Generation: 2},
			Status:     v1alpha1.ModelBackendStatus{Deployed: true, Healthy: true, ObservedGeneration: 2},
		}
		statusOnly := old.DeepCopy()
		statusOnly.Status.Healthy = false
		statusOnly.Status.LastReconciled = &metav1.Time{Time: time.Now()}
		assertGenerationWatchContract(t, old, statusOnly)
	})
}

// assertGenerationWatchContract asserts the HOR-559 predicates: creates and
// deletes enqueue, status-only updates do not, and a generation change does.
func assertGenerationWatchContract(t *testing.T, old, statusOnly client.Object) {
	t.Helper()
	specChange := old.DeepCopyObject().(client.Object)
	specChange.SetGeneration(old.GetGeneration() + 1)
	for _, p := range generationChangedPredicates() {
		assert.True(t, p.Create(event.CreateEvent{Object: old}), "create events must enqueue")
		assert.True(t, p.Delete(event.DeleteEvent{Object: old}), "delete events must enqueue")
		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: statusOnly}),
			"status-only updates must not enqueue a reconcile (HOR-559)")
		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: specChange}),
			"generation changes must enqueue a reconcile")
	}
}
