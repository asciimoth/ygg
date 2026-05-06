package outproxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/sockstun"
)

type ProxyConfig = sockstun.ProxyConfig

type Logger = logger.Logger

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
	log             Logger
}

func newRouteNetwork(direct gonnect.Network, cfgs []ProxyConfig, defaultProxyURL string, logs ...Logger) (*routeNetwork, error) {
	var log Logger
	if len(logs) > 0 {
		log = logs[0]
	}
	if log == nil {
		log = logger.Discard()
	}
	n := &routeNetwork{direct: direct, log: log}
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
			return fmt.Errorf("outproxy proxy filter must not be empty")
		}
		if cfg.ProxyURL == "" {
			return fmt.Errorf("outproxy proxy url must not be empty")
		}
		client, err := socksgo.ClientFromURL(cfg.ProxyURL)
		if err != nil {
			return fmt.Errorf("invalid outproxy proxy url %q: %w", cfg.ProxyURL, err)
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
			return fmt.Errorf("invalid default outproxy proxy url %q: %w", defaultProxyURL, err)
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
	n.log.Debugf("outproxy routing configured proxy_routes=%d default_proxy=%q", len(routes), defaultProxyURL)
	for i, route := range routes {
		n.log.Debugf("outproxy routing proxy_route index=%d filter=%q proxy_url=%q", i, route.cfg.Filter, route.cfg.ProxyURL)
	}
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
	return n.networkFor("Dial", network, address).Dial(ctx, network, address)
}

func (n *routeNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return n.networkFor("Listen", network, address).Listen(ctx, network, address)
}

func (n *routeNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.networkFor("PacketDial", network, address).PacketDial(ctx, network, address)
}

func (n *routeNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.networkFor("ListenPacket", network, address).ListenPacket(ctx, network, address)
}

func (n *routeNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	return n.networkFor("DialTCP", network, raddr).DialTCP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenTCP(ctx context.Context, network, laddr string) (gonnect.TCPListener, error) {
	return n.networkFor("ListenTCP", network, laddr).ListenTCP(ctx, network, laddr)
}

func (n *routeNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	return n.networkFor("DialUDP", network, raddr).DialUDP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	return n.networkFor("ListenUDP", network, laddr).ListenUDP(ctx, network, laddr)
}

func (n *routeNetwork) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupIP(ctx, network, host)
	}
	return nil, noSuchHost(host)
}

func (n *routeNetwork) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupIPAddr(ctx, host)
	}
	return nil, noSuchHost(host)
}

func (n *routeNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupNetIP(ctx, network, host)
	}
	return nil, noSuchHost(host)
}

func (n *routeNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupHost(ctx, host)
	}
	return nil, noSuchHost(host)
}

func (n *routeNetwork) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupAddr(ctx, addr)
	}
	return nil, noSuchHost(addr)
}

func (n *routeNetwork) LookupCNAME(ctx context.Context, host string) (string, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupCNAME(ctx, host)
	}
	return "", noSuchHost(host)
}

func (n *routeNetwork) LookupPort(ctx context.Context, network, service string) (int, error) {
	return gonnect.LookupPortOffline(network, service)
}

func (n *routeNetwork) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupNS(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupMX(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupSRV(ctx, service, proto, name)
	}
	return "", nil, noSuchHost(name)
}

func (n *routeNetwork) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupTXT(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) networkFor(op, network, address string) gonnect.Network {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, route := range n.routes {
		if route.filter(network, address) {
			n.log.Debugf("outproxy routing %s network=%q address=%q selected=proxy filter=%q proxy_url=%q", op, network, address, route.cfg.Filter, route.cfg.ProxyURL)
			return route.network
		}
	}
	if n.defaultProxy != nil {
		n.log.Debugf("outproxy routing %s network=%q address=%q selected=default-proxy proxy_url=%q", op, network, address, n.defaultProxyURL)
		return n.defaultProxy
	}
	n.log.Debugf("outproxy routing %s network=%q address=%q selected=direct", op, network, address)
	return n.direct
}

func noSuchHost(host string) error {
	return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}
