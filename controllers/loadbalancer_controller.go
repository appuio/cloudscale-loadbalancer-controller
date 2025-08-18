package controllers

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cloudscale-ch/cloudscale-go-sdk/v6"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

const (
	LoadBalancerFinalizer = "loadbalancer.cloudscale.appuio.io/finalizer"

	controllerAdoptedTag    = "appuio_io_loadbalancer_controller__adopted"
	controllerManagedTag    = "appuio_io_loadbalancer_controller__managed"
	controllerNodeNameTag   = "appuio_io_loadbalancer_controller__node_name"
	controllerServerNameTag = "appuio_io_loadbalancer_controller__server_name"

	cloudscaleProviderIDPrefix = "cloudscale://"

	cloudscaleLoadBalancerFlavor = "lb-standard"
)

// LoadBalancerReconciler reconciles a LoadBalancer object
type LoadBalancerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// MaxReconcileInterval is the maximum interval between two reconciles of a LoadBalancer object.
	// Can be lowered to speed up reconciliation of external state changes.
	// Defaults to 5 minutes.
	MaxReconcileInterval time.Duration

	ServerClient                    cloudscale.ServerService
	FloatingIPsClient               cloudscale.FloatingIPsService
	LoadbalancerClient              cloudscale.LoadBalancerService
	LoadbalancerHealthMonitorClient cloudscale.LoadBalancerHealthMonitorService
	LoadbalancerListenerClient      cloudscale.LoadBalancerListenerService
	LoadbalancerPoolClient          cloudscale.LoadBalancerPoolService
	LoadbalancerPoolMemberClient    cloudscale.LoadBalancerPoolMemberService
}

//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cloudscale.appuio.io,resources=loadbalancers/finalizers,verbs=update

