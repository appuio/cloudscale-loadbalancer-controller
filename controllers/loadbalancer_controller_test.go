package controllers

import (
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

func Test_ClusterVersionReconciler_Reconcile(t *testing.T) {
	ctx := log.IntoContext(t.Context(), testr.New(t))

	lb := &cloudscalev1beta1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-lb",
			Namespace: "default",
		},
		Spec: cloudscalev1beta1.LoadBalancerSpec{},
	}

	c := controllerClient(t, lb)
	reconciler := &LoadBalancerReconciler{
		Client: c,
		Scheme: c.Scheme(),
	}

	_, err := reconciler.Reconcile(ctx, requestForObject(lb))
	require.NoError(t, err, "Reconcile should not return an error")
}

func controllerClient(t *testing.T, initObjs ...client.Object) client.WithWatch {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, cloudscalev1beta1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(initObjs...).
		WithStatusSubresource(
			&cloudscalev1beta1.LoadBalancer{},
		).
		Build()
}

func requestForObject(o client.Object) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      o.GetName(),
			Namespace: o.GetNamespace(),
		},
	}
}
