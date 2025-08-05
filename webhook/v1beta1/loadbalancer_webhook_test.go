package v1beta1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
	webhookv1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/webhook/v1beta1"
)

type validatorCreateTestCase struct {
	name     string
	newObj   runtime.Object
	matchErr []string
}

var createTestCases = []validatorCreateTestCase{
	{
		name: "valid LoadBalancer creation",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				VirtualIPAddresses: []cloudscalev1beta1.VirtualIPAddress{
					{
						Address:  "192.0.2.1",
						SubnetID: "7BEB269D-5133-4459-93F0-72970CD3B189",
					},
				},
				FloatingIPAddresses: []cloudscalev1beta1.FloatingIPAddress{
					{
						CIDR: "203.0.113.1/32",
					},
				},
				UUID: "934B8C1C-5D8C-4AE2-8301-8A6459CEC2D0",
				Zone: "lpg1",
				Pools: []cloudscalev1beta1.Pool{
					{
						Name: "default",
						Frontend: cloudscalev1beta1.PoolFrontend{
							Protocol: "TCP",
							Port:     80,
							AllowedCIDRs: []cloudscalev1beta1.AllowedCIDR{
								{CIDR: "203.0.13.0/24"},
								{CIDR: "2001:db8::/32"},
							},
						},
						Backend: cloudscalev1beta1.PoolBackend{
							HealthMonitor: cloudscalev1beta1.HealthMonitor{
								HTTP: cloudscalev1beta1.HTTPHealthMonitor{
									StatusCodes: []string{"200-299"},
								},
							},
						},
					},
				},
			},
		},
	},
	{
		name: "Floating IP address already in use",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				FloatingIPAddresses: []cloudscalev1beta1.FloatingIPAddress{
					{
						CIDR: "7.7.7.7/32",
					},
				},
			},
		},
		matchErr: []string{
			".spec.floatingIPAddresses[0].cidr:",
			"7.7.7.7/32",
			"assigned to another LoadBalancer",
		},
	},
	{
		name: "Floating IP address invalid format",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				FloatingIPAddresses: []cloudscalev1beta1.FloatingIPAddress{
					{
						CIDR: "1.2.3.4",
					},
				},
			},
		},
		matchErr: []string{
			".spec.floatingIPAddresses[0].cidr:",
			"1.2.3.4",
			"Invalid",
		},
	},
	{
		name: "Floating IP address invalid format",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				FloatingIPAddresses: []cloudscalev1beta1.FloatingIPAddress{
					{
						CIDR: "2001:db8::ff00:42:8329:/128",
					},
				},
			},
		},
		matchErr: []string{
			".spec.floatingIPAddresses[0].cidr:",
			"2001:db8::ff00:42:8329:/128",
			"Invalid",
		},
	},
	{
		name: "Virtual IP address invalid format",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				VirtualIPAddresses: []cloudscalev1beta1.VirtualIPAddress{
					{
						Address:  "nope",
						SubnetID: "7BEB269D-5133-4459-93F0-72970CD3B189",
					},
				},
			},
		},
		matchErr: []string{
			".spec.virtualIPAddresses[0].address:",
			"nope",
			"Invalid",
		},
	},
	{
		name: "Invalid status code format",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Backend: cloudscalev1beta1.PoolBackend{
							HealthMonitor: cloudscalev1beta1.HealthMonitor{
								HTTP: cloudscalev1beta1.HTTPHealthMonitor{
									StatusCodes: []string{"notacode"},
								},
							},
						},
					},
				},
			},
		},
		matchErr: []string{
			"spec.pools[0].backend.healthCheck.http.statusCodes[0]:",
			"notacode",
			"Invalid status code",
		},
	},
	{
		name: "Status code range with multiple codes",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Backend: cloudscalev1beta1.PoolBackend{
							HealthMonitor: cloudscalev1beta1.HealthMonitor{
								HTTP: cloudscalev1beta1.HTTPHealthMonitor{
									StatusCodes: []string{"200-299", "404"},
								},
							},
						},
					},
				},
			},
		},
		matchErr: []string{
			"spec.pools[0].backend.healthCheck.http.statusCodes[0]:",
			"Status code ranges are not allowed when multiple status codes are specified",
		},
	},
	{
		name: "Multiple status codes are valid",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Backend: cloudscalev1beta1.PoolBackend{
							HealthMonitor: cloudscalev1beta1.HealthMonitor{
								HTTP: cloudscalev1beta1.HTTPHealthMonitor{
									StatusCodes: []string{"200", "201", "202"},
								},
							},
						},
					},
				},
			},
		},
	},
	{
		name: "Single status code range is valid",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Backend: cloudscalev1beta1.PoolBackend{
							HealthMonitor: cloudscalev1beta1.HealthMonitor{
								HTTP: cloudscalev1beta1.HTTPHealthMonitor{
									StatusCodes: []string{"200-299"},
								},
							},
						},
					},
				},
			},
		},
	},
	{
		name: "Allowed CIDR invalid format",
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Frontend: cloudscalev1beta1.PoolFrontend{
							AllowedCIDRs: []cloudscalev1beta1.AllowedCIDR{
								{CIDR: "203.0.13.0/24"},
								{CIDR: "invalid-cidr"},
							},
						},
					},
				},
			},
		},
		matchErr: []string{
			".spec.pools[0].frontend.allowedCIDRs[1]",
			"invalid-cidr",
			"Invalid allowed CIDR",
		},
	},
}