//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile compares the LoadBalancer object with the upstream LoadBalancer object and updates the upstream LoadBalancer object if necessary.
func (r *LoadBalancerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	l := log.FromContext(ctx).WithName("LoadBalancerReconciler.reconcile")
	l.Info("Reconciling LoadBalancer")

	var lb cloudscalev1beta1.LoadBalancer
	if err := r.Get(ctx, req.NamespacedName, &lb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	defer func() {
		if err == nil && res == (ctrl.Result{}) {
			res = ctrl.Result{RequeueAfter: r.maxReconcileInterval()}
		}

		if err != nil {
			r.Recorder.Eventf(&lb, corev1.EventTypeWarning, "ReconcileFailed", "Reconciliation failed: %s", err.Error())
		}
	}()

	if !lb.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(&lb, LoadBalancerFinalizer) {
		if err := r.cleanupLoadBalancer(ctx, lb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to cleanup load balancer: %w", err)
		}

		if controllerutil.RemoveFinalizer(&lb, LoadBalancerFinalizer) {
			if err := r.Update(ctx, &lb); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	} else if !lb.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&lb, LoadBalancerFinalizer) {
		if err := r.Update(ctx, &lb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	ensuredLB, err := r.ensureLoadBalancer(ctx, lb)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure load balancer: %w", err)
	}
	if lb.Spec.UUID == "" {
		lb.Spec.UUID = ensuredLB.UUID
		lb.Spec.Zone = ensuredLB.Zone.Slug
		if err := r.Update(ctx, &lb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update LoadBalancer with UUID and Zone: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if lb.Spec.Zone == "" {
		lb.Spec.Zone = ensuredLB.Zone.Slug
		if err := r.Update(ctx, &lb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update LoadBalancer with Zone: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if lb.Status.CloudscaleStatus != ensuredLB.Status {
		lb.Status.CloudscaleStatus = ensuredLB.Status
		if err := r.Status().Update(ctx, &lb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update LoadBalancer status: %w", err)
		}
	}
	if ensuredLB.Status == "changing" {
		r.Recorder.Event(&lb, corev1.EventTypeNormal, "LoadBalancerChanging", "Load balancer is currently changing")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if lb.Spec.UUID != ensuredLB.UUID {
		return ctrl.Result{}, fmt.Errorf("load balancer UUID changed from %s to %s, which is not allowed", lb.Spec.UUID, ensuredLB.UUID)
	}

	pools, err := r.ensureLoadBalancerPools(ctx, lb)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure load balancer pool: %w", err)
	}

	poolStatus := make([]cloudscalev1beta1.StatusPool, 0, len(pools))
	for _, pool := range pools {
		_, err = r.ensureLoadBalancerListener(ctx, lb, pool)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to ensure load balancer listener: %w", err)
		}

		_, err = r.ensureLoadBalancerHealthMonitor(ctx, lb, pool)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to ensure load balancer health monitor: %w", err)
		}

		backends, err := r.matchingNodes(ctx, &pool.Ours.Backend.NodeSelector)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get matching nodes: %w", err)
		}

		servers, warnings, err := r.cloudscaleServersForNodes(ctx, backends)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get cloudscale servers for nodes: %w", err)
		}
		if len(warnings) > 0 {
			r.Recorder.Event(&lb, corev1.EventTypeWarning, "BackendsIgnored", strings.Join(warnings, "; "))
		}

		poolMembers, err := r.ensureLoadBalancerPoolMembers(ctx, lb, pool, servers)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to ensure load balancer pool members: %w", err)
		}

		statusBackends := make([]cloudscalev1beta1.StatusBackend, 0, len(poolMembers))
		for _, member := range poolMembers {
			statusBackends = append(statusBackends, cloudscalev1beta1.StatusBackend{
				NodeName:   member.Tags[controllerNodeNameTag],
				ServerName: member.Tags[controllerServerNameTag],
				Address:    member.Address,
				Status:     member.MonitorStatus,
			})
		}
		slices.SortFunc(statusBackends, func(a, b cloudscalev1beta1.StatusBackend) int {
			return 10*strings.Compare(a.NodeName, b.NodeName) + strings.Compare(a.Address, b.Address)
		})
		poolStatus = append(poolStatus, cloudscalev1beta1.StatusPool{
			Name:     pool.Ours.Name,
			Backends: statusBackends,
		})
	}

	var statusNeedsUpdate bool

	if !slices.EqualFunc(lb.Status.Pools, poolStatus, func(a, b cloudscalev1beta1.StatusPool) bool {
		return a.Name == b.Name && slices.Equal(a.Backends, b.Backends)
	}) {
		statusNeedsUpdate = true
		lb.Status.Pools = poolStatus
	}

	var frontendIPs []cloudscalev1beta1.StatusVirtualIPAddress
	for _, ip := range ensuredLB.VIPAddresses {
		frontendIPs = append(frontendIPs, cloudscalev1beta1.StatusVirtualIPAddress{
			Address: ip.Address,
		})
	}
	slices.SortFunc(frontendIPs, func(a, b cloudscalev1beta1.StatusVirtualIPAddress) int {
		return strings.Compare(a.Address, b.Address)
	})
	if !slices.Equal(lb.Status.VirtualIPAddresses, frontendIPs) {
		statusNeedsUpdate = true
		lb.Status.VirtualIPAddresses = frontendIPs
	}

	attachedCIDRs, err := r.attachFloatingIPs(ctx, lb)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure load balancer floating IPs: %w", err)
	}

	statusFloatingIPs := make([]cloudscalev1beta1.StatusFloatingIPAddress, 0, len(attachedCIDRs))
	for _, ip := range attachedCIDRs {
		statusFloatingIPs = append(statusFloatingIPs, cloudscalev1beta1.StatusFloatingIPAddress{
			CIDR: ip,
		})
	}
	if !slices.Equal(lb.Status.FloatingIPAddresses, statusFloatingIPs) {
		statusNeedsUpdate = true
		lb.Status.FloatingIPAddresses = statusFloatingIPs
	}

	if statusNeedsUpdate {
		if err := r.Status().Update(ctx, &lb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update LoadBalancer status: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LoadBalancerReconciler) SetupWithManager(name string, mgr ctrl.Manager) error {
	// All node changes reconcile all LoadBalancer objects as we do not know the former labels of the node.
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&cloudscalev1beta1.LoadBalancer{}).
		Watches(&corev1.Node{}, nodeUpdateHandler{
			client: mgr.GetClient(),
		}).
		Complete(r)
}

func (r *LoadBalancerReconciler) attachFloatingIPs(ctx context.Context, lb cloudscalev1beta1.LoadBalancer) ([]string, error) {
	if len(lb.Spec.FloatingIPAddresses) == 0 {
		return []string{}, nil
	}

	actualIPs, err := r.FloatingIPsClient.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list floating IPs: %w", err)
	}

	haveIPs := sets.New[string]()
	for _, wantIP := range lb.Spec.FloatingIPAddresses {
		if slices.ContainsFunc(actualIPs, func(ip cloudscale.FloatingIP) bool {
			// (cloudscale.FloatingIP).Network contains the ip with a /32 or /128 added.
			return ip.Network == wantIP.CIDR && ptr.Deref(ip.LoadBalancer, cloudscale.LoadBalancerStub{}).UUID == lb.Spec.UUID
		}) {
			haveIPs.Insert(wantIP.CIDR)
			continue
		}

		// The update api wants the IP without the /32 or /128 suffix.
		wantAddr := strings.TrimSuffix(strings.TrimSuffix(wantIP.CIDR, "/32"), "/128")
		if err := r.FloatingIPsClient.Update(ctx, wantAddr, &cloudscale.FloatingIPUpdateRequest{
			LoadBalancer: lb.Spec.UUID,
		}); err != nil {
			return nil, fmt.Errorf("failed to update floating IP %q: %w", wantAddr, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "FloatingIPAttached", "Attached floating IP %q to load balancer %q", wantAddr, lb.Spec.UUID)
		haveIPs.Insert(wantIP.CIDR)
	}

	return sets.List(haveIPs), nil
}

func (r *LoadBalancerReconciler) ensureLoadBalancerPoolMembers(ctx context.Context, lb cloudscalev1beta1.LoadBalancer, pool poolMapping, servers []nodeServerMapping) ([]cloudscale.LoadBalancerPoolMember, error) {
	l := log.FromContext(ctx).WithName("LoadBalancerReconciler.ensureLoadBalancerPoolMembers")

	shouldPoolMembers := make([]cloudscale.LoadBalancerPoolMemberRequest, 0)
	for _, server := range servers {
		for _, inf := range server.Server.Interfaces {
			if inf.Type == "public" {
				continue
			}
			for _, addr := range inf.Addresses {
				if addr.Subnet.UUID == "" {
					l.Info("Skipping pool member for server interface with empty subnet UUID", "server_name", server.Server.Name, "server_uuid", server.Server.UUID, "interface_network", inf.Network.UUID, "address", addr.Address)
					continue
				}
				if a := pool.Ours.Backend.AllowedSubnets; len(a) > 0 && !slices.ContainsFunc(a, func(s cloudscalev1beta1.AllowedSubnet) bool {
					return s.UUID == addr.Subnet.UUID
				}) {
					continue
				}
				tags := baseTagMap()
				(*tags)[controllerNodeNameTag] = server.Node.Name
				(*tags)[controllerServerNameTag] = server.Server.Name
				shouldPoolMembers = append(shouldPoolMembers, cloudscale.LoadBalancerPoolMemberRequest{
					TaggedResourceRequest: cloudscale.TaggedResourceRequest{
						Tags: tags,
					},
					Name:         fmt.Sprintf("%s-%s-%s-%d", cloudscaleLoadbalancerName(lb), pool.Ours.Name, addr.Address, pool.Ours.Backend.Port),
					Enabled:      ptr.To(true),
					ProtocolPort: int(pool.Ours.Backend.Port),
					MonitorPort:  int(pool.Ours.Backend.HealthMonitor.Port),
					Address:      addr.Address,
					Subnet:       addr.Subnet.UUID,
				})
			}
		}
	}

	actualPoolMembers, err := r.LoadbalancerPoolMemberClient.List(ctx, pool.Theirs.UUID)
	if err != nil {
		return nil, fmt.Errorf("failed to list load balancer pool members: %w", err)
	}

	membersToDelete := make([]cloudscale.LoadBalancerPoolMember, 0, len(actualPoolMembers))
	for _, member := range actualPoolMembers {
		if !slices.ContainsFunc(shouldPoolMembers, func(s cloudscale.LoadBalancerPoolMemberRequest) bool {
			return loadBalancerPoolMemberRequestEqual(loadBalancerPoolMemberToRequest(member), s)
		}) {
			membersToDelete = append(membersToDelete, member)
		}
	}
	membersToCreate := make([]cloudscale.LoadBalancerPoolMemberRequest, 0, len(shouldPoolMembers))
	for _, member := range shouldPoolMembers {
		if !slices.ContainsFunc(actualPoolMembers, func(s cloudscale.LoadBalancerPoolMember) bool {
			return loadBalancerPoolMemberRequestEqual(member, loadBalancerPoolMemberToRequest(s))
		}) {
			membersToCreate = append(membersToCreate, member)
		}
	}

	for _, member := range membersToDelete {
		if err := r.LoadbalancerPoolMemberClient.Delete(ctx, member.Pool.UUID, member.UUID); err != nil {
			return nil, fmt.Errorf("failed to delete load balancer pool member %s: %w", member.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "PoolMemberDeleted", "Deleted load balancer pool member %q", member.UUID)
	}
	for _, member := range membersToCreate {
		createdMember, err := r.LoadbalancerPoolMemberClient.Create(ctx, pool.Theirs.UUID, &member)
		if err != nil {
			return nil, fmt.Errorf("failed to create load balancer pool member: %w", err)
		}
		if createdMember == nil {
			return nil, fmt.Errorf("cloudscale returned empty load balancer pool member")
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "PoolMemberCreated", "Created load balancer pool member %q", createdMember.UUID)
	}

	actualPoolMembers, err = r.LoadbalancerPoolMemberClient.List(ctx, pool.Theirs.UUID)
	if err != nil {
		return nil, fmt.Errorf("failed to list load balancer pool members: %w", err)
	}

	return actualPoolMembers, nil
}

func (r *LoadBalancerReconciler) ensureLoadBalancerHealthMonitor(ctx context.Context, lb cloudscalev1beta1.LoadBalancer, pool poolMapping) (cloudscale.LoadBalancerHealthMonitor, error) {
	allHealthMonitors, err := r.LoadbalancerHealthMonitorClient.List(ctx)
	if err != nil {
		return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to list load balancer health monitors: %w", err)
	}
	healthMonitors := make([]cloudscale.LoadBalancerHealthMonitor, 0, len(allHealthMonitors))
	for _, healthMonitor := range allHealthMonitors {
		if healthMonitor.Pool.UUID == pool.Theirs.UUID {
			healthMonitors = append(healthMonitors, healthMonitor)
		}
	}

	var httpHost *string
	if h := pool.Ours.Backend.HealthMonitor.HTTP.Host; h != "" {
		httpHost = &h
	}
	healthMonitorReq := &cloudscale.LoadBalancerHealthMonitorRequest{
		TaggedResourceRequest: cloudscale.TaggedResourceRequest{
			Tags: baseTagMap(),
		},
		Pool:          pool.Theirs.UUID,
		DelayS:        int(pool.Ours.Backend.HealthMonitor.GetDelay().Seconds()),
		TimeoutS:      int(pool.Ours.Backend.HealthMonitor.GetTimeout().Seconds()),
		UpThreshold:   int(pool.Ours.Backend.HealthMonitor.GetUpThreshold()),
		DownThreshold: int(pool.Ours.Backend.HealthMonitor.GetDownThreshold()),
		Type:          healthMonitorTypeToCloudscaleHealthMonitorType(pool.Ours.Backend.HealthMonitor.GetType()),
	}
	httpConfigAllowed := healthMonitorReq.Type == "http" || healthMonitorReq.Type == "https"
	if httpConfigAllowed {
		healthMonitorReq.HTTP = &cloudscale.LoadBalancerHealthMonitorHTTPRequest{
			ExpectedCodes: pool.Ours.Backend.HealthMonitor.HTTP.StatusCodes,
			Method:        pool.Ours.Backend.HealthMonitor.HTTP.GetMethod(),
			UrlPath:       pool.Ours.Backend.HealthMonitor.HTTP.GetPath(),
			Version:       pool.Ours.Backend.HealthMonitor.HTTP.GetVersion(),
			Host:          httpHost,
		}
	}
	createHealthMonitor := func() (cloudscale.LoadBalancerHealthMonitor, error) {
		healthMonitor, err := r.LoadbalancerHealthMonitorClient.Create(ctx, healthMonitorReq)
		if err != nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to create load balancer health monitor: %w", err)
		}
		if healthMonitor == nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("cloudscale returned empty load balancer health monitor")
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorCreated", "Created load balancer health monitor %q", healthMonitor.UUID)
		return *healthMonitor, nil
	}

	if len(healthMonitors) == 0 {
		return createHealthMonitor()
	}
	if len(healthMonitors) > 1 {
		return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("found multiple load balancer health monitors for load balancer %s and pool %s: %v", lb.Spec.UUID, pool.Theirs.UUID, healthMonitors)
	}

	healthMonitor := healthMonitors[0]

	if healthMonitor.Type != healthMonitorReq.Type {
		r.Recorder.Eventf(&lb, corev1.EventTypeWarning, "HealthMonitorChanged", "Load balancer health monitor %q has changed type (was: %s, now: %s), recreating healthMonitor",
			healthMonitor.UUID, healthMonitor.Type, healthMonitorReq.Type)
		if err := r.LoadbalancerHealthMonitorClient.Delete(ctx, healthMonitor.UUID); err != nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to delete load balancer health monitor %s: %w", healthMonitor.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorDeleted", "Deleted load balancer health monitor %q", healthMonitor.UUID)
		return createHealthMonitor()
	}

	// The cloudscale API does not allow updating more than one attribute at a time so we do every attribute separately.
	if healthMonitor.DelayS != healthMonitorReq.DelayS {
		err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
			DelayS: healthMonitorReq.DelayS,
		})
		if err != nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `delay_s`: %w", healthMonitor.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `delay_s`", healthMonitor.UUID)
		healthMonitor.DelayS = healthMonitorReq.DelayS
	}
	if healthMonitor.TimeoutS != healthMonitorReq.TimeoutS {
		err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
			TimeoutS: healthMonitorReq.TimeoutS,
		})
		if err != nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `timeout_s`: %w", healthMonitor.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `timeout_s`", healthMonitor.UUID)
		healthMonitor.TimeoutS = healthMonitorReq.TimeoutS
	}
	if healthMonitor.UpThreshold != healthMonitorReq.UpThreshold {
		err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
			UpThreshold: healthMonitorReq.UpThreshold,
		})
		if err != nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `up_threshold`: %w", healthMonitor.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `up_threshold`: %w", healthMonitor.UUID, healthMonitorReq.UpThreshold)
		healthMonitor.UpThreshold = healthMonitorReq.UpThreshold
	}
	if healthMonitor.DownThreshold != healthMonitorReq.DownThreshold {
		err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
			DownThreshold: healthMonitorReq.DownThreshold,
		})
		if err != nil {
			return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `down_threshold`: %w", healthMonitor.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `down_threshold`: %w", healthMonitor.UUID, healthMonitorReq.DownThreshold)
		healthMonitor.DownThreshold = healthMonitorReq.DownThreshold
	}

	if httpConfigAllowed {
		if healthMonitor.HTTP == nil {
			healthMonitor.HTTP = &cloudscale.LoadBalancerHealthMonitorHTTP{}
		}
		if healthMonitor.HTTP.Method != healthMonitorReq.HTTP.Method {
			err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
				HTTP: &cloudscale.LoadBalancerHealthMonitorHTTPRequest{
					Method: healthMonitorReq.HTTP.Method,
				},
			})
			if err != nil {
				return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `http.method`: %w", healthMonitor.UUID, err)
			}
			r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `http.method`", healthMonitor.UUID)
			healthMonitor.HTTP.Method = healthMonitorReq.HTTP.Method
		}
		if healthMonitor.HTTP.UrlPath != healthMonitorReq.HTTP.UrlPath {
			err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
				HTTP: &cloudscale.LoadBalancerHealthMonitorHTTPRequest{
					UrlPath: healthMonitorReq.HTTP.UrlPath,
				},
			})
			if err != nil {
				return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `http.url_path`: %w", healthMonitor.UUID, err)
			}
			r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `http.url_path`", healthMonitor.UUID)
			healthMonitor.HTTP.UrlPath = healthMonitorReq.HTTP.UrlPath
		}
		if healthMonitor.HTTP.Version != healthMonitorReq.HTTP.Version {
			err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
				HTTP: &cloudscale.LoadBalancerHealthMonitorHTTPRequest{
					Version: healthMonitorReq.HTTP.Version,
				},
			})
			if err != nil {
				return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `http.version`: %w", healthMonitor.UUID, err)
			}
			r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `http.version`", healthMonitor.UUID)
			healthMonitor.HTTP.Version = healthMonitorReq.HTTP.Version
		}
		if !comparablePtrEqual(healthMonitor.HTTP.Host, healthMonitorReq.HTTP.Host) {
			err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
				HTTP: &cloudscale.LoadBalancerHealthMonitorHTTPRequest{
					Host: healthMonitorReq.HTTP.Host,
				},
			})
			if err != nil {
				return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `http.host`: %w", healthMonitor.UUID, err)
			}
			r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `http.host`", healthMonitor.UUID)
			healthMonitor.HTTP.Host = healthMonitorReq.HTTP.Host
		}
		if !slicesMatch(healthMonitor.HTTP.ExpectedCodes, healthMonitorReq.HTTP.ExpectedCodes) {
			err := r.LoadbalancerHealthMonitorClient.Update(ctx, healthMonitor.UUID, &cloudscale.LoadBalancerHealthMonitorRequest{
				HTTP: &cloudscale.LoadBalancerHealthMonitorHTTPRequest{
					ExpectedCodes: healthMonitorReq.HTTP.ExpectedCodes,
				},
			})
			if err != nil {
				return cloudscale.LoadBalancerHealthMonitor{}, fmt.Errorf("failed to update load balancer health monitor %s `http.expected_codes`: %w", healthMonitor.UUID, err)
			}
			r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "HealthMonitorUpdated", "Updated load balancer health monitor %q `http.expected_codes`", healthMonitor.UUID)
			healthMonitor.HTTP.ExpectedCodes = healthMonitorReq.HTTP.ExpectedCodes
		}
	}

	return healthMonitor, nil
}

