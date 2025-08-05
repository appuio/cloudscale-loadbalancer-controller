package controllers

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudscale-ch/cloudscale-go-sdk/v6"
	"github.com/go-logr/logr/testr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
	"github.com/appuio/cloudscale-loadbalancer-controller/controllers/testutil"
)

func Test_LoadBalancerReconciler_Reconcile(t *testing.T) {
	ctx := t.Context()

	scheme, cfg := testutil.SetupEnvtestEnv(t)
	c, err := client.NewWithWatch(cfg, client.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	t.Run("Adopt", func(t *testing.T) {
		t.Run("OK", func(t *testing.T) {
			_, mock := setupReconcilerWithMocks(t, cfg, c)

			mock.AddLoadBalancers(cloudscale.LoadBalancer{
				UUID: "lb-to-adopt-uuid",
				Name: "lb-to-adopt",
				ZonalResource: cloudscale.ZonalResource{
					Zone: cloudscale.Zone{
						Slug: "lpg1",
					},
				},
				Flavor: cloudscale.LoadBalancerFlavorStub{
					Slug: "lb-standard",
				},
				Status: "active",
			})

			lb := &cloudscalev1beta1.LoadBalancer{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    "default",
					GenerateName: "test-lb-",
				},
				Spec: cloudscalev1beta1.LoadBalancerSpec{
					UUID: "lb-to-adopt-uuid",
				},
			}
			require.NoError(t, c.Create(ctx, lb))

			require.EventuallyWithT(t, func(t *assert.CollectT) {
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
				assert.Equal(t, "lb-to-adopt-uuid", lb.Spec.UUID)
				assert.Equal(t, "lpg1", lb.Spec.Zone, "LoadBalancer zone should be set from adopted LoadBalancer")
				assert.Equal(t, "active", lb.Status.CloudscaleStatus, "LoadBalancer status should be set from adopted LoadBalancer")
			}, 10*time.Second, 100*time.Millisecond)
		})
		t.Run("Wrong zone", func(t *testing.T) {
			_, mock := setupReconcilerWithMocks(t, cfg, c)

			mock.AddLoadBalancers(cloudscale.LoadBalancer{
				UUID: "lb-to-adopt-wrong-zone-uuid",
				Name: "lb-to-adopt",
				ZonalResource: cloudscale.ZonalResource{
					Zone: cloudscale.Zone{
						Slug: "lpg1",
					},
				},
				Flavor: cloudscale.LoadBalancerFlavorStub{
					Slug: "lb-standard",
				},
				Status: "active",
			})

			lb := &cloudscalev1beta1.LoadBalancer{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    "default",
					GenerateName: "test-lb-",
				},
				Spec: cloudscalev1beta1.LoadBalancerSpec{
					UUID: "lb-to-adopt-wrong-zone-uuid",
					Zone: "rma1",
				},
			}
			require.NoError(t, c.Create(ctx, lb))

			require.EventuallyWithT(t, func(t *assert.CollectT) {
				var events corev1.EventList
				assert.NoError(t, c.List(ctx, &events, eventSelectorFor(lb.Name)))
				assert.True(t, slices.ContainsFunc(events.Items, func(e corev1.Event) bool {
					return e.Type == corev1.EventTypeWarning &&
						e.Reason == "ReconcileFailed" &&
						strings.Contains(e.Message, "refusing to adopt load balancer") &&
						strings.Contains(e.Message, "different zone")
				}), "Should emit warning event for adopting LoadBalancer in wrong zone, have: %+v", events.Items)
			}, 10*time.Second, 100*time.Millisecond)
		})
	})

	t.Run("E2E", func(t *testing.T) {
		_, mock := setupReconcilerWithMocks(t, cfg, c)

		nodesAndServers := sampleNodeAndServers()
		mock.AddServers(servers(nodesAndServers)...)
		for _, n := range nodes(nodesAndServers) {
			require.NoError(t, c.Create(ctx, &n), "Failed to create node %q", n.Name)
		}

		mock.AddFloatingIPs(
			cloudscale.FloatingIP{
				Network: "222.238.68.109/32",
			}, cloudscale.FloatingIP{
				Network: "a9d8:05d2:f5b7:7588:c167:c4ee:976f:ff06/128",
			},
		)

		lb := &cloudscalev1beta1.LoadBalancer{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    "default",
				GenerateName: "test-lb-",
			},
			Spec: cloudscalev1beta1.LoadBalancerSpec{
				VirtualIPAddresses: []cloudscalev1beta1.VirtualIPAddress{
					{
						SubnetID: "49bae9c6-17a3-4a5b-8d9e-faf085d912e3",
					},
				},
				FloatingIPAddresses: []cloudscalev1beta1.FloatingIPAddress{
					{
						CIDR: "222.238.68.109/32",
					},
					{
						CIDR: "a9d8:05d2:f5b7:7588:c167:c4ee:976f:ff06/128",
					},
				},
				Pools: []cloudscalev1beta1.Pool{
					{
						Name: "http",
						Frontend: cloudscalev1beta1.PoolFrontend{
							Port: 80,
						},
						Backend: cloudscalev1beta1.PoolBackend{
							Port: 80,
							NodeSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"role": "infra",
								},
							},
						},
					},
					{
						Name: "https",
						Frontend: cloudscalev1beta1.PoolFrontend{
							Port: 443,
						},
						Backend: cloudscalev1beta1.PoolBackend{
							Port: 443,
							NodeSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"role": "infra",
								},
							},
						},
					},
				},
			},
		}
		require.NoError(t, c.Create(ctx, lb))

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
			assert.NotEmpty(t, lb.Spec.UUID, "LoadBalancer ID should be set")
			assert.Equal(t, "lpg1", lb.Spec.Zone, "LoadBalancer zone should be set")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer should get an ID and zone assigned")

		require.Len(t, mock.GetLoadBalancers(), 1, "LoadBalancer should be created")
		require.NoError(t, mock.SetLoadBalancerStatus(mock.GetLoadBalancers()[0].UUID, "active"), "Failed to set LoadBalancer status to active")

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			assert.Len(t, mock.GetLoadBalancerPools(), 2, "LoadBalancer pools should be created")
			assert.Len(t, mock.GetLoadBalancerListeners(), 2, "LoadBalancer listeners should be created")
			assert.Len(t, mock.GetLoadBalancerHealthMonitors(), 2, "LoadBalancer health monitors should be created")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer pools and additional configs should be created")

		for _, pool := range lb.Spec.Pools {
			csPool, csPoolOk := slicesFindFunc(mock.GetLoadBalancerPools(), func(p cloudscale.LoadBalancerPool) bool {
				return strings.HasSuffix(p.Name, "-"+pool.Name)
			})
			require.Truef(t, csPoolOk, "Should find LoadBalancer pool for %q", pool.Name)

			_, listenerOk := slicesFindFunc(mock.GetLoadBalancerListeners(), func(l cloudscale.LoadBalancerListener) bool {
				return csPool.UUID == l.Pool.UUID
			})
			require.Truef(t, listenerOk, "Should find LoadBalancer listener for pool %q", pool.Name)
			_, hmOk := slicesFindFunc(mock.GetLoadBalancerHealthMonitors(), func(hm cloudscale.LoadBalancerHealthMonitor) bool {
				return csPool.UUID == hm.Pool.UUID
			})
			require.Truef(t, hmOk, "Should find LoadBalancer health monitor for pool %q", pool.Name)

			require.EventuallyWithT(t, func(t *assert.CollectT) {
				poolMembers := mock.GetLoadBalancerPoolMembers(csPool.UUID)
				addrs := make([]string, 0, len(poolMembers))
				for _, member := range poolMembers {
					addrs = append(addrs, member.Address)
				}
				assert.ElementsMatch(t, addrs, []string{"10.23.12.101", "10.23.12.111", "10.23.12.102"}, "Pool members should be created from matching node interfaces")
			}, 10*time.Second, 100*time.Millisecond, "LoadBalancer pool members should be created from matching node interfaces")

			require.EventuallyWithT(t, func(t *assert.CollectT) {
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
				ps, psOk := slicesFindFunc(lb.Status.Pools, func(p cloudscalev1beta1.StatusPool) bool {
					return p.Name == pool.Name
				})
				require.True(t, psOk, "Pool should be in LoadBalancer status")

				assert.ElementsMatch(t, ps.Backends, []cloudscalev1beta1.StatusBackend{
					{
						NodeName:   "infra-1",
						ServerName: "infra-1",
						Address:    "10.23.12.101",
						Status:     "unknown",
					}, {
						NodeName:   "infra-1",
						ServerName: "infra-1",
						Address:    "10.23.12.111",
						Status:     "unknown",
					}, {
						NodeName:   "infra-2",
						ServerName: "infra-2",
						Address:    "10.23.12.102",
						Status:     "unknown",
					},
				})
			}, 10*time.Second, 100*time.Millisecond)
		}

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			for _, fip := range mock.GetFloatingIPs() {
				assert.Equalf(t, lb.Spec.UUID, ptr.Deref(fip.LoadBalancer, cloudscale.LoadBalancerStub{}).UUID, "Floating IP %q should be assigned to LoadBalancer %q", fip.Network, lb.Spec.UUID)
			}
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer should have floating IPs assigned")

		// Update

		// Change node selector to app role
		lb.Spec.Pools[0].Backend.NodeSelector.MatchLabels["role"] = "app"
		require.NoError(t, c.Update(ctx, lb), "Failed to update LoadBalancer with new node selector")
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
			ps := lb.Status.Pools[0]
			assert.ElementsMatch(t, ps.Backends, []cloudscalev1beta1.StatusBackend{
				{
					NodeName:   "app-1",
					ServerName: "app-1",
					Address:    "10.23.13.101",
					Status:     "unknown",
				}, {
					NodeName:   "app-2",
					ServerName: "app-2",
					Address:    "10.23.13.102",
					Status:     "unknown",
				},
			})
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer should be updated")

		// Label spare node as app role and check if it gets added to the pool
		var spareNode corev1.Node
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "spare-1"}, &spareNode))
		spareNode.Labels["role"] = "app"
		require.NoError(t, c.Update(ctx, &spareNode), "Failed to update spare node with new role")

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
			ps := lb.Status.Pools[0]
			assert.True(t, slices.ContainsFunc(ps.Backends, func(b cloudscalev1beta1.StatusBackend) bool {
				return b.NodeName == spareNode.Name
			}), "Spare node should be added to LoadBalancer pool")
		}, 10*time.Second, 100*time.Millisecond)

		// Create new node with app role and check if it gets added to the pool
		mock.AddServers(cloudscale.Server{
			UUID: "new-app-node",
			Name: "app-3",
			Interfaces: []cloudscale.Interface{
				{
					Type: "private",
					Addresses: []cloudscale.Address{
						{
							Address: "10.23.15.101",
							Subnet: cloudscale.SubnetStub{
								UUID: subnetUUIDForAddress("10.23.15.101"),
							},
						},
					},
				},
			},
		})
		newAppNode := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "app-3",
				Labels: map[string]string{
					"role": "app",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: "cloudscale://new-app-node",
			},
		}
		require.NoError(t, c.Create(ctx, &newAppNode), "Failed to create new app node")
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
			ps := lb.Status.Pools[0]
			assert.True(t, slices.ContainsFunc(ps.Backends, func(b cloudscalev1beta1.StatusBackend) bool {
				return b.NodeName == newAppNode.Name && b.Address == "10.23.15.101"
			}), "New app node should be added to LoadBalancer pool")
		}, 10*time.Second, 100*time.Millisecond)

		// Remove spare node and check if it gets removed from the pool
		require.NoError(t, c.Delete(ctx, &spareNode), "Failed to delete spare node")
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
			ps := lb.Status.Pools[0]
			assert.False(t, slices.ContainsFunc(ps.Backends, func(b cloudscalev1beta1.StatusBackend) bool {
				return b.NodeName == spareNode.Name
			}), "Spare node should be removed from LoadBalancer pool")
		}, 10*time.Second, 100*time.Millisecond, "Spare node should be removed from LoadBalancer pool")

		// Update allowed subnets
		lb.Spec.Pools[0].Backend.AllowedSubnets = []cloudscalev1beta1.AllowedSubnet{
			{
				UUID: subnetUUIDForAddress("10.23.13.101"),
			},
		}
		require.NoError(t, c.Update(ctx, lb))
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lb), lb))
			ps := lb.Status.Pools[0]
			assert.ElementsMatch(t, ps.Backends, []cloudscalev1beta1.StatusBackend{
				{
					NodeName:   "app-1",
					ServerName: "app-1",
					Address:    "10.23.13.101",
					Status:     "unknown",
				},
			}, "LoadBalancer pool should only contain backends in allowed subnets")
		}, 10*time.Second, 100*time.Millisecond)

		// Update Health Monitor with recreate
		pool := lb.Spec.Pools[0]
		csPool, csPoolOk := slicesFindFunc(mock.GetLoadBalancerPools(), func(p cloudscale.LoadBalancerPool) bool {
			return strings.HasSuffix(p.Name, "-"+pool.Name)
		})
		require.True(t, csPoolOk)
		hms := mock.GetLoadBalancerHealthMonitorsByPool(csPool.UUID)
		require.Len(t, hms, 1)
		oldHMUUID := hms[0].UUID
		lb.Spec.Pools[0].Backend.HealthMonitor = cloudscalev1beta1.HealthMonitor{
			Type: "HTTP",
			HTTP: cloudscalev1beta1.HTTPHealthMonitor{
				Path: "/healthz",
			},
		}
		require.NoError(t, c.Update(ctx, lb), "Failed to update LoadBalancer with new health monitor")
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			hms := mock.GetLoadBalancerHealthMonitorsByPool(csPool.UUID)
			assert.Len(t, hms, 1, "Health Monitor should be recreated")
			assert.NotEqual(t, oldHMUUID, hms[0].UUID, "Health Monitor should be recreated with a new UUID")
			assert.Equal(t, "http", hms[0].Type, "Health Monitor type should be updated")
			require.NotNil(t, hms[0].HTTP, "Health Monitor HTTP should not be nil")
			assert.Equal(t, "/healthz", hms[0].HTTP.UrlPath, "Health Monitor path should be updated")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer health monitor should be updated")
		// Update Health Monitor with no recreation
		lb.Spec.Pools[0].Backend.HealthMonitor.Delay = metav1.Duration{Duration: 17 * time.Second}
		lb.Spec.Pools[0].Backend.HealthMonitor.Timeout = metav1.Duration{Duration: 17 * time.Second}
		lb.Spec.Pools[0].Backend.HealthMonitor.SuccessThreshold = 17
		lb.Spec.Pools[0].Backend.HealthMonitor.FailureThreshold = 17
		lb.Spec.Pools[0].Backend.HealthMonitor.HTTP.Path = "/sibzeh"
		lb.Spec.Pools[0].Backend.HealthMonitor.HTTP.Method = "PATCH"
		lb.Spec.Pools[0].Backend.HealthMonitor.HTTP.Host = "sibzeh.com"
		lb.Spec.Pools[0].Backend.HealthMonitor.HTTP.Version = "1.0"
		lb.Spec.Pools[0].Backend.HealthMonitor.HTTP.StatusCodes = []string{"217"}
		require.NoError(t, c.Update(ctx, lb), "Failed to update LoadBalancer with new health monitor")

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			hms := mock.GetLoadBalancerHealthMonitorsByPool(csPool.UUID)
			assert.Len(t, hms, 1)
			assert.Equal(t, "http", hms[0].Type, "Health Monitor type should not be updated")
			assert.Equal(t, 17, hms[0].DelayS, "Health Monitor delay should be updated")
			assert.Equal(t, 17, hms[0].TimeoutS, "Health Monitor timeout should be updated")
			assert.Equal(t, 17, hms[0].UpThreshold, "Health Monitor success threshold should be updated")
			assert.Equal(t, 17, hms[0].DownThreshold, "Health Monitor failure threshold should be updated")
			require.NotNil(t, hms[0].HTTP, "Health Monitor HTTP should not be nil")
			assert.Equal(t, "/sibzeh", hms[0].HTTP.UrlPath, "Health Monitor path should not be updated")
			assert.Equal(t, "PATCH", hms[0].HTTP.Method, "Health Monitor method should be updated")
			assert.Equal(t, "sibzeh.com", ptr.Deref(hms[0].HTTP.Host, "<nil>"), "Health Monitor host should be updated")
			assert.Equal(t, "1.0", hms[0].HTTP.Version, "Health Monitor version should be updated")
			assert.ElementsMatch(t, []string{"217"}, hms[0].HTTP.ExpectedCodes, "Health Monitor status codes should be updated")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer health monitor should be updated")

		// Update listener with recreate
		ls := mock.GetLoadBalancerListenersByPool(csPool.UUID)
		require.Len(t, ls, 1)
		oldLSUUID := ls[0].UUID

		lb.Spec.Pools[0].Frontend.Port = 8080
		require.NoError(t, c.Update(ctx, lb), "Failed to update LoadBalancer with new frontend port")
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			ls := mock.GetLoadBalancerListenersByPool(csPool.UUID)
			assert.Len(t, ls, 1)
			assert.NotEqual(t, oldLSUUID, ls[0].UUID, "LoadBalancer listener should be recreated")
			assert.Equal(t, 8080, ls[0].ProtocolPort, "LoadBalancer frontend port should be updated")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer frontend port should be updated")
		// Update listener with no recreation
		lb.Spec.Pools[0].Frontend.AllowedCIDRs = []cloudscalev1beta1.AllowedCIDR{{CIDR: "17.17.17.17/17"}}
		lb.Spec.Pools[0].Frontend.DataTimeout = metav1.Duration{Duration: 16 * time.Second}
		lb.Spec.Pools[0].Backend.DataTimeout = metav1.Duration{Duration: 17 * time.Second}
		lb.Spec.Pools[0].Backend.ConnectTimeout = metav1.Duration{Duration: 18 * time.Second}
		require.NoError(t, c.Update(ctx, lb))
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			ls := mock.GetLoadBalancerListenersByPool(csPool.UUID)
			assert.Len(t, ls, 1)
			assert.Equal(t, int(16*time.Second.Milliseconds()), ls[0].TimeoutClientDataMS, "LoadBalancer frontend data timeout should be updated")
			assert.Equal(t, int(17*time.Second.Milliseconds()), ls[0].TimeoutMemberDataMS, "LoadBalancer backend data timeout should be updated")
			assert.Equal(t, int(18*time.Second.Milliseconds()), ls[0].TimeoutMemberConnectMS, "LoadBalancer backend connect timeout should be updated")
			assert.ElementsMatch(t, []string{"17.17.17.17/17"}, ls[0].AllowedCIDRs, "LoadBalancer frontend allowed CIDRs should be updated")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer listener should be updated")

		// Change pool algorithm
		{
			pool := lb.Spec.Pools[1]
			lb.Spec.Pools[1].Algorithm = "LeastConnections"
			require.NoError(t, c.Update(ctx, lb))
			require.EventuallyWithT(t, func(t *assert.CollectT) {
				csPool, csPoolOk := slicesFindFunc(mock.GetLoadBalancerPools(), func(p cloudscale.LoadBalancerPool) bool {
					return strings.HasSuffix(p.Name, "-"+pool.Name)
				})
				require.True(t, csPoolOk)
				assert.Equal(t, "least_connections", csPool.Algorithm, "LoadBalancer pool algorithm should be updated")
			}, 10*time.Second, 100*time.Millisecond)
		}

		// Deletion
		require.NoError(t, c.DeleteAllOf(ctx, &cloudscalev1beta1.LoadBalancer{}, client.InNamespace("default")))
		require.EventuallyWithT(t, func(t *assert.CollectT) {
			assert.Empty(t, mock.GetLoadBalancers(), "LoadBalancer should be deleted")
		}, 10*time.Second, 100*time.Millisecond, "LoadBalancer should be deleted")
	})
}

