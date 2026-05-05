package admin

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/ygg/ygglib/address"
	"github.com/asciimoth/ygg/ygglib/autopeer"
	"github.com/asciimoth/ygg/ygglib/multicast"
	"github.com/asciimoth/ygg/ygglib/tun"
)

type GetNodeInfoRequest struct {
	Key string `json:"key"`
}

type GetNodeInfoResponse map[string]json.RawMessage

type DebugRemoteGetRequest struct {
	Key string `json:"key"`
}

type DebugKeys struct {
	Keys []string `json:"keys"`
}

type DebugGetSelfResponse map[string]json.RawMessage
type DebugGetPeersResponse map[string]DebugKeys
type DebugGetTreeResponse map[string]DebugKeys

type GetMulticastInterfacesResponse struct {
	Interfaces []multicast.InterfaceState `json:"multicast_interfaces"`
}

type GetAutoPeerResponse struct {
	Enabled                   bool            `json:"enabled"`
	Active                    bool            `json:"active"`
	Sources                   []string        `json:"sources"`
	FetchInterval             string          `json:"fetch_interval"`
	CheckInterval             string          `json:"check_interval"`
	MinimumConnected          int             `json:"minimum_connected"`
	MinimumConnectedFromFetch int             `json:"minimum_connected_from_fetch"`
	Countries                 []string        `json:"countries"`
	TransportSchemes          []string        `json:"transport_schemes"`
	Peers                     []autopeer.Peer `json:"peers"`
}

type SetAutoPeerRequest struct {
	Enabled                   string `json:"enabled,omitempty"`
	Sources                   string `json:"sources,omitempty"`
	FetchInterval             string `json:"fetch_interval,omitempty"`
	CheckInterval             string `json:"check_interval,omitempty"`
	MinimumConnected          string `json:"minimum_connected,omitempty"`
	MinimumConnectedFromFetch string `json:"minimum_connected_from_fetch,omitempty"`
	Countries                 string `json:"countries,omitempty"`
	TransportSchemes          string `json:"transport_schemes,omitempty"`
}

type RefreshAutoPeerRequest struct {
	Source string `json:"source,omitempty"`
}

type AutoPeerController struct {
	mu      sync.RWMutex
	manager *autopeer.Manager
	enabled bool
}

func NewAutoPeerController(manager *autopeer.Manager, enabled bool) *AutoPeerController {
	if manager == nil {
		return nil
	}
	return &AutoPeerController{
		manager: manager,
		enabled: enabled,
	}
}

func (c *AutoPeerController) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

func (c *AutoPeerController) Snapshot() *GetAutoPeerResponse {
	if c == nil || c.manager == nil {
		return nil
	}
	managerCfg := c.manager.Config()
	fetcher := c.manager.Fetcher()
	res := &GetAutoPeerResponse{
		Enabled:                   c.Enabled(),
		Active:                    c.manager.IsStarted(),
		CheckInterval:             managerCfg.CheckInterval.String(),
		MinimumConnected:          managerCfg.MinimumConnected,
		MinimumConnectedFromFetch: managerCfg.MinimumConnectedFromFetch,
		Countries:                 managerCfg.Countries,
		TransportSchemes:          managerCfg.TransportSchemes,
		Peers:                     c.manager.Peers(),
	}
	if fetcher != nil {
		res.Sources = fetcher.Sources()
		res.FetchInterval = fetcher.Interval().String()
	}
	return res
}