func (r *LoadBalancerReconciler) ensureLoadBalancerListener(ctx context.Context, lb cloudscalev1beta1.LoadBalancer, pool poolMapping) (cloudscale.LoadBalancerListener, error) {
	allListeners, err := r.LoadbalancerListenerClient.List(ctx)
	if err != nil {
		return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to list load balancer listeners: %w", err)
	}
	listeners := make([]cloudscale.LoadBalancerListener, 0, len(allListeners))
	for _, listener := range allListeners {
		if listener.Pool.UUID == pool.Theirs.UUID {
			listeners = append(listeners, listener)
		}
	}

	allowedCIDRs := make([]string, 0, len(pool.Ours.Frontend.AllowedCIDRs))
	for _, cidr := range pool.Ours.Frontend.AllowedCIDRs {
		allowedCIDRs = append(allowedCIDRs, cidr.CIDR)
	}
	slices.Sort(allowedCIDRs)

	listenerReq := &cloudscale.LoadBalancerListenerRequest{
		TaggedResourceRequest: cloudscale.TaggedResourceRequest{
			Tags: baseTagMap(),
		},
		Name:                   fmt.Sprintf("%s-%d", cloudscaleLoadbalancerName(lb), pool.Ours.Frontend.Port),
		Pool:                   pool.Theirs.UUID,
		Protocol:               listenerProtocolToCloudscaleListenerProtocol(pool.Ours.Frontend.GetProtocol()),
		ProtocolPort:           int(pool.Ours.Frontend.Port),
		AllowedCIDRs:           &allowedCIDRs,
		TimeoutClientDataMS:    int(pool.Ours.Frontend.GetDataTimeout().Milliseconds()),
		TimeoutMemberConnectMS: int(pool.Ours.Backend.GetConnectTimeout().Milliseconds()),
		TimeoutMemberDataMS:    int(pool.Ours.Backend.GetDataTimeout().Milliseconds()),
	}
	createListener := func() (cloudscale.LoadBalancerListener, error) {
		listener, err := r.LoadbalancerListenerClient.Create(ctx, listenerReq)
		if err != nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to create load balancer listener: %w", err)
		}
		if listener == nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("cloudscale returned empty load balancer listener")
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "ListenerCreated", "Created load balancer listener %q for pool %q", listener.UUID, pool.Theirs.UUID)
		return *listener, nil
	}

	if len(listeners) == 0 {
		return createListener()
	}
	if len(listeners) > 1 {
		return cloudscale.LoadBalancerListener{}, fmt.Errorf("found multiple load balancer listeners for load balancer %s and pool %s: %v", lb.Spec.UUID, pool.Theirs.UUID, listeners)
	}

	listener := listeners[0]

	if listener.Protocol != listenerReq.Protocol ||
		listener.ProtocolPort != listenerReq.ProtocolPort {
		r.Recorder.Eventf(&lb, corev1.EventTypeWarning, "ListenerChanged", "Load balancer listener %q has changed protocol (was: %s/%d, now: %s/%d), recreating listener",
			listener.UUID, listener.Protocol, listener.ProtocolPort, listenerReq.Protocol, listenerReq.ProtocolPort)
		if err := r.LoadbalancerListenerClient.Delete(ctx, listener.UUID); err != nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to delete load balancer listener %s: %w", listener.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "ListenerDeleted", "Deleted load balancer listener %q", listener.UUID)
		return createListener()
	}

	// The cloudscale API does not allow updating more than one attribute at a time so we do every attribute separately.
	actualAllowedCIDRs := slices.Clone(listener.AllowedCIDRs)
	slices.Sort(actualAllowedCIDRs)
	if !slices.Equal(actualAllowedCIDRs, allowedCIDRs) {
		err := r.LoadbalancerListenerClient.Update(ctx, listener.UUID, &cloudscale.LoadBalancerListenerRequest{
			AllowedCIDRs: &allowedCIDRs,
		})
		if err != nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to update load balancer listener %s `allowed_cidrs`: %w", listener.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "ListenerUpdated", "Updated load balancer listener %q `allowed_cidrs`", listener.UUID)
		listener.AllowedCIDRs = allowedCIDRs
	}
	if listener.TimeoutClientDataMS != listenerReq.TimeoutClientDataMS {
		err := r.LoadbalancerListenerClient.Update(ctx, listener.UUID, &cloudscale.LoadBalancerListenerRequest{
			TimeoutClientDataMS: listenerReq.TimeoutClientDataMS,
		})
		if err != nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to update load balancer listener %s `timeout_client_data_ms`: %w", listener.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "ListenerUpdated", "Updated load balancer listener %q `timeout_client_data_ms`", listener.UUID)
		listener.TimeoutClientDataMS = listenerReq.TimeoutClientDataMS
	}
	if listener.TimeoutMemberConnectMS != listenerReq.TimeoutMemberConnectMS {
		err := r.LoadbalancerListenerClient.Update(ctx, listener.UUID, &cloudscale.LoadBalancerListenerRequest{
			TimeoutMemberConnectMS: listenerReq.TimeoutMemberConnectMS,
		})
		if err != nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to update load balancer listener %s `timeout_member_connect_ms`: %w", listener.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "ListenerUpdated", "Updated load balancer listener %q `timeout_member_connect_ms`", listener.UUID)
		listener.TimeoutMemberConnectMS = listenerReq.TimeoutMemberConnectMS
	}
	if listener.TimeoutMemberDataMS != listenerReq.TimeoutMemberDataMS {
		err := r.LoadbalancerListenerClient.Update(ctx, listener.UUID, &cloudscale.LoadBalancerListenerRequest{
			TimeoutMemberDataMS: listenerReq.TimeoutMemberDataMS,
		})
		if err != nil {
			return cloudscale.LoadBalancerListener{}, fmt.Errorf("failed to update load balancer listener %s `timeout_member_data_ms`: %w", listener.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "ListenerUpdated", "Updated load balancer listener %q `timeout_member_data_ms`", listener.UUID)
		listener.TimeoutMemberDataMS = listenerReq.TimeoutMemberDataMS
	}

	return listener, nil
}