type validatorUpdateTestCase struct {
	name     string
	oldObj   runtime.Object
	newObj   runtime.Object
	matchErr []string
}

var updateTestCases = []validatorUpdateTestCase{
	{
		name: "valid LoadBalancer update",
		oldObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Frontend: cloudscalev1beta1.PoolFrontend{
							AllowedCIDRs: []cloudscalev1beta1.AllowedCIDR{
								{CIDR: "203.0.113.0/24"},
							},
						},
					},
				},
			},
		},
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Pools: []cloudscalev1beta1.Pool{
					{
						Frontend: cloudscalev1beta1.PoolFrontend{
							AllowedCIDRs: []cloudscalev1beta1.AllowedCIDR{
								{CIDR: "203.0.113.0/24"},
								{CIDR: "203.1.113.0/24"},
							},
						},
					},
				},
			},
		},
		matchErr: []string{},
	},
	{
		name: "UUID can be set on update if not set before",
		oldObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{},
		},
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				UUID: "12345678-1234-5678-1234-56789abcdef01",
			},
		},
	},
	{
		name: "invalid LoadBalancer update with changed UUID",
		oldObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				UUID: "934B8C1C-5D8C-4AE2-8301-8A6459CEC2D0",
			},
		},
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				UUID: "12345678-1234-5678-1234-56789abcdef01",
			},
		},
		matchErr: []string{
			".spec.uuid:",
			"UUID cannot be changed once set",
		},
	},
	{
		name: "zone cannot be changed on update",
		oldObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Zone: "zone-1",
			},
		},
		newObj: &cloudscalev1beta1.LoadBalancer{
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				Zone: "zone-2",
			},
		},
		matchErr: []string{
			".spec.zone:",
			"Zone cannot be changed once set",
		},
	},
}

var existingLoadBalancer = &cloudscalev1beta1.LoadBalancer{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "used-floating-ip",
		Namespace: "default",
	},
	Spec: cloudscalev1beta1.LoadBalancerSpec{
		FloatingIPAddresses: []cloudscalev1beta1.FloatingIPAddress{
			{
				CIDR: "7.7.7.7/32",
			},
		},
	},
}

func Test_LoadBalancerCustomValidator_ValidateCreate(t *testing.T) {
	c := buildFakeClient(t, existingLoadBalancer.DeepCopy())

	for _, tc := range createTestCases {
		t.Run(tc.name, func(t *testing.T) {
			validator := &webhookv1beta1.LoadBalancerCustomValidator{
				Client: c,
			}
			_, err := validator.ValidateCreate(t.Context(), tc.newObj)
			if len(tc.matchErr) > 0 {
				for _, match := range tc.matchErr {
					assert.ErrorContains(t, err, match)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func Test_LoadBalancerCustomValidator_ValidateUpdate(t *testing.T) {
	c := buildFakeClient(t, existingLoadBalancer.DeepCopy())

	tcs := make([]validatorUpdateTestCase, 0, len(updateTestCases)+len(createTestCases))
	for _, tc := range createTestCases {
		tcs = append(tcs, validatorUpdateTestCase{
			name:     tc.name,
			newObj:   tc.newObj,
			oldObj:   tc.newObj,
			matchErr: tc.matchErr,
		})
	}
	tcs = append(tcs, updateTestCases...)

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			validator := &webhookv1beta1.LoadBalancerCustomValidator{
				Client: c,
			}
			_, err := validator.ValidateUpdate(t.Context(), tc.oldObj, tc.newObj)
			if len(tc.matchErr) > 0 {
				for _, match := range tc.matchErr {
					assert.ErrorContains(t, err, match)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func Test_LoadBalancerCustomValidator_ValidateDelete(t *testing.T) {
	validator := &webhookv1beta1.LoadBalancerCustomValidator{}

	_, err := validator.ValidateDelete(t.Context(), &cloudscalev1beta1.LoadBalancer{})
	require.NoError(t, err, "ValidateDelete should not return an error")
}

func buildFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, cloudscalev1beta1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(
			&cloudscalev1beta1.LoadBalancer{},
		).
		Build()
}
