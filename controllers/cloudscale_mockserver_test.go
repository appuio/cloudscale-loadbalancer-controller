package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cloudscale-ch/cloudscale-go-sdk/v6"
	"github.com/google/uuid"
	"k8s.io/utils/ptr"
)

type cloudscaleMockServer struct {
	ServersMux                   sync.RWMutex
	Servers                      []cloudscale.Server
	FloatingIPsMux               sync.RWMutex
	FloatingIPs                  []cloudscale.FloatingIP
	LoadBalancerMux              sync.RWMutex
	LoadBalancers                []cloudscale.LoadBalancer
	LoadBalancerHealthMonitorMux sync.RWMutex
	LoadBalancerHealthMonitors   []cloudscale.LoadBalancerHealthMonitor
	LoadBalancerListenerMux      sync.RWMutex
	LoadBalancerListeners        []cloudscale.LoadBalancerListener
	LoadBalancerPoolsMux         sync.RWMutex
	LoadBalancerPools            []cloudscale.LoadBalancerPool
	LoadBalancerPoolMemberMux    sync.RWMutex
	LoadBalancerPoolMembers      map[string][]cloudscale.LoadBalancerPoolMember
}

func (m *cloudscaleMockServer) GetServers() []cloudscale.Server {
	m.ServersMux.RLock()
	defer m.ServersMux.RUnlock()
	return slices.Clone(m.Servers)
}

func (m *cloudscaleMockServer) AddServers(s ...cloudscale.Server) {
	m.ServersMux.Lock()
	defer m.ServersMux.Unlock()
	m.Servers = append(m.Servers, s...)
}

func (m *cloudscaleMockServer) GetFloatingIPs() []cloudscale.FloatingIP {
	m.FloatingIPsMux.RLock()
	defer m.FloatingIPsMux.RUnlock()
	return slices.Clone(m.FloatingIPs)
}

func (m *cloudscaleMockServer) AddFloatingIPs(fips ...cloudscale.FloatingIP) {
	m.FloatingIPsMux.Lock()
	defer m.FloatingIPsMux.Unlock()
	m.FloatingIPs = append(m.FloatingIPs, fips...)
}

func (m *cloudscaleMockServer) GetLoadBalancers() []cloudscale.LoadBalancer {
	m.LoadBalancerMux.RLock()
	defer m.LoadBalancerMux.RUnlock()
	return slices.Clone(m.LoadBalancers)
}

func (m *cloudscaleMockServer) AddLoadBalancers(lbs ...cloudscale.LoadBalancer) {
	m.LoadBalancerMux.Lock()
	defer m.LoadBalancerMux.Unlock()
	m.LoadBalancers = append(m.LoadBalancers, lbs...)
}

func (m *cloudscaleMockServer) SetLoadBalancerStatus(id, status string) error {
	m.LoadBalancerMux.Lock()
	defer m.LoadBalancerMux.Unlock()

	for i, lb := range m.LoadBalancers {
		if lb.UUID == id {
			m.LoadBalancers[i].Status = status
			return nil
		}
	}
	return fmt.Errorf("load balancer %q not found", id)
}

func (m *cloudscaleMockServer) GetLoadBalancerHealthMonitors() []cloudscale.LoadBalancerHealthMonitor {
	m.LoadBalancerHealthMonitorMux.RLock()
	defer m.LoadBalancerHealthMonitorMux.RUnlock()
	return slices.Clone(m.LoadBalancerHealthMonitors)
}

func (m *cloudscaleMockServer) GetLoadBalancerHealthMonitorsByPool(poolID string) []cloudscale.LoadBalancerHealthMonitor {
	m.LoadBalancerHealthMonitorMux.RLock()
	defer m.LoadBalancerHealthMonitorMux.RUnlock()

	var monitors []cloudscale.LoadBalancerHealthMonitor
	for _, monitor := range m.LoadBalancerHealthMonitors {
		if monitor.Pool.UUID == poolID {
			monitors = append(monitors, monitor)
		}
	}
	return monitors
}