type poolMapping struct {
	Ours   cloudscalev1beta1.Pool
	Theirs cloudscale.LoadBalancerPool
}

func (r *LoadBalancerReconciler) ensureLoadBalancerPools(ctx context.Context, lb cloudscalev1beta1.LoadBalancer) ([]poolMapping, error) {
	allPools, err := r.LoadbalancerPoolClient.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list load balancer pools: %w", err)
	}
	actualPools := make([]cloudscale.LoadBalancerPool, 0, len(allPools))
	for _, pool := range allPools {
		if pool.LoadBalancer.UUID == lb.Spec.UUID {
			actualPools = append(actualPools, pool)
		}
	}
	shouldPools := make([]cloudscale.LoadBalancerPoolRequest, 0, len(lb.Spec.Pools))
	for _, pool := range lb.Spec.Pools {
		shouldPools = append(shouldPools, cloudscale.LoadBalancerPoolRequest{
			TaggedResourceRequest: cloudscale.TaggedResourceRequest{
				Tags: baseTagMap(),
			},
			Name:         fmt.Sprintf("%s-%s", cloudscaleLoadbalancerName(lb), pool.Name),
			LoadBalancer: lb.Spec.UUID,
			Algorithm:    poolAlgorithmToCloudscalePoolAlgorithm(pool.GetAlgorithm()),
			Protocol:     poolProtocolToCloudscalePoolProtocol(pool.Backend.GetProtocol()),
		})
	}

	ensuredPools := make([]poolMapping, 0, len(lb.Spec.Pools))

	for _, actualPool := range actualPools {
		ar := cloudscale.LoadBalancerPoolRequest{
			Name:         actualPool.Name,
			LoadBalancer: actualPool.LoadBalancer.UUID,
			Algorithm:    actualPool.Algorithm,
			Protocol:     actualPool.Protocol,
		}
		if slices.ContainsFunc(shouldPools, func(s cloudscale.LoadBalancerPoolRequest) bool {
			return loadBalancerPoolRequestEqual(ar, s)
		}) {
			continue
		}
		if err := r.LoadbalancerPoolClient.Delete(ctx, actualPool.UUID); err != nil {
			return nil, fmt.Errorf("failed to delete load balancer pool %q (%q): %w", actualPool.Name, actualPool.UUID, err)
		}
		r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "PoolDeleted", "Deleted load balancer pool %q (%q)", actualPool.Name, actualPool.UUID)
	}

	for i, shouldPool := range shouldPools {
		if ai := slices.IndexFunc(actualPools, func(a cloudscale.LoadBalancerPool) bool {
			return loadBalancerPoolRequestEqual(shouldPool, cloudscale.LoadBalancerPoolRequest{
				Name:         a.Name,
				LoadBalancer: a.LoadBalancer.UUID,
				Algorithm:    a.Algorithm,
				Protocol:     a.Protocol,
			})
		}); ai >= 0 {
			// Pool already exists, we don't need to create it.
			ensuredPools = append(ensuredPools, poolMapping{
				Ours:   lb.Spec.Pools[i],
				Theirs: actualPools[ai],
			})
		} else {
			pool, err := r.LoadbalancerPoolClient.Create(ctx, &shouldPool)
			if err != nil {
				return nil, fmt.Errorf("failed to create load balancer pool: %w", err)
			}
			if pool == nil {
				return nil, fmt.Errorf("cloudscale returned empty load balancer pool")
			}
			r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "PoolCreated", "Created load balancer pool %q (%q)", pool.Name, pool.UUID)
			ensuredPools = append(ensuredPools, poolMapping{
				Ours:   lb.Spec.Pools[i],
				Theirs: *pool,
			})
		}
	}

	return ensuredPools, nil
}

