package controllers

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

func Test_LoadBalancerStatusCollector(t *testing.T) {
	emptyLB := &cloudscalev1beta1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-lb",
			Namespace: "default",
		},
	}
	activeLB := &cloudscalev1beta1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "active-lb",
			Namespace: "default",
		},
		Status: cloudscalev1beta1.LoadBalancerStatus{
			CloudscaleStatus: "active",
			Pools: []cloudscalev1beta1.StatusPool{
				{
					Name: "empty-pool",
				},
				{
					Name: "active-pool",
					Backends: []cloudscalev1beta1.StatusBackend{
						{
							NodeName:   "node1",
							ServerName: "node1-server",
							Address:    "1.2.3.4",
							Status:     "up",
						}, {
							NodeName:   "node2",
							ServerName: "node2-server",
							Address:    "1.2.3.5",
							Status:     "down",
						}, {
							Address: "1.2.3.6",
							Status:  "up",
						},
					},
				},
			},
		},
	}
	degradedLB := &cloudscalev1beta1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "degraded-lb",
			Namespace: "default",
		},
		Status: cloudscalev1beta1.LoadBalancerStatus{
			CloudscaleStatus: "degraded",
		},
	}

	c := fakeClient(t, emptyLB, activeLB, degradedLB)
	subject := &LoadBalancerStatusCollector{
		Client: c,
	}

	metrics := `
# HELP cloudscale_loadbalancer_controller_loadbalancer_backend_status The current status of the load balancer backend as reported by cloudscale.
# TYPE cloudscale_loadbalancer_controller_loadbalancer_backend_status gauge
cloudscale_loadbalancer_controller_loadbalancer_backend_status{address="1.2.3.4",loadbalancer="active-lb",namespace="default",node="node1",pool="active-pool",server="node1-server",status="up"} 1
cloudscale_loadbalancer_controller_loadbalancer_backend_status{address="1.2.3.5",loadbalancer="active-lb",namespace="default",node="node2",pool="active-pool",server="node2-server",status="down"} 1
cloudscale_loadbalancer_controller_loadbalancer_backend_status{address="1.2.3.6",loadbalancer="active-lb",namespace="default",node="",pool="active-pool",server="",status="up"} 1
# HELP cloudscale_loadbalancer_controller_loadbalancer_backend_status_up 1 if the load balancer backend is 'up', 0 otherwise. Check "cloudscale_loadbalancer_controller_loadbalancer_backend_status" for current status.
# TYPE cloudscale_loadbalancer_controller_loadbalancer_backend_status_up gauge
cloudscale_loadbalancer_controller_loadbalancer_backend_status_up{address="1.2.3.4",loadbalancer="active-lb",namespace="default",node="node1",pool="active-pool",server="node1-server"} 1
cloudscale_loadbalancer_controller_loadbalancer_backend_status_up{address="1.2.3.5",loadbalancer="active-lb",namespace="default",node="node2",pool="active-pool",server="node2-server"} 0
cloudscale_loadbalancer_controller_loadbalancer_backend_status_up{address="1.2.3.6",loadbalancer="active-lb",namespace="default",node="",pool="active-pool",server=""} 1
# HELP cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status The current status of the load balancer as reported by cloudscale.
# TYPE cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status gauge
cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status{loadbalancer="active-lb",namespace="default",status="active"} 1
cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status{loadbalancer="degraded-lb",namespace="default",status="degraded"} 1
cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status{loadbalancer="empty-lb",namespace="default",status=""} 1
# HELP cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status_active 1 if the load balancer is 'active', 0 otherwise. Check "cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status" for current status.
# TYPE cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status_active gauge
cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status_active{loadbalancer="active-lb",namespace="default"} 1
cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status_active{loadbalancer="degraded-lb",namespace="default"} 0
cloudscale_loadbalancer_controller_loadbalancer_cloudscale_status_active{loadbalancer="empty-lb",namespace="default"} 0
`

	require.NoError(t,
		testutil.CollectAndCompare(subject, strings.NewReader(metrics)),
	)

}

func fakeClient(t *testing.T, initObjs ...client.Object) client.WithWatch {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, cloudscalev1beta1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(initObjs...).
		WithStatusSubresource(
			&cloudscalev1beta1.LoadBalancer{},
		).
		Build()
}
