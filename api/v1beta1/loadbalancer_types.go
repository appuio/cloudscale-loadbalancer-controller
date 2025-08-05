package v1beta1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LoadBalancerSpec defines the desired state of LoadBalancer
type LoadBalancerSpec struct {
	// LoadBalancerUUID uniquely identifies the loadbalancer. This field
	// should not be provided by the customer, unless the adoption of an
	// existing load balancer is desired.
	//
	// In all other cases, this value is set by the CCM after creating the
	// load balancer, to ensure that we track it with a proper ID and not
	// a name that might change without our knowledge.
	UUID string `json:"uuid,omitempty"`

	// Zone denotes the availability zone where the load balancer is deployed.
	// This defaults to the zone of the Nodes (if there is only one).
	//
	// This can not be changed once the LoadBalancer custom resource is created.
	Zone string `json:"zone,omitempty"`

	// VirtualIPAddresses defines the virtual IP addresses through which
	// incoming traffic is received. this defaults to an automatically assigned
	// public IPv4 and IPv6 address.
	//
	// If you want to use a specific private subnet instead, to load balance
	// inside your cluster, you have to specify the subnet the loadbalancer
	// should bind to, and optionally what IP address it should use (if you
	// don't want an automatically assigned one).
	//
	// Currently limited to one virtual IP address per load balancer.
	// See: https://www.cloudscale.ch/en/api/v1#vip_addresses-attribute-specification
	//
	//+kubebuilder:validation:MaxItems=1
	VirtualIPAddresses []VirtualIPAddress `json:"virtualIPAddresses,omitempty"`

	// FloatingIPAddresses assigns the given Floating IPs to the
	// load balancer. The expected value is a list of addresses of the
	// Floating IPs in CIDR notation. For example:
	//
	// ["5.102.150.123/32", "2a06:c01::123/128"]
	//
	// Floating IPs already assigned to the loadbalancer, but no longer
	// present in this field, stay on the loadbalancer until another
	// service requests them. This is due to the fact that it is not possible
	// to unassign Floating IPs to point to nowhere.
	//
	// The Floating IPs are only assigned to the LoadBalancer once it has
	// been fully created.
	FloatingIPAddresses []FloatingIPAddress `json:"floatingIPAddresses,omitempty"`

	// Pools defines the pools that are part of the load balancer.
	Pools []Pool `json:"pools,omitempty"`
}

type Pool struct {
	// Name is the name of the pool.
	// Changing the name of the pool will cause short downtime as the pool is recreated.
	//+required
	Name string `json:"name"`

	// Algorithm defines the algorithm used to distribute traffic across
	// the backend servers.
	// This can be one of `RoundRobin`, `LeastConnections`, or `SourceIP`.
	// * `RoundRobin` distributes traffic evenly across all servers.
	// * `LeastConnections` sends traffic to the server with the least active
	// connections.
	// * `SourceIP` uses the source IP address to determine which server to
	// send the traffic to. `SourceIP` maintains session persistence across
	// multiple requests and is useful for applications that require it.
	//
	// The default is `RoundRobin`.
	//
	// Changing the algorithm of the pool will cause short downtime as the pool is recreated.
	//
	//+kubebuilder:validation:Enum=RoundRobin;LeastConnections;SourceIP
	//+kubebuilder:default="RoundRobin"
	Algorithm string `json:"algorithm,omitempty"`

	// Frontend configures the frontend of the pool.
	Frontend PoolFrontend `json:"frontend,omitempty"`

	// Backend configures the backend of the pool.
	Backend PoolBackend `json:"backend,omitempty"`
}