func (r *LoadBalancerReconciler) ensureLoadBalancer(ctx context.Context, lb cloudscalev1beta1.LoadBalancer) (ensuredLB cloudscale.LoadBalancer, err error) {
	l := log.FromContext(ctx).WithName("LoadBalancerReconciler.ensureLoadBalancer")

	name := cloudscaleLoadbalancerName(lb)

	if lb.Spec.UUID == "" {
		// Check if we already created a load balancer with this name but failed to set the UUID on the manifest.
		ll, err := r.LoadbalancerClient.List(ctx, cloudscale.WithTagFilter(cloudscale.TagMap{controllerManagedTag: "true"}))
		if err != nil {
			return cloudscale.LoadBalancer{}, fmt.Errorf("failed to list load balancers: %w", err)
		}
		for _, existingLB := range ll {
			if existingLB.Name == name {
				l.Info("Found existing cloudscale load balancer with matching name", "uuid", existingLB.UUID)
				return existingLB, nil
			}
		}

		zone := lb.Spec.Zone
		if zone == "" {
			var err error
			zone, err = r.guessLoadbalancerZone(ctx, lb)
			if err != nil {
				return cloudscale.LoadBalancer{}, fmt.Errorf("failed to guess load balancer zone: %w", err)
			}
		}

		// Cloudscale does not seem to handle empty arrays, so we explicitly create a null pointer.
		var vips *[]cloudscale.VIPAddressRequest
		if len(lb.Spec.VirtualIPAddresses) > 0 {
			vs := make([]cloudscale.VIPAddressRequest, 0, len(lb.Spec.VirtualIPAddresses))
			for _, vip := range lb.Spec.VirtualIPAddresses {
				vs = append(vs, cloudscale.VIPAddressRequest{
					Subnet:  vip.SubnetID,
					Address: vip.Address,
				})
			}
			vips = &vs
		}

		shouldLB := &cloudscale.LoadBalancerRequest{
			Name:         name,
			Flavor:       cloudscaleLoadBalancerFlavor,
			VIPAddresses: vips,
			ZonalResourceRequest: cloudscale.ZonalResourceRequest{
				Zone: zone,
			},
			TaggedResourceRequest: cloudscale.TaggedResourceRequest{
				Tags: baseTagMap(),
			},
		}
		clb, err := r.LoadbalancerClient.Create(ctx, shouldLB)
		if err != nil {
			return cloudscale.LoadBalancer{}, fmt.Errorf("failed to create cloudscale load balancer: %w", err)
		}
		if clb == nil {
			return cloudscale.LoadBalancer{}, fmt.Errorf("cloudscale returned empty load balancer")
		}
		l.Info("Created cloudscale load balancer", "uuid", clb.UUID)
		return *clb, nil
	}

	clb, err := r.LoadbalancerClient.Get(ctx, lb.Spec.UUID)
	if err != nil {
		return cloudscale.LoadBalancer{}, fmt.Errorf("failed to get cloudscale load balancer: %w", err)
	}
	if clb == nil {
		return cloudscale.LoadBalancer{}, fmt.Errorf("cloudscale returned empty load balancer")
	}

	if clb.Tags[controllerManagedTag] == "true" {
		l.Info("Found existing cloudscale load balancer", "uuid", clb.UUID)
		return *clb, nil
	}
	l.Info("Adopting existing cloudscale load balancer", "uuid", clb.UUID)

	if len(lb.Spec.VirtualIPAddresses) > 0 {
		return cloudscale.LoadBalancer{}, fmt.Errorf("refusing to adopt load balancer %q: VIPAddresses must be empty to adopt", clb.UUID)
	}
	if clb.Flavor.Slug != cloudscaleLoadBalancerFlavor {
		return cloudscale.LoadBalancer{}, fmt.Errorf("refusing to adopt load balancer %q with different flavor (want: %q, have: %q)", clb.UUID, cloudscaleLoadBalancerFlavor, clb.Flavor.Slug)
	}
	if lb.Spec.Zone != "" && clb.Zone.Slug != lb.Spec.Zone {
		return cloudscale.LoadBalancer{}, fmt.Errorf("refusing to adopt load balancer %q in different zone (want: %q, have: %q)", clb.UUID, lb.Spec.Zone, clb.Zone.Slug)
	}

	// Adopt the load balancer by updating its tags.
	if clb.Tags == nil {
		clb.Tags = make(cloudscale.TagMap)
	}
	clb.Tags[controllerManagedTag] = "true"
	clb.Tags[controllerAdoptedTag] = "true"
	if err := r.LoadbalancerClient.Update(ctx, clb.UUID, &cloudscale.LoadBalancerRequest{
		TaggedResourceRequest: cloudscale.TaggedResourceRequest{
			Tags: &clb.Tags,
		},
	}); err != nil {
		return cloudscale.LoadBalancer{}, fmt.Errorf("failed to update cloudscale load balancer: %w", err)
	}
	l.Info("Adopted cloudscale load balancer", "uuid", clb.UUID)
	r.Recorder.Eventf(&lb, corev1.EventTypeNormal, "LoadBalancerAdopted", "Adopted cloudscale load balancer %q (%q)", clb.UUID, lb.Name)
	return *clb, nil
}

