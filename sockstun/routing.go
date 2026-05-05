package sockstun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/socksgo"
)

type ProxyConfig struct {
	Filter   string `json:"filter,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
}

type proxyRoute struct {
	cfg     ProxyConfig
	filter  gonnect.Filter
	network gonnect.Network
}

type routeNetwork struct {
	direct gonnect.Network

	mu              sync.RWMutex
	routes          []proxyRoute
	defaultProxyURL string
	defaultProxy    gonnect.Network
}

func newRouteNetwork(direct gonnect.Network, cfgs []ProxyConfig, defaultProxyURL string) (*routeNetwork, error) {
	n := &routeNetwork{direct: direct}
	if err := n.SetProxies(cfgs, defaultProxyURL); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *routeNetwork) SetProxies(cfgs []ProxyConfig, defaultProxyURL string) error {
	routes := make([]proxyRoute, 0, len(cfgs))
	for _, cfg := range cfgs {
		cfg.Filter = strings.TrimSpace(cfg.Filter)
		cfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
		if cfg.Filter == "" {
			return fmt.Errorf("sockstun proxy filter must not be empty")
		}
		if cfg.ProxyURL == "" {
			return fmt.Errorf("sockstun proxy url must not be empty")
		}
		client, err := socksgo.ClientFromURL(cfg.ProxyURL)
		if err != nil {
			return fmt.Errorf("invalid sockstun proxy url %q: %w", cfg.ProxyURL, err)
		}
		client.WithNetwork(n.direct)
		client.Filter = gonnect.FalseFilter
		routes = append(routes, proxyRoute{
			cfg:     cfg,
			filter:  socksgo.BuildFilter(cfg.Filter),
			network: client,
		})
	}

	var defaultProxy gonnect.Network
	defaultProxyURL = strings.TrimSpace(defaultProxyURL)
	if defaultProxyURL != "" {
		client, err := socksgo.ClientFromURL(defaultProxyURL)
		if err != nil {
			return fmt.Errorf("invalid default sockstun proxy url %q: %w", defaultProxyURL, err)
		}
		client.WithNetwork(n.direct)
		client.Filter = gonnect.FalseFilter
		defaultProxy = client
	}

	n.mu.Lock()
	n.routes = routes
	n.defaultProxyURL = defaultProxyURL
	n.defaultProxy = defaultProxy
	n.mu.Unlock()
	return nil
}

func (n *routeNetwork) Proxies() []ProxyConfig {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]ProxyConfig, 0, len(n.routes))
	for _, route := range n.routes {
		out = append(out, route.cfg)
	}
	return out
}

func (n *routeNetwork) DefaultProxyURL() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.defaultProxyURL
}

func (n *routeNetwork) IsNative() bool {
	return false
}

func (n *routeNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return n.networkFor(network, address).Dial(ctx, network, address)
}

func (n *routeNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return n.networkFor(network, address).Listen(ctx, network, address)
}

func (n *routeNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.networkFor(network, address).PacketDial(ctx, network, address)
}

func (n *routeNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.networkFor(network, address).ListenPacket(ctx, network, address)
}

func (n *routeNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	return n.networkFor(network, raddr).DialTCP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenTCP(ctx context.Context, network, laddr string) (gonnect.TCPListener, error) {
	return n.networkFor(network, laddr).ListenTCP(ctx, network, laddr)
}

func (n *routeNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	return n.networkFor(network, raddr).DialUDP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	return n.networkFor(network, laddr).ListenUDP(ctx, network, laddr)
}

func (n *routeNetwork) networkFor(network, address string) gonnect.Network {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, route := range n.routes {
		if route.filter(network, address) {
			return route.network
		}
	}
	if n.defaultProxy != nil && !isYggdrasilAddress(address) {
		return n.defaultProxy
	}
	return n.direct
}

func isYggdrasilAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	host = strings.Trim(host, "[]")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return netip.MustParsePrefix("200::/7").Contains(addr)
}