func eventSelectorFor(lbName string) client.ListOption {
	return client.MatchingFieldsSelector{
		Selector: fields.AndSelectors(
			fields.OneTermEqualSelector("involvedObject.kind", "LoadBalancer"),
			fields.OneTermEqualSelector("involvedObject.name", lbName),
		),
	}
}

type nodeAndServer struct {
	Node   *corev1.Node
	Server *cloudscale.Server
}

func sampleNodeAndServers() []nodeAndServer {
	tc := []struct {
		name       string
		zone       string
		nodeLabels map[string]string
		addresses  []string
	}{
		{
			name: "infra-1",
			zone: "lpg1",
			nodeLabels: map[string]string{
				"role": "infra",
			},
			addresses: []string{"10.23.12.101", "10.23.12.111"},
		},
		{
			name: "infra-2",
			zone: "lpg1",
			nodeLabels: map[string]string{
				"role": "infra",
			},
			addresses: []string{"10.23.12.102"},
		},
		{
			name: "app-1",
			zone: "lpg1",
			nodeLabels: map[string]string{
				"role": "app",
			},
			addresses: []string{"10.23.13.101"},
		},
		{
			name: "app-2",
			zone: "lpg1",
			nodeLabels: map[string]string{
				"role": "app",
			},
			addresses: []string{"10.23.13.102"},
		},
		{
			name: "spare-1",
			zone: "lpg1",
			nodeLabels: map[string]string{
				"role": "spare",
			},
			addresses: []string{"10.23.14.101"},
		},
		{
			name: "remote-1",
			zone: "rma1",
			nodeLabels: map[string]string{
				"role": "remote",
			},
			addresses: []string{"10.27.12.101"},
		},
	}

	nodesAndServers := make([]nodeAndServer, len(tc))
	for i, c := range tc {
		serverUUID := uuid.NewSHA1(uuid.MustParse("C9C31A60-F019-4026-A3F3-EE1B31929C4A"), []byte(c.name)).String()

		interfaces := make([]cloudscale.Interface, 0, len(c.addresses))
		for addrs := range slices.Chunk(c.addresses, 2) {
			as := make([]cloudscale.Address, 0, len(addrs))
			for _, addr := range addrs {
				as = append(as, cloudscale.Address{
					Address: addr,
					Subnet: cloudscale.SubnetStub{
						UUID: subnetUUIDForAddress(addr),
					},
				})
			}
			if i == 0 {
				interfaces = append(interfaces, cloudscale.Interface{
					Type: "public",
				}, cloudscale.Interface{
					Type: "private",
					// Address without subnet
					Addresses: []cloudscale.Address{{
						Address: "10.45.12.27",
					}},
				})
			}

			interfaces = append(interfaces, cloudscale.Interface{
				Type:      "private",
				Addresses: as,
			})
		}

		nodesAndServers[i] = nodeAndServer{
			Node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   c.name,
					Labels: c.nodeLabels,
				},
				Spec: corev1.NodeSpec{
					ProviderID: "cloudscale://" + serverUUID,
				},
			},
			Server: &cloudscale.Server{
				UUID:       serverUUID,
				Name:       c.name,
				Interfaces: interfaces,
				ZonalResource: cloudscale.ZonalResource{
					Zone: cloudscale.Zone{
						Slug: c.zone,
					},
				},
			},
		}
	}

	return nodesAndServers
}