func (r *LoadBalancerReconciler) matchingNodes(ctx context.Context, ls *metav1.LabelSelector) ([]corev1.Node, error) {
	nodeSelector, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return nil, fmt.Errorf("failed to convert node selector: %w", err)
	}

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes, client.MatchingLabelsSelector{
		Selector: nodeSelector,
	}); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	slices.SortFunc(nodes.Items, func(a, b corev1.Node) int {
		return strings.Compare(a.Name, b.Name)
	})

	return nodes.Items, nil
}

type nodeServerMapping struct {
	Node   corev1.Node
	Server cloudscale.Server
}

func (r *LoadBalancerReconciler) cloudscaleServersForNodes(ctx context.Context, nodes []corev1.Node) (servers []nodeServerMapping, warnings []string, err error) {
	warnings = make([]string, 0, len(nodes))
	servers = make([]nodeServerMapping, 0, len(nodes))
	for _, node := range nodes {
		if node.Spec.ProviderID == "" {
			warnings = append(warnings, fmt.Sprintf("node %q does not have a provider ID", node.Name))
			continue
		}
		if !strings.HasPrefix(node.Spec.ProviderID, cloudscaleProviderIDPrefix) {
			warnings = append(warnings, fmt.Sprintf("node %q does not have a cloudscale provider ID (%q)", node.Name, node.Spec.ProviderID))
			continue
		}
		id := strings.TrimPrefix(node.Spec.ProviderID, cloudscaleProviderIDPrefix)
		server, err := r.ServerClient.Get(ctx, id)
		if err != nil {
			if IsCloudscaleNotFoundErr(err) {
				warnings = append(warnings, fmt.Sprintf("node %q(%q) does not have a cloudscale server ", node.Name, id))
				continue
			}
			return nil, nil, fmt.Errorf("failed to get cloudscale server for node %q: %w", node.Name, err)
		}
		if server == nil {
			warnings = append(warnings, fmt.Sprintf("cloudscale returned empty server for node %q(%q) ", node.Name, id))
			continue
		}

		servers = append(servers, nodeServerMapping{
			Node:   node,
			Server: *server,
		})
	}
	return servers, warnings, nil
}

