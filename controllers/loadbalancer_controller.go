package controllers

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

// LoadBalancerReconciler reconciles a LoadBalancer object
type LoadBalancerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers/finalizers,verbs=update

// Reconcile compares the LoadBalancer object with the upstream LoadBalancer object and updates the upstream LoadBalancer object if necessary.
func (r *LoadBalancerReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return reconcile.Result{}, errors.New("not implemented")
}

// SetupWithManager sets up the controller with the Manager.
func (r *LoadBalancerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudscalev1beta1.LoadBalancer{}).
		Complete(r)
}