func subnetUUIDForAddress(address string) string {
	return uuid.NewSHA1(uuid.MustParse("09D5FC92-6C24-4767-AEE3-F0636DEDAA95"), []byte(address)).String()
}

func servers(ns []nodeAndServer) []cloudscale.Server {
	servers := make([]cloudscale.Server, 0, len(ns))
	for _, n := range ns {
		if n.Server != nil {
			servers = append(servers, *n.Server)
		}
	}
	return servers
}

func nodes(ns []nodeAndServer) []corev1.Node {
	nodes := make([]corev1.Node, 0, len(ns))
	for _, n := range ns {
		if n.Node != nil {
			nodes = append(nodes, *n.Node)
		}
	}
	return nodes
}

func setupReconcilerWithMocks(t *testing.T, cfg *rest.Config, c client.Client) (*LoadBalancerReconciler, *cloudscaleMockServer) {
	t.Helper()

	logger := testr.NewWithOptions(t, testr.Options{
		Verbosity: 1,
	})

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: c.Scheme(),
		Logger: logger,
	})
	require.NoError(t, err)

	server := new(cloudscaleMockServer)
	s := server.Server()
	t.Cleanup(s.Close)

	t.Setenv("CLOUDSCALE_API_URL", s.URL)
	cloudscaleClient := cloudscale.NewClient(nil)

	subject := &LoadBalancerReconciler{
		Client:   c,
		Scheme:   c.Scheme(),
		Recorder: mgr.GetEventRecorderFor("loadbalancer-controller"),

		ServerClient:                    cloudscaleClient.Servers,
		FloatingIPsClient:               cloudscaleClient.FloatingIPs,
		LoadbalancerClient:              cloudscaleClient.LoadBalancers,
		LoadbalancerHealthMonitorClient: cloudscaleClient.LoadBalancerHealthMonitors,
		LoadbalancerListenerClient:      cloudscaleClient.LoadBalancerListeners,
		LoadbalancerPoolClient:          cloudscaleClient.LoadBalancerPools,
		LoadbalancerPoolMemberClient:    cloudscaleClient.LoadBalancerPoolMembers,
	}
	require.NoError(t, subject.SetupWithManager(t.Name(), mgr))

	mgrCtx, mgrCancel := context.WithCancel(log.IntoContext(t.Context(), logger))
	var waitShutdown sync.WaitGroup
	waitShutdown.Add(1)
	go func() {
		defer waitShutdown.Done()
		require.NoError(t, mgr.Start(mgrCtx))
	}()
	t.Cleanup(func() {
		mgrCancel()
		waitShutdown.Wait()
	})

	return subject, server
}

func slicesFindFunc[T any](slice []T, f func(T) bool) (T, bool) {
	for _, item := range slice {
		if f(item) {
			return item, true
		}
	}
	var zero T
	return zero, false
}