func (r *LoadBalancerReconciler) cleanupLoadBalancer(ctx context.Context, lb cloudscalev1beta1.LoadBalancer) error {
	l := log.FromContext(ctx).WithName("LoadBalancerReconciler.cleanupLoadBalancer")

	if lb.Spec.UUID != "" {
		if err := r.LoadbalancerClient.Delete(ctx, lb.Spec.UUID); err != nil {
			if IsCloudscaleNotFoundErr(err) {
				l.Info("Cloudscale load balancer already deleted", "uuid", lb.Spec.UUID)
				return nil
			}
			return fmt.Errorf("failed to delete cloudscale load balancer %s: %w", lb.Spec.UUID, err)
		}
		l.Info("Deleted cloudscale load balancer", "uuid", lb.Spec.UUID)
	}

	// Cleanup load balancers that were created but manifest save failed.
	lbs, err := r.LoadbalancerClient.List(ctx, cloudscale.WithTagFilter(cloudscale.TagMap{controllerManagedTag: "true"}))
	if err != nil {
		return fmt.Errorf("failed to list cloudscale load balancers: %w", err)
	}
	toDelete := make([]cloudscale.LoadBalancer, 0, len(lbs))
	for _, existingLB := range lbs {
		if existingLB.Name == cloudscaleLoadbalancerName(lb) {
			toDelete = append(toDelete, existingLB)
		}
	}
	for _, del := range toDelete {
		if err := r.LoadbalancerClient.Delete(ctx, del.UUID); err != nil {
			return fmt.Errorf("failed to delete cloudscale load balancer %s: %w", del.UUID, err)
		}
		l.Info("Deleted cloudscale load balancer", "uuid", del.UUID)
	}

	return nil
}

