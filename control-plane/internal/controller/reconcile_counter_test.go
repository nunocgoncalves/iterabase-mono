package controller

import (
	"context"
	"sync/atomic"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// primaryGetCounter counts Reconcile invocations for one primary CR: every
// reconcile starts by loading that object, and the reconcilers under test load
// their primary kind nowhere else. It lets the HOR-559 regression tests observe
// enqueues without instrumenting production code.
type primaryGetCounter[T client.Object] struct {
	client.Client
	name  string
	count atomic.Int64
}

func (c *primaryGetCounter[T]) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(T); ok && key.Name == c.name {
		c.count.Add(1)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