type PoolFrontend struct {
	// Protocol defines the protocol used by the listener.
	// Currently, only `TCP` is supported.
	// Defaults to `TCP`.
	//+kubebuilder:validation:Enum=TCP
	//+kubebuilder:default="TCP"
	Protocol string `json:"protocol,omitempty"`

	// Port is the port on which the listener will accept traffic.
	//+required
	Port int32 `json:"port"`

	// AllowedCIDRs defines the source IPs that are allowed to access the listener.
	// This is a list of source IP addresses in CIDR notation, for example:
	// ["1.1.1.1", "104.132.228.0/24"]
	// If not specified, all source IP addresses are allowed to access the
	// listener.
	// If specified, only the source IP addresses that match the CIDRs
	// in this list are allowed to access the listener.
	AllowedCIDRs []AllowedCIDR `json:"allowedCIDRs,omitempty"`

	// DataTimeout defines the milliseconds until inactive client connections are dropped.
	// Defaults to 50 seconds.
	//+kubebuilder:validation:Type=string
	//+kubebuilder:validation:Format=duration
	//+kubebuilder:default="50s"
	DataTimeout metav1.Duration `json:"dataTimeout,omitempty"`
}

func (l PoolFrontend) GetProtocol() string {
	if l.Protocol == "" {
		return "TCP"
	}
	return l.Protocol
}

func (l PoolFrontend) GetDataTimeout() time.Duration {
	if l.DataTimeout.Duration == 0 {
		return 50 * time.Second
	}
	return l.DataTimeout.Duration
}

type PoolBackend struct {
	// Port is the port on which the traffic is sent on the backend servers.
	//+required
	Port int32 `json:"port"`

	// Protocol defines the protocol used by the backend servers.
	// This can be one of `TCP`, `Proxy`, or `ProxyV2`.
	// * `TCP` no connection information is sent to the backend servers.
	// * `Proxy` wraps the connection in a proxy protocol header.
	//   The load balancer sends an initial series of octets describing the
	//   connection to the backend server.
	// * `ProxyV2` is similar to `Proxy`, but uses a different version of the
	//   proxy protocol header.
	//
	// The default is `TCP`.
	//
	// Changing the protocol of the pool will cause short downtime as the pool is recreated.
	//
	//+kubebuilder:validation:Enum=TCP;Proxy;ProxyV2
	//+kubebuilder:default="TCP"
	Protocol string `json:"protocol,omitempty"`

	// HealthMonitor defines the health check configuration for the load balancer.
	HealthMonitor HealthMonitor `json:"healthMonitor,omitempty"`

	// NodeSelector defines the node selector for the load balancer.
	// The controller automatically updates the pool members to match
	// the nodes matching this selector.
	NodeSelector metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// DataTimeout defines the milliseconds until inactive backend connections are dropped.
	// Defaults to 50 seconds.
	//+kubebuilder:validation:Type=string
	//+kubebuilder:validation:Format=duration
	//+kubebuilder:default="50s"
	DataTimeout metav1.Duration `json:"dataTimeout,omitempty"`

	// ConnectTimeout denotes the time it should maximally take to connect to a backend server
	// before the connection is considered failed.
	// Defaults to 5 seconds.
	//+kubebuilder:validation:Type=string
	//+kubebuilder:validation:Format=duration
	//+kubebuilder:default="5s"
	ConnectTimeout metav1.Duration `json:"connectTimeout,omitempty"`

	// AllowedSubnets is a JSON list of subnet UUIDs that the
	// loadbalancer should use. By default, all subnets of a node are used.
	//
	// If set, nodes without a matching subnet are ignored.
	AllowedSubnets []AllowedSubnet `json:"allowedSubnets,omitempty"`
}

func (p PoolBackend) GetProtocol() string {
	if p.Protocol == "" {
		return "TCP"
	}
	return p.Protocol
}

func (p Pool) GetAlgorithm() string {
	if p.Algorithm == "" {
		return "RoundRobin"
	}
	return p.Algorithm
}

// AllowedCIDR defines a CIDR that is allowed to access the listener.
type AllowedCIDR struct {
	// CIDR is the source IP address in CIDR notation.
	// For example: "192.168.1.0/24"
	CIDR string `json:"cidr"`
}