func (r *LoadBalancerReconciler) guessLoadbalancerZone(ctx context.Context, lb cloudscalev1beta1.LoadBalancer) (string, error) {
	zoneSet := sets.New[string]()

	for _, pool := range lb.Spec.Pools {
		backends, err := r.matchingNodes(ctx, &pool.Backend.NodeSelector)
		if err != nil {
			return "", fmt.Errorf("failed to get matching nodes: %w", err)
		}

		servers, _, err := r.cloudscaleServersForNodes(ctx, backends)
		if err != nil {
			return "", fmt.Errorf("failed to get cloudscale servers for nodes: %w", err)
		}
		for _, server := range servers {
			if zone := server.Server.Zone.Slug; zone != "" {
				zoneSet.Insert(zone)
			}
		}
	}

	zones := sets.List(zoneSet)

	if len(zones) == 0 {
		return "", fmt.Errorf("no zone specified and no servers with zone information found")
	}
	if len(zones) > 1 {
		return "", fmt.Errorf("no zone specified and multiple zones found in backends: %v", zones)
	}

	return zones[0], nil
}

func (r *LoadBalancerReconciler) maxReconcileInterval() time.Duration {
	if r.MaxReconcileInterval == 0 {
		return 5 * time.Minute
	}
	return r.MaxReconcileInterval
}

func loadBalancerPoolRequestEqual(a, b cloudscale.LoadBalancerPoolRequest) bool {
	return a.Name == b.Name &&
		a.LoadBalancer == b.LoadBalancer &&
		a.Algorithm == b.Algorithm &&
		a.Protocol == b.Protocol
}

func cloudscaleLoadbalancerName(lb cloudscalev1beta1.LoadBalancer) string {
	return fmt.Sprintf("appuio_io_loadbalancer_controller-%s-%s", lb.Namespace, lb.Name)
}

// IsCloudscaleNotFoundErr checks if the error is a Cloudscale not found error.
func IsCloudscaleNotFoundErr(err error) bool {
	var res *cloudscale.ErrorResponse
	return errors.As(err, &res) && res.StatusCode == http.StatusNotFound
}

func baseTagMap() *cloudscale.TagMap {
	return &cloudscale.TagMap{
		controllerManagedTag: "true",
	}
}

func comparablePtrEqual[T comparable](a, b *T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// slicesMatch checks if two slices contain the same elements, regardless of order.
func slicesMatch[S ~[]E, E cmp.Ordered](a, b S) bool {
	if len(a) != len(b) {
		return false
	}
	ac := slices.Clone(a)
	bc := slices.Clone(b)
	slices.Sort(ac)
	slices.Sort(bc)
	return slices.Equal(ac, bc)
}

func poolAlgorithmToCloudscalePoolAlgorithm(alg string) string {
	switch alg {
	case "RoundRobin":
		return "round_robin"
	case "LeastConnections":
		return "least_connections"
	case "SourceIP":
		return "source_ip"
	default:
		return alg
	}
}

func listenerProtocolToCloudscaleListenerProtocol(proto string) string {
	switch proto {
	case "TCP":
		return "tcp"
	default:
		return proto
	}
}

func poolProtocolToCloudscalePoolProtocol(proto string) string {
	switch proto {
	case "TCP":
		return "tcp"
	case "Proxy":
		return "proxy"
	case "ProxyV2":
		return "proxyv2"
	default:
		return proto
	}
}

func healthMonitorTypeToCloudscaleHealthMonitorType(typ string) string {
	switch typ {
	case "Ping":
		return "ping"
	case "HTTP":
		return "http"
	case "HTTPS":
		return "https"
	case "TCP":
		return "tcp"
	case "TLSHello":
		return "tls-hello"
	default:
		return typ
	}
}

func loadBalancerPoolMemberToRequest(member cloudscale.LoadBalancerPoolMember) cloudscale.LoadBalancerPoolMemberRequest {
	return cloudscale.LoadBalancerPoolMemberRequest{
		TaggedResourceRequest: cloudscale.TaggedResourceRequest{
			Tags: &member.Tags,
		},
		Name:         member.Name,
		Enabled:      ptr.To(member.Enabled),
		ProtocolPort: member.ProtocolPort,
		MonitorPort:  member.MonitorPort,
		Address:      member.Address,
		Subnet:       member.Subnet.UUID,
	}
}

func loadBalancerPoolMemberRequestEqual(a, b cloudscale.LoadBalancerPoolMemberRequest) bool {
	return comparablePtrEqual(a.Enabled, b.Enabled) &&
		a.ProtocolPort == b.ProtocolPort &&
		a.MonitorPort == b.MonitorPort &&
		a.Address == b.Address &&
		a.Subnet == b.Subnet
}