func (c *AutoPeerController) Apply(req *SetAutoPeerRequest) error {
	if c == nil || c.manager == nil {
		return fmt.Errorf("autopeer controller not configured")
	}

	fetcher := c.manager.Fetcher()
	if fetcher == nil {
		return fmt.Errorf("autopeer fetcher not configured")
	}

	managerCfg := c.manager.Config()
	if req.CheckInterval != "" {
		d, err := time.ParseDuration(req.CheckInterval)
		if err != nil {
			return fmt.Errorf("invalid check_interval: %w", err)
		}
		managerCfg.CheckInterval = d
	}
	if req.MinimumConnected != "" {
		v, err := strconv.Atoi(req.MinimumConnected)
		if err != nil {
			return fmt.Errorf("invalid minimum_connected: %w", err)
		}
		managerCfg.MinimumConnected = v
	}
	if req.MinimumConnectedFromFetch != "" {
		v, err := strconv.Atoi(req.MinimumConnectedFromFetch)
		if err != nil {
			return fmt.Errorf("invalid minimum_connected_from_fetch: %w", err)
		}
		managerCfg.MinimumConnectedFromFetch = v
	}
	if req.Countries != "" {
		managerCfg.Countries = splitAdminCSV(req.Countries)
	}
	if req.TransportSchemes != "" {
		managerCfg.TransportSchemes = splitAdminCSV(req.TransportSchemes)
	}

	if req.FetchInterval != "" {
		d, err := time.ParseDuration(req.FetchInterval)
		if err != nil {
			return fmt.Errorf("invalid fetch_interval: %w", err)
		}
		fetcher.SetInterval(d)
	}
	if req.Sources != "" {
		fetcher.SetSources(splitAdminCSV(req.Sources))
	}

	c.manager.SetConfig(managerCfg)

	if req.Enabled != "" {
		enabled, err := strconv.ParseBool(req.Enabled)
		if err != nil {
			return fmt.Errorf("invalid enabled: %w", err)
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		if !enabled && c.enabled && c.manager.IsStarted() {
			return fmt.Errorf("disabling autopeer at runtime is not supported")
		}
		c.enabled = enabled
		if enabled && !c.manager.IsStarted() {
			c.manager.Start()
		}
	}

	return nil
}

func (c *AutoPeerController) Refresh(source string) error {
	if c == nil || c.manager == nil || c.manager.Fetcher() == nil {
		return fmt.Errorf("autopeer controller not configured")
	}
	ctx := context.Background()
	fetcher := c.manager.Fetcher()
	if strings.TrimSpace(source) != "" {
		return fetcher.FetchNow(ctx, strings.TrimSpace(source))
	}
	for _, source := range fetcher.Sources() {
		if err := fetcher.FetchNow(ctx, source); err != nil {
			return err
		}
	}
	return nil
}

func splitAdminCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func (a *AdminSocket) SetupAutoPeerHandlers(c *AutoPeerController) {
	if c == nil {
		return
	}
	_ = a.AddHandler(
		"getAutoPeer", "Show autopeer configuration, state and fetched peers", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			res := c.Snapshot()
			if res == nil {
				return nil, fmt.Errorf("autopeer controller not configured")
			}
			return res, nil
		},
	)
	_ = a.AddHandler(
		"setAutoPeer", "Update runtime autopeer configuration", []string{
			"enabled", "sources", "fetch_interval", "check_interval",
			"minimum_connected", "minimum_connected_from_fetch", "countries", "transport_schemes",
		},
		func(in json.RawMessage) (interface{}, error) {
			req := &SetAutoPeerRequest{}
			if err := json.Unmarshal(in, req); err != nil {
				return nil, err
			}
			if err := c.Apply(req); err != nil {
				return nil, err
			}
			return c.Snapshot(), nil
		},
	)
	_ = a.AddHandler(
		"refreshAutoPeer", "Refresh autopeer sources immediately", []string{"source"},
		func(in json.RawMessage) (interface{}, error) {
			req := &RefreshAutoPeerRequest{}
			if err := json.Unmarshal(in, req); err != nil {
				return nil, err
			}
			if err := c.Refresh(req.Source); err != nil {
				return nil, err
			}
			return c.Snapshot(), nil
		},
	)
}

func (a *AdminSocket) SetupMulticastHandlers(m *multicast.Multicast) {
	_ = a.AddHandler(
		"getMulticastInterfaces", "Show which interfaces multicast is enabled on", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			return &GetMulticastInterfacesResponse{
				Interfaces: m.InterfaceStates(),
			}, nil
		},
	)
}

type TunController interface {
	Attach(req AttachTunRequest, replace bool) error
	Detach() error
}

type TunSocksProxyController interface {
	GetSocksProxies() TunSocksProxyRouting
	SetSocksProxies(TunSocksProxyRouting) error
}

type TunSocksDNSController interface {
	GetSocksDNS() TunSocksDNSConfig
	SetSocksDNS(TunSocksDNSConfig) error
}

type AttachTunRequest struct {
	Type              string `json:"type,omitempty"`
	Name              string `json:"name,omitempty"`
	MTU               string `json:"mtu,omitempty"`
	SocksListen       string `json:"socks_listen,omitempty"`
	SocksProxies      string `json:"socks_proxies,omitempty"`
	SocksDefaultProxy string `json:"socks_default_proxy,omitempty"`
	SocksDNSFallback  string `json:"socks_dns_fallback,omitempty"`
	SocksNoResolve    string `json:"socks_no_resolve,omitempty"`
	MWO               string `json:"mwo,omitempty"`
	MRO               string `json:"mro,omitempty"`
}