type AllowedSubnet struct {
	// UUID is the UUID of the subnet that should be used for the load balancer.
	UUID string `json:"uuid"`
}

func (l PoolBackend) GetDataTimeout() time.Duration {
	if l.DataTimeout.Duration == 0 {
		return 50 * time.Second
	}
	return l.DataTimeout.Duration
}

func (l PoolBackend) GetConnectTimeout() time.Duration {
	if l.ConnectTimeout.Duration == 0 {
		return 5 * time.Second
	}
	return l.ConnectTimeout.Duration
}

// VirtualIPAddress defines a virtual IP address for the load balancer.
type VirtualIPAddress struct {
	// Address is the actual IP address that should be assigned to the loadbalancer.
	Address string `json:"address,omitempty"`

	// SubnetID is the ID of the subnet where this IP address resides.
	//+required
	SubnetID string `json:"subnetID"`
}

// FloatingIPAddress defines a floating IP address for the load balancer.
type FloatingIPAddress struct {
	// CIDR is the actual floating IP address that should be assigned to the loadbalancer in CIDR notation.
	CIDR string `json:"cidr"`
}

// HealthMonitor defines the health check configuration for the load balancer.
type HealthMonitor struct {
	// Port is the port on which the health check is performed.
	// If not specified the health check is performed on the port which is configured for the pool backend.
	Port int32 `json:"port,omitempty"`

	// Type defines the type of health check to perform.
	// This can be one of `Ping`,`TCP`, `HTTP`, `HTTPS`, or `TLSHello`.
	// Defaults to `TCP`.
	//+kubebuilder:default="TCP"
	//+kubebuilder:validation:Enum=Ping;TCP;HTTP;HTTPS;TLSHello
	Type string `json:"type,omitempty"`

	// Delay is the time between health checks.
	// Defaults to 2 seconds.
	//+kubebuilder:validation:Type=string
	//+kubebuilder:validation:Format=duration
	//+kubebuilder:default="2s"
	Delay metav1.Duration `json:"delay,omitempty"`

	// Timeout is the time to wait for a response from the backend server.
	// Defaults to 1 second.
	//+kubebuilder:validation:Type=string
	//+kubebuilder:validation:Format=duration
	//+kubebuilder:default="1s"
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// SuccessThreshold is the number of consecutive successful health checks
	// required before a backend server is considered healthy.
	// Defaults to 2.
	//+kubebuilder:validation:Minimum=1
	//+kubebuilder:default=2
	SuccessThreshold int `json:"successThreshold,omitempty"`

	// FailureThreshold is the number of consecutive failed health checks
	// required before a backend server is considered unhealthy.
	// Defaults to 3.
	//+kubebuilder:validation:Minimum=1
	//+kubebuilder:default=3
	FailureThreshold int `json:"failureThreshold,omitempty"`

	// HTTP defines the HTTP health check configuration.
	HTTP HTTPHealthMonitor `json:"http,omitempty"`
}

func (h HealthMonitor) GetType() string {
	if h.Type == "" {
		return "TCP"
	}
	return h.Type
}

func (h HealthMonitor) GetDelay() time.Duration {
	if h.Delay.Duration == 0 {
		return 2 * time.Second
	}
	return h.Delay.Duration
}

func (h HealthMonitor) GetTimeout() time.Duration {
	if h.Timeout.Duration == 0 {
		return 1 * time.Second
	}
	return h.Timeout.Duration
}

func (h HealthMonitor) GetUpThreshold() int {
	if h.SuccessThreshold == 0 {
		return 2
	}
	return h.SuccessThreshold
}

func (h HealthMonitor) GetDownThreshold() int {
	if h.FailureThreshold == 0 {
		return 3
	}
	return h.FailureThreshold
}

