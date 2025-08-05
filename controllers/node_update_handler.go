package controllers

import (
	"context"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

// nodeUpdateHandler requeues all LoadBalancer objects when a node's labels or providerID might have changed.
// Prevents the controller from hammering the Cloudscale API with requests for every node change (which there are plenty of).
type nodeUpdateHandler struct {
	client client.Client
}

func (h nodeUpdateHandler) Create(ctx context.Context, ev event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No need to requeue all LoadBalancer on the initial list. All LoadBalancers will be queued anyways on startup.
	if ev.IsInInitialList {
		return
	}

	h.queueAllLoadBalancers(ctx, q)
}

// Update only requeues all LoadBalancer objects if the labels of a node, or its providerID, have changed.
func (h nodeUpdateHandler) Update(ctx context.Context, ev event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	o := ev.ObjectOld.(*corev1.Node)
	n := ev.ObjectNew.(*corev1.Node)
	if o.Spec.ProviderID == n.Spec.ProviderID && maps.Equal(o.GetLabels(), n.GetLabels()) {
		return
	}

	h.queueAllLoadBalancers(ctx, q)
}

func (h nodeUpdateHandler) Delete(ctx context.Context, ev event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueAllLoadBalancers(ctx, q)
}

func (h nodeUpdateHandler) Generic(ctx context.Context, ev event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueAllLoadBalancers(ctx, q)
}

func (h nodeUpdateHandler) queueAllLoadBalancers(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	var lbs cloudscalev1beta1.LoadBalancerList
	if err := h.client.List(ctx, &lbs); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list LoadBalancers")
		return
	}

	for _, lb := range lbs.Items {
		q.Add(reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: lb.Namespace,
				Name:      lb.Name,
			},
		})
	}
}