type TunSocksProxyConfig struct {
	Filter   string `json:"filter,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
}

type GetTunSocksProxiesResponse struct {
	Proxies         []TunSocksProxyConfig `json:"proxies"`
	DefaultProxyURL string                `json:"default_proxy_url,omitempty"`
}

type SetTunSocksProxiesRequest struct {
	Proxies         string `json:"proxies,omitempty"`
	DefaultProxyURL string `json:"default_proxy_url,omitempty"`
}

type TunSocksProxyRouting struct {
	Proxies         []TunSocksProxyConfig
	DefaultProxyURL string
}

type GetTunSocksDNSResponse struct {
	FallbackServer string   `json:"fallback_server,omitempty"`
	NoResolveZones []string `json:"no_resolve_zones"`
}

type SetTunSocksDNSRequest struct {
	FallbackServer string `json:"fallback_server,omitempty"`
	NoResolveZones string `json:"no_resolve_zones,omitempty"`
}

type TunSocksDNSConfig struct {
	FallbackServer string
	NoResolveZones []string
}

func (a *AdminSocket) SetupTunHandlers(t *tun.TunAdapter, controllers ...TunController) {
	var controller TunController
	if len(controllers) > 0 {
		controller = controllers[0]
	}
	_ = a.AddHandler(
		"getTun", "Show information about the node's TUN interface", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			return t.Status(), nil
		},
	)
	if controller == nil {
		return
	}
	_ = a.AddHandler(
		"attachTun", "Attach a TUN implementation", []string{"type", "name", "mtu", "socks_listen", "socks_proxies", "socks_default_proxy", "socks_dns_fallback", "socks_no_resolve", "mwo", "mro"},
		func(in json.RawMessage) (interface{}, error) {
			req := AttachTunRequest{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := controller.Attach(req, false); err != nil {
				return nil, err
			}
			return t.Status(), nil
		},
	)
	_ = a.AddHandler(
		"replaceTun", "Replace the active TUN implementation", []string{"type", "name", "mtu", "socks_listen", "socks_proxies", "socks_default_proxy", "socks_dns_fallback", "socks_no_resolve", "mwo", "mro"},
		func(in json.RawMessage) (interface{}, error) {
			req := AttachTunRequest{}
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			if err := controller.Attach(req, true); err != nil {
				return nil, err
			}
			return t.Status(), nil
		},
	)
	_ = a.AddHandler(
		"detachTun", "Detach the active TUN implementation", []string{},
		func(_ json.RawMessage) (interface{}, error) {
			if err := controller.Detach(); err != nil {
				return nil, err
			}
			return t.Status(), nil
		},
	)
	if proxyController, ok := controller.(TunSocksProxyController); ok {
		_ = a.AddHandler(
			"getTunSocksProxies", "Show sockstun second-hop SOCKS proxy routing", []string{},
			func(_ json.RawMessage) (interface{}, error) {
				routing := proxyController.GetSocksProxies()
				return GetTunSocksProxiesResponse{
					Proxies:         routing.Proxies,
					DefaultProxyURL: routing.DefaultProxyURL,
				}, nil
			},
		)
		_ = a.AddHandler(
			"setTunSocksProxies", "Replace sockstun second-hop SOCKS proxy routing", []string{"proxies", "default_proxy_url"},
			func(in json.RawMessage) (interface{}, error) {
				req := SetTunSocksProxiesRequest{}
				if err := json.Unmarshal(in, &req); err != nil {
					return nil, err
				}
				proxies := []TunSocksProxyConfig{}
				if strings.TrimSpace(req.Proxies) != "" {
					if err := json.Unmarshal([]byte(req.Proxies), &proxies); err != nil {
						return nil, fmt.Errorf("proxies: %w", err)
					}
				}
				if err := proxyController.SetSocksProxies(TunSocksProxyRouting{
					Proxies:         proxies,
					DefaultProxyURL: strings.TrimSpace(req.DefaultProxyURL),
				}); err != nil {
					return nil, err
				}
				routing := proxyController.GetSocksProxies()
				return GetTunSocksProxiesResponse{
					Proxies:         routing.Proxies,
					DefaultProxyURL: routing.DefaultProxyURL,
				}, nil
			},
		)
	}
	if dnsController, ok := controller.(TunSocksDNSController); ok {
		_ = a.AddHandler(
			"getTunSocksDNS", "Show sockstun DNS resolution settings", []string{},
			func(_ json.RawMessage) (interface{}, error) {
				cfg := dnsController.GetSocksDNS()
				return GetTunSocksDNSResponse{
					FallbackServer: cfg.FallbackServer,
					NoResolveZones: cfg.NoResolveZones,
				}, nil
			},
		)
		_ = a.AddHandler(
			"setTunSocksDNS", "Replace sockstun DNS resolution settings", []string{"fallback_server", "no_resolve_zones"},
			func(in json.RawMessage) (interface{}, error) {
				req := SetTunSocksDNSRequest{}
				if err := json.Unmarshal(in, &req); err != nil {
					return nil, err
				}
				zones := []string{}
				if strings.TrimSpace(req.NoResolveZones) != "" {
					if err := json.Unmarshal([]byte(req.NoResolveZones), &zones); err != nil {
						return nil, fmt.Errorf("no_resolve_zones: %w", err)
					}
				}
				if err := dnsController.SetSocksDNS(TunSocksDNSConfig{
					FallbackServer: strings.TrimSpace(req.FallbackServer),
					NoResolveZones: zones,
				}); err != nil {
					return nil, err
				}
				cfg := dnsController.GetSocksDNS()
				return GetTunSocksDNSResponse{
					FallbackServer: cfg.FallbackServer,
					NoResolveZones: cfg.NoResolveZones,
				}, nil
			},
		)
	}
}

func keyToIP(keyHex string) string {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return keyHex
	}
	addr := address.AddrForKey(ed25519.PublicKey(key))
	return net.IP(addr[:]).String()
}