// HTTPHealthMonitor defines the HTTP health check configuration.
type HTTPHealthMonitor struct {
	// Method is the HTTP method to use for the health check.
	// This can be one of `CONNECT`, `DELETE`, `GET`, `HEAD`, `OPTIONS`, `PATCH`,
	// `POST`, `PUT`, or `TRACE`.
	// Defaults to `GET`.
	//+kubebuilder:validation:Enum=CONNECT;DELETE;GET;HEAD;OPTIONS;PATCH;POST;PUT;TRACE
	Method string `json:"method,omitempty"`

	// Path is the URL path to check.
	// Defaults to `/`.
	Path string `json:"path,omitempty"`

	// Host is the HTTP `Host:` header to use for the health check.
	Host string `json:"host,omitempty"`

	// Version is the HTTP version to use for the health check.
	// This can be one of `1.0`or `1.1`.
	// Defaults to `1.1`.
	//+kubebuilder:validation:Enum="1.0";"1.1"
	Version string `json:"version,omitempty"`

	// StatusCodes is a list of HTTP status codes that indicate a healthy backend.
	// This can either be a list of status codes or a single range of status codes.
	// For example, `200`, `201`, `202`, or `200-299`.
	// Defaults to `[200]`.
	StatusCodes []string `json:"statusCodes,omitempty"`
}

func (h HTTPHealthMonitor) GetMethod() string {
	if h.Method == "" {
		return "GET"
	}
	return h.Method
}

func (h HTTPHealthMonitor) GetPath() string {
	if h.Path == "" {
		return "/"
	}
	return h.Path
}

func (h HTTPHealthMonitor) GetVersion() string {
	if h.Version == "" {
		return "1.1"
	}
	return h.Version
}

func (h HTTPHealthMonitor) GetStatusCodes() []string {
	if len(h.StatusCodes) == 0 {
		return []string{"200"}
	}
	return h.StatusCodes
}

// LoadBalancerStatus defines the observed state of LoadBalancer
type LoadBalancerStatus struct {
	// CloudscaleStatus is the raw status of the load balancer as reported by Cloudscale.
	CloudscaleStatus string `json:"cloudscaleStatus,omitempty"`

	// VirtualIPAddresses contains the IP addresses through which the load balancer can be accessed.
	VirtualIPAddresses []StatusVirtualIPAddress `json:"virtualIPAddresses,omitempty"`

	// FloatingIPAddresses contains the floating IP addresses that are assigned to the load balancer.
	// This is a list of IP addresses in CIDR notation.
	FloatingIPAddresses []StatusFloatingIPAddress `json:"floatingIPAddresses,omitempty"`

	// Pools contains the status of the pools that are part of the load balancer.
	Pools []StatusPool `json:"pools,omitempty"`
}

type StatusPool struct {
	// Name is the name of the pool.
	Name string `json:"name,omitempty"`

	// Backends contains all nodes that are currently part of the load balancer.
	Backends []StatusBackend `json:"backends,omitempty"`
}

type StatusVirtualIPAddress struct {
	// Address is the IP address of the frontend.
	Address string `json:"address,omitempty"`
}

type StatusFloatingIPAddress struct {
	// CIDR is the floating IP address in CIDR notation.
	CIDR string `json:"cidr,omitempty"`
}

// StatusBackend defines a backend node in the load balancer status.
type StatusBackend struct {
	// NodeName is the name of the node.
	NodeName string `json:"nodeName,omitempty"`
	// ServerName is the name of the server.
	ServerName string `json:"serverName,omitempty"`
	// Address is the IP address of the backend node.
	Address string `json:"address,omitempty"`
	// Status is the status of the backend node as reported by the health monitor.
	Status string `json:"status,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// LoadBalancer is the Schema for the LoadBalancers API
type LoadBalancer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LoadBalancerSpec   `json:"spec,omitempty"`
	Status LoadBalancerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// LoadBalancerList contains a list of LoadBalancer
type LoadBalancerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LoadBalancer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LoadBalancer{}, &LoadBalancerList{})
}