func (m *cloudscaleMockServer) GetLoadBalancerListeners() []cloudscale.LoadBalancerListener {
	m.LoadBalancerListenerMux.RLock()
	defer m.LoadBalancerListenerMux.RUnlock()
	return slices.Clone(m.LoadBalancerListeners)
}

func (m *cloudscaleMockServer) GetLoadBalancerListenersByPool(poolID string) []cloudscale.LoadBalancerListener {
	m.LoadBalancerListenerMux.RLock()
	defer m.LoadBalancerListenerMux.RUnlock()

	var listeners []cloudscale.LoadBalancerListener
	for _, listener := range m.LoadBalancerListeners {
		if listener.Pool.UUID == poolID {
			listeners = append(listeners, listener)
		}
	}
	return listeners
}

func (m *cloudscaleMockServer) GetLoadBalancerPools() []cloudscale.LoadBalancerPool {
	m.LoadBalancerPoolsMux.RLock()
	defer m.LoadBalancerPoolsMux.RUnlock()
	return slices.Clone(m.LoadBalancerPools)
}

func (m *cloudscaleMockServer) GetLoadBalancerPoolMembers(poolID string) []cloudscale.LoadBalancerPoolMember {
	m.LoadBalancerPoolMemberMux.RLock()
	defer m.LoadBalancerPoolMemberMux.RUnlock()
	return slices.Clone(m.LoadBalancerPoolMembers[poolID])
}

