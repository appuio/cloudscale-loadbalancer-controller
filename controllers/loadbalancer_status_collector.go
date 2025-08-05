package controllers

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers,verbs=get;list;watch

const statusMetricName = MetricsNamespace + "_loadbalancer_cloudscale_status"

var loadBalancerStatusDesc = prometheus.NewDesc(
	statusMetricName,
	"The current status of the load balancer as reported by cloudscale.",
	[]string{
		"namespace",
		"loadbalancer",
		"status",
	},
	nil,
)

var loadBalancerActiveDesc = prometheus.NewDesc(
	MetricsNamespace+"_loadbalancer_cloudscale_status_active",
	fmt.Sprintf("1 if the load balancer is 'active', 0 otherwise. Check %q for current status.", statusMetricName),
	[]string{
		"namespace",
		"loadbalancer",
	},
	nil,
)

const backendStatusMetricName = MetricsNamespace + "_loadbalancer_backend_status"

var loadBalancerBackendStatusDesc = prometheus.NewDesc(
	backendStatusMetricName,
	"The current status of the load balancer backend as reported by cloudscale.",
	[]string{
		"namespace",
		"loadbalancer",
		"pool",
		"node",
		"server",
		"address",
		"status",
	},
	nil,
)

var loadBalancerBackendUpDesc = prometheus.NewDesc(
	MetricsNamespace+"_loadbalancer_backend_status_up",
	fmt.Sprintf("1 if the load balancer backend is 'up', 0 otherwise. Check %q for current status.", backendStatusMetricName),
	[]string{
		"namespace",
		"loadbalancer",
		"pool",
		"node",
		"server",
		"address",
	},
	nil,
)

// LoadBalancerStatusCollector is a Prometheus collector that exposes various metrics about the upgrade process.
type LoadBalancerStatusCollector struct {
	client.Client
}

var _ prometheus.Collector = &LoadBalancerStatusCollector{}

// Describe implements prometheus.Collector.
// Sends the static description of the metrics to the provided channel.
func (*LoadBalancerStatusCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- loadBalancerStatusDesc
	ch <- loadBalancerActiveDesc
	ch <- loadBalancerBackendStatusDesc
	ch <- loadBalancerBackendUpDesc
}

// Collect implements prometheus.Collector.
// Sends a metric if the cluster is currently upgrading and an upgrading metric for each machine config pool.
// It also collects job states and whether they have matching disruptive hooks.
func (m *LoadBalancerStatusCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()

	var lbs cloudscalev1beta1.LoadBalancerList
	if err := m.Client.List(ctx, &lbs); err != nil {
		err := fmt.Errorf("failed to list load balancers: %w", err)
		ch <- prometheus.NewInvalidMetric(loadBalancerStatusDesc, err)
		ch <- prometheus.NewInvalidMetric(loadBalancerActiveDesc, err)
		ch <- prometheus.NewInvalidMetric(loadBalancerBackendStatusDesc, err)
		ch <- prometheus.NewInvalidMetric(loadBalancerBackendUpDesc, err)
	}
	for _, lb := range lbs.Items {
		ch <- prometheus.MustNewConstMetric(
			loadBalancerStatusDesc,
			prometheus.GaugeValue,
			1,
			lb.Namespace,
			lb.Name,
			lb.Status.CloudscaleStatus,
		)
		ch <- prometheus.MustNewConstMetric(
			loadBalancerActiveDesc,
			prometheus.GaugeValue,
			boolToFloat64(lb.Status.CloudscaleStatus == "active"),
			lb.Namespace,
			lb.Name,
		)
		for _, pool := range lb.Status.Pools {
			for _, backend := range pool.Backends {
				ch <- prometheus.MustNewConstMetric(
					loadBalancerBackendStatusDesc,
					prometheus.GaugeValue,
					1,
					lb.Namespace,
					lb.Name,
					pool.Name,
					backend.NodeName,
					backend.ServerName,
					backend.Address,
					backend.Status,
				)
				ch <- prometheus.MustNewConstMetric(
					loadBalancerBackendUpDesc,
					prometheus.GaugeValue,
					boolToFloat64(backend.Status == "up"),
					lb.Namespace,
					lb.Name,
					pool.Name,
					backend.NodeName,
					backend.ServerName,
					backend.Address,
				)
			}
		}
	}
}

func boolToFloat64(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