func (m *cloudscaleMockServer) Server() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/servers", func(w http.ResponseWriter, r *http.Request) {
		m.ServersMux.RLock()
		defer m.ServersMux.RUnlock()

		respondWithJSON(w, m.Servers)
	})
	mux.HandleFunc("GET /v1/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.ServersMux.RLock()
		defer m.ServersMux.RUnlock()

		id := r.PathValue("id")
		for _, server := range m.Servers {
			if server.UUID == id {
				respondWithJSON(w, server)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/floating-ips", func(w http.ResponseWriter, r *http.Request) {
		m.FloatingIPsMux.RLock()
		defer m.FloatingIPsMux.RUnlock()

		respondWithJSON(w, m.FloatingIPs)
	})
	mux.HandleFunc("PATCH /v1/floating-ips/{ip}", func(w http.ResponseWriter, r *http.Request) {
		m.FloatingIPsMux.Lock()
		defer m.FloatingIPsMux.Unlock()

		ip := r.PathValue("ip")

		var fipReq cloudscale.FloatingIPUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&fipReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		for i, fip := range m.FloatingIPs {
			if strings.TrimSuffix(strings.TrimSuffix(fip.Network, "/32"), "/128") == ip {
				fip.LoadBalancer = &cloudscale.LoadBalancerStub{
					UUID: fipReq.LoadBalancer,
				}
				m.FloatingIPs[i] = fip
				respondWithJSON(w, fip.LoadBalancer)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/load-balancers", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerMux.RLock()
		defer m.LoadBalancerMux.RUnlock()

		respondWithJSON(w, m.LoadBalancers)
	})
	mux.HandleFunc("GET /v1/load-balancers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerMux.RLock()
		defer m.LoadBalancerMux.RUnlock()

		id := r.PathValue("id")
		for _, lb := range m.LoadBalancers {
			if lb.UUID == id {
				respondWithJSON(w, lb)
				return
			}
		}
		httpNotFound(w, r)
	})
	mux.HandleFunc("POST /v1/load-balancers", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerMux.Lock()
		defer m.LoadBalancerMux.Unlock()

		var lbr cloudscale.LoadBalancerRequest
		if err := json.NewDecoder(r.Body).Decode(&lbr); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vipAddresses := make([]cloudscale.VIPAddress, 0)
		for _, vip := range ptr.Deref(lbr.VIPAddresses, []cloudscale.VIPAddressRequest{}) {
			vipAddresses = append(vipAddresses, cloudscale.VIPAddress{
				Address: vip.Address,
			})
		}
		lb := cloudscale.LoadBalancer{
			UUID: uuid.New().String(),
			Name: lbr.Name,
			ZonalResource: cloudscale.ZonalResource{
				Zone: cloudscale.Zone{Slug: lbr.Zone},
			},
			TaggedResource: cloudscale.TaggedResource{
				Tags: ptr.Deref(lbr.Tags, cloudscale.TagMap{}),
			},
			Flavor: cloudscale.LoadBalancerFlavorStub{
				Name: lbr.Flavor,
			},
			VIPAddresses: vipAddresses,
			Status:       "changing",
			CreatedAt:    time.Now(),
		}
		m.LoadBalancers = append(m.LoadBalancers, lb)
		respondWithJSON(w, lb)
	})
	mux.HandleFunc("PATCH /v1/load-balancers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerMux.Lock()
		defer m.LoadBalancerMux.Unlock()

		id := r.PathValue("id")
		var lbr cloudscale.LoadBalancerRequest
		if err := json.NewDecoder(r.Body).Decode(&lbr); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i, lb := range m.LoadBalancers {
			if lb.UUID == id {
				if lbr.Name != "" {
					lb.Name = lbr.Name
				}
				if lbr.Tags != nil {
					lb.Tags = *lbr.Tags
				}
				m.LoadBalancers[i] = lb
				respondWithJSON(w, lb)
				return
			}
		}
		httpNotFound(w, r)
	})
	mux.HandleFunc("DELETE /v1/load-balancers/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerMux.Lock()
		defer m.LoadBalancerMux.Unlock()

		id := r.PathValue("id")
		for i, lb := range m.LoadBalancers {
			if lb.UUID == id {
				m.LoadBalancers = append(m.LoadBalancers[:i], m.LoadBalancers[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/load-balancers/pools", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolsMux.RLock()
		defer m.LoadBalancerPoolsMux.RUnlock()

		respondWithJSON(w, m.LoadBalancerPools)
	})
	mux.HandleFunc("GET /v1/load-balancers/pools/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolsMux.RLock()
		defer m.LoadBalancerPoolsMux.RUnlock()

		id := r.PathValue("id")
		for _, pool := range m.LoadBalancerPools {
			if pool.UUID == id {
				respondWithJSON(w, pool)
				return
			}
		}
		httpNotFound(w, r)
	})
	mux.HandleFunc("POST /v1/load-balancers/pools", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolsMux.Lock()
		defer m.LoadBalancerPoolsMux.Unlock()

		var poolReq cloudscale.LoadBalancerPoolRequest
		if err := json.NewDecoder(r.Body).Decode(&poolReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pool := cloudscale.LoadBalancerPool{
			UUID: uuid.New().String(),
			Name: poolReq.Name,
			TaggedResource: cloudscale.TaggedResource{
				Tags: ptr.Deref(poolReq.Tags, cloudscale.TagMap{}),
			},
			CreatedAt: time.Now(),
			LoadBalancer: cloudscale.LoadBalancerStub{
				UUID: poolReq.LoadBalancer,
			},
			Algorithm: poolReq.Algorithm,
			Protocol:  poolReq.Protocol,
		}
		m.LoadBalancerPools = append(m.LoadBalancerPools, pool)
		respondWithJSON(w, pool)
	})
	mux.HandleFunc("DELETE /v1/load-balancers/pools/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolsMux.Lock()
		defer m.LoadBalancerPoolsMux.Unlock()

		id := r.PathValue("id")
		for i, pool := range m.LoadBalancerPools {
			if pool.UUID == id {
				m.LoadBalancerPools = append(m.LoadBalancerPools[:i], m.LoadBalancerPools[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/load-balancers/pools/{poolID}/members", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolMemberMux.RLock()
		defer m.LoadBalancerPoolMemberMux.RUnlock()

		poolID := r.PathValue("poolID")
		respondWithJSON(w, m.LoadBalancerPoolMembers[poolID])
	})
	mux.HandleFunc("POST /v1/load-balancers/pools/{poolID}/members", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolMemberMux.Lock()
		defer m.LoadBalancerPoolMemberMux.Unlock()

		poolID := r.PathValue("poolID")
		var memberReq cloudscale.LoadBalancerPoolMemberRequest
		if err := json.NewDecoder(r.Body).Decode(&memberReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		member := cloudscale.LoadBalancerPoolMember{
			UUID:         uuid.New().String(),
			Name:         memberReq.Name,
			Address:      memberReq.Address,
			ProtocolPort: memberReq.ProtocolPort,
			Enabled:      ptr.Deref(memberReq.Enabled, true),
			CreatedAt:    time.Now(),
			TaggedResource: cloudscale.TaggedResource{
				Tags: ptr.Deref(memberReq.Tags, cloudscale.TagMap{}),
			},
			Pool: cloudscale.LoadBalancerPoolStub{
				UUID: poolID,
			},
			MonitorPort: memberReq.MonitorPort,
			Subnet: cloudscale.SubnetStub{
				UUID: memberReq.Subnet,
			},
			MonitorStatus: "unknown",
		}
		if m.LoadBalancerPoolMembers == nil {
			m.LoadBalancerPoolMembers = make(map[string][]cloudscale.LoadBalancerPoolMember)
		}
		m.LoadBalancerPoolMembers[poolID] = append(m.LoadBalancerPoolMembers[poolID], member)
		respondWithJSON(w, member)
	})
	mux.HandleFunc("DELETE /v1/load-balancers/pools/{poolID}/members/{memberID}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerPoolMemberMux.Lock()
		defer m.LoadBalancerPoolMemberMux.Unlock()

		poolID := r.PathValue("poolID")
		memberID := r.PathValue("memberID")
		members, ok := m.LoadBalancerPoolMembers[poolID]
		if !ok {
			httpNotFound(w, r)
			return
		}
		for i, member := range members {
			if member.UUID == memberID {
				m.LoadBalancerPoolMembers[poolID] = append(members[:i], members[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/load-balancers/health-monitors", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerHealthMonitorMux.RLock()
		defer m.LoadBalancerHealthMonitorMux.RUnlock()

		respondWithJSON(w, m.LoadBalancerHealthMonitors)
	})
	mux.HandleFunc("GET /v1/load-balancers/health-monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerHealthMonitorMux.RLock()
		defer m.LoadBalancerHealthMonitorMux.RUnlock()

		id := r.PathValue("id")
		for _, hm := range m.LoadBalancerHealthMonitors {
			if hm.UUID == id {
				respondWithJSON(w, hm)
				return
			}
		}
		httpNotFound(w, r)
	})
	mux.HandleFunc("PATCH /v1/load-balancers/health-monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerHealthMonitorMux.Lock()
		defer m.LoadBalancerHealthMonitorMux.Unlock()

		var hmReq cloudscale.LoadBalancerHealthMonitorRequest
		if err := json.NewDecoder(r.Body).Decode(&hmReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		id := r.PathValue("id")
		for i, hm := range m.LoadBalancerHealthMonitors {
			if hm.UUID == id {
				if hmReq.Tags != nil {
					hm.Tags = *hmReq.Tags
				}

				if hmReq.DelayS != 0 {
					hm.DelayS = hmReq.DelayS
				}
				if hmReq.TimeoutS != 0 {
					hm.TimeoutS = hmReq.TimeoutS
				}
				if hmReq.UpThreshold != 0 {
					hm.UpThreshold = hmReq.UpThreshold
				}
				if hmReq.DownThreshold != 0 {
					hm.DownThreshold = hmReq.DownThreshold
				}
				if hmReq.Type != "" {
					hm.Type = hmReq.Type
				}
				if !slices.Contains([]string{"http", "https"}, hm.Type) {
					if hmReq.HTTP != nil {
						http.Error(w, "HTTP health monitor can only be set for HTTP or HTTPS type", http.StatusBadRequest)
						return
					}
					hm.HTTP = nil
				}
				if hmReq.HTTP != nil {
					if hm.HTTP == nil {
						hm.HTTP = &cloudscale.LoadBalancerHealthMonitorHTTP{}
					}
					if len(hmReq.HTTP.ExpectedCodes) > 0 {
						hm.HTTP.ExpectedCodes = hmReq.HTTP.ExpectedCodes
					}
					if hmReq.HTTP.Method != "" {
						hm.HTTP.Method = hmReq.HTTP.Method
					}
					if hmReq.HTTP.UrlPath != "" {
						hm.HTTP.UrlPath = hmReq.HTTP.UrlPath
					}
					if hmReq.HTTP.Version != "" {
						hm.HTTP.Version = hmReq.HTTP.Version
					}
					if ptr.Deref(hmReq.HTTP.Host, "") != "" {
						hm.HTTP.Host = hmReq.HTTP.Host
					}
				}

				m.LoadBalancerHealthMonitors[i] = hm
				respondWithJSON(w, hm)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("POST /v1/load-balancers/health-monitors", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerHealthMonitorMux.Lock()
		defer m.LoadBalancerHealthMonitorMux.Unlock()

		var hmReq cloudscale.LoadBalancerHealthMonitorRequest
		if err := json.NewDecoder(r.Body).Decode(&hmReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var http *cloudscale.LoadBalancerHealthMonitorHTTP
		if hmReq.HTTP != nil {
			http = &cloudscale.LoadBalancerHealthMonitorHTTP{
				ExpectedCodes: hmReq.HTTP.ExpectedCodes,
				Method:        hmReq.HTTP.Method,
				UrlPath:       hmReq.HTTP.UrlPath,
				Version:       hmReq.HTTP.Version,
				Host:          hmReq.HTTP.Host,
			}
		}
		hm := cloudscale.LoadBalancerHealthMonitor{
			UUID: uuid.New().String(),
			TaggedResource: cloudscale.TaggedResource{
				Tags: ptr.Deref(hmReq.Tags, cloudscale.TagMap{}),
			},
			Pool: cloudscale.LoadBalancerPoolStub{
				UUID: hmReq.Pool,
			},
			DelayS:        hmReq.DelayS,
			TimeoutS:      hmReq.TimeoutS,
			UpThreshold:   hmReq.UpThreshold,
			DownThreshold: hmReq.DownThreshold,
			Type:          hmReq.Type,
			HTTP:          http,
			CreatedAt:     time.Now(),
		}
		m.LoadBalancerHealthMonitors = append(m.LoadBalancerHealthMonitors, hm)
		respondWithJSON(w, hm)
	})
	mux.HandleFunc("DELETE /v1/load-balancers/health-monitors/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerHealthMonitorMux.Lock()
		defer m.LoadBalancerHealthMonitorMux.Unlock()

		id := r.PathValue("id")
		for i, hm := range m.LoadBalancerHealthMonitors {
			if hm.UUID == id {
				m.LoadBalancerHealthMonitors = append(m.LoadBalancerHealthMonitors[:i], m.LoadBalancerHealthMonitors[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		httpNotFound(w, r)
	})

	mux.HandleFunc("GET /v1/load-balancers/listeners", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerListenerMux.RLock()
		defer m.LoadBalancerListenerMux.RUnlock()

		respondWithJSON(w, m.LoadBalancerListeners)
	})
	mux.HandleFunc("GET /v1/load-balancers/listeners/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerListenerMux.RLock()
		defer m.LoadBalancerListenerMux.RUnlock()

		id := r.PathValue("id")
		for _, listener := range m.LoadBalancerListeners {
			if listener.UUID == id {
				respondWithJSON(w, listener)
				return
			}
		}
		httpNotFound(w, r)
	})
	mux.HandleFunc("PATCH /v1/load-balancers/listeners/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerListenerMux.Lock()
		defer m.LoadBalancerListenerMux.Unlock()

		var listenerReq cloudscale.LoadBalancerListenerRequest
		if err := json.NewDecoder(r.Body).Decode(&listenerReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		id := r.PathValue("id")
		for i, listener := range m.LoadBalancerListeners {
			if listener.UUID == id {
				if listenerReq.Name != "" {
					listener.Name = listenerReq.Name
				}
				if listenerReq.Tags != nil {
					listener.Tags = *listenerReq.Tags
				}

				if len(ptr.Deref(listenerReq.AllowedCIDRs, nil)) > 0 {
					listener.AllowedCIDRs = *listenerReq.AllowedCIDRs
				}
				if listenerReq.TimeoutClientDataMS != 0 {
					listener.TimeoutClientDataMS = listenerReq.TimeoutClientDataMS
				}
				if listenerReq.TimeoutMemberConnectMS != 0 {
					listener.TimeoutMemberConnectMS = listenerReq.TimeoutMemberConnectMS
				}
				if listenerReq.TimeoutMemberDataMS != 0 {
					listener.TimeoutMemberDataMS = listenerReq.TimeoutMemberDataMS
				}
				m.LoadBalancerListeners[i] = listener
				respondWithJSON(w, listener)
				return
			}
		}
		httpNotFound(w, r)
	})
	mux.HandleFunc("POST /v1/load-balancers/listeners", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerListenerMux.Lock()
		defer m.LoadBalancerListenerMux.Unlock()

		var listenerReq cloudscale.LoadBalancerListenerRequest
		if err := json.NewDecoder(r.Body).Decode(&listenerReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		listener := cloudscale.LoadBalancerListener{
			UUID:      uuid.New().String(),
			CreatedAt: time.Now(),
			Name:      listenerReq.Name,
			TaggedResource: cloudscale.TaggedResource{
				Tags: ptr.Deref(listenerReq.Tags, cloudscale.TagMap{}),
			},
			Pool: &cloudscale.LoadBalancerPoolStub{
				UUID: listenerReq.Pool,
			},
			Protocol:               listenerReq.Protocol,
			ProtocolPort:           listenerReq.ProtocolPort,
			AllowedCIDRs:           ptr.Deref(listenerReq.AllowedCIDRs, []string{}),
			TimeoutClientDataMS:    listenerReq.TimeoutClientDataMS,
			TimeoutMemberConnectMS: listenerReq.TimeoutMemberConnectMS,
			TimeoutMemberDataMS:    listenerReq.TimeoutMemberDataMS,
		}
		m.LoadBalancerListeners = append(m.LoadBalancerListeners, listener)
		respondWithJSON(w, listener)
	})
	mux.HandleFunc("DELETE /v1/load-balancers/listeners/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.LoadBalancerListenerMux.Lock()
		defer m.LoadBalancerListenerMux.Unlock()

		id := r.PathValue("id")
		for i, listener := range m.LoadBalancerListeners {
			if listener.UUID == id {
				m.LoadBalancerListeners = append(m.LoadBalancerListeners[:i], m.LoadBalancerListeners[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		httpNotFound(w, r)
	})

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Request received:", r.Method, r.URL)
		mux.ServeHTTP(w, r)
	}))
}

func respondWithJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func httpNotFound(w http.ResponseWriter, r *http.Request) {
	fmt.Println("404 Not found:", r.Method, r.URL)
	// The cloudscale go client expects valid JSON for all error responses, otherwise the response code gets lost.
	http.Error(w, "{}", http.StatusNotFound)
}
