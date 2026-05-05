package sockstun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
	gonnecthelpers "github.com/asciimoth/gonnect/helpers"
	"github.com/asciimoth/mnlib"
	"github.com/asciimoth/socksgo"
)

var defaultNoResolveZones = []string{".onion", ".i2p", ".loki"}

type ProxyConfig struct {
	Filter   string `json:"filter,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
}

type DNSConfig struct {
	FallbackServer string   `json:"fallback_server,omitempty"`
	NoResolveZones []string `json:"no_resolve_zones,omitempty"`
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
	dns             DNSConfig
	noResolveZones  []string
	resolver        gonnect.Resolver
}

func newRouteNetwork(direct gonnect.Network, cfgs []ProxyConfig, defaultProxyURL string, dns DNSConfig) (*routeNetwork, error) {
	n := &routeNetwork{direct: direct}
	if err := n.SetProxies(cfgs, defaultProxyURL); err != nil {
		return nil, err
	}
	if err := n.SetDNS(dns); err != nil {
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

func (n *routeNetwork) SetDNS(cfg DNSConfig) error {
	cfg.FallbackServer = strings.TrimSpace(cfg.FallbackServer)
	cfg.NoResolveZones = normalizeNoResolveZones(cfg.NoResolveZones)

	resolver := mnlib.NewResolver(n)
	if cfg.FallbackServer != "" {
		fallback := gonnect.ResolverCfg{
			Dial: n.dialUnresolved,
			Server: &gonnect.DnsServer{
				Addr: cfg.FallbackServer,
			},
		}.Build()
		resolver.Fallback = &fallback
	}

	n.mu.Lock()
	n.dns = cfg
	n.noResolveZones = append(append([]string{}, defaultNoResolveZones...), cfg.NoResolveZones...)
	n.resolver = resolver
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

func (n *routeNetwork) DNS() DNSConfig {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return DNSConfig{
		FallbackServer: n.dns.FallbackServer,
		NoResolveZones: append([]string{}, n.dns.NoResolveZones...),
	}
}

func (n *routeNetwork) IsNative() bool {
	return false
}

func (n *routeNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	address = n.resolveAddress(ctx, network, address)
	return n.dialUnresolved(ctx, network, address)
}

func (n *routeNetwork) dialUnresolved(ctx context.Context, network, address string) (net.Conn, error) {
	return n.networkFor(network, address).Dial(ctx, network, address)
}

func (n *routeNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	address = n.resolveAddress(ctx, network, address)
	return n.networkFor(network, address).Listen(ctx, network, address)
}

func (n *routeNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	address = n.resolveAddress(ctx, network, address)
	return n.networkFor(network, address).PacketDial(ctx, network, address)
}

func (n *routeNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	address = n.resolveAddress(ctx, network, address)
	return n.networkFor(network, address).ListenPacket(ctx, network, address)
}

func (n *routeNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	raddr = n.resolveAddress(ctx, network, raddr)
	return n.networkFor(network, raddr).DialTCP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenTCP(ctx context.Context, network, laddr string) (gonnect.TCPListener, error) {
	laddr = n.resolveAddress(ctx, network, laddr)
	return n.networkFor(network, laddr).ListenTCP(ctx, network, laddr)
}

func (n *routeNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	raddr = n.resolveAddress(ctx, network, raddr)
	return n.networkFor(network, raddr).DialUDP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	laddr = n.resolveAddress(ctx, network, laddr)
	return n.networkFor(network, laddr).ListenUDP(ctx, network, laddr)
}

func (n *routeNetwork) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	host = normalizeHost(host)
	if host == "" {
		return nil, noSuchHost(host)
	}
	if ip := net.ParseIP(host); ip != nil {
		ips := filterIPsByNetwork([]net.IP{ip}, network)
		if len(ips) == 0 {
			return nil, noSuchHost(host)
		}
		return ips, nil
	}
	if !n.shouldSkipResolve(host) {
		if ips, err := n.lookupIPViaRouteResolver(ctx, network, host); err == nil && len(ips) > 0 {
			return ips, nil
		}
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupIP(ctx, network, host)
	}
	return nil, noSuchHost(host)
}

func (n *routeNetwork) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	ips, err := n.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: ip})
	}
	return out, nil
}

func (n *routeNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	ips, err := n.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr.Unmap())
		}
	}
	if len(out) == 0 {
		return nil, noSuchHost(host)
	}
	return out, nil
}

func (n *routeNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
	ips, err := n.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

func (n *routeNetwork) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	if names, err := n.lookupAddrViaRouteResolver(ctx, addr); err == nil && len(names) > 0 {
		return names, nil
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupAddr(ctx, addr)
	}
	return nil, noSuchHost(addr)
}

func (n *routeNetwork) LookupCNAME(ctx context.Context, host string) (string, error) {
	if !n.shouldSkipResolve(host) {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if cname, err := resolver.LookupCNAME(ctx, host); err == nil && cname != "" {
				return cname, nil
			}
		}
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupCNAME(ctx, host)
	}
	return "", noSuchHost(host)
}

func (n *routeNetwork) LookupPort(ctx context.Context, network, service string) (int, error) {
	return gonnect.LookupPortOffline(network, service)
}

func (n *routeNetwork) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	if !n.shouldSkipResolve(name) {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if records, err := resolver.LookupNS(ctx, name); err == nil && len(records) > 0 {
				return records, nil
			}
		}
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupNS(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	if !n.shouldSkipResolve(name) {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if records, err := resolver.LookupMX(ctx, name); err == nil && len(records) > 0 {
				return records, nil
			}
		}
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupMX(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if !n.shouldSkipResolve(name) {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			cname, records, err := resolver.LookupSRV(ctx, service, proto, name)
			if err == nil && len(records) > 0 {
				return cname, records, nil
			}
		}
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupSRV(ctx, service, proto, name)
	}
	return "", nil, noSuchHost(name)
}

func (n *routeNetwork) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if !n.shouldSkipResolve(name) {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if records, err := resolver.LookupTXT(ctx, name); err == nil && len(records) > 0 {
				return records, nil
			}
		}
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupTXT(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) resolveAddress(ctx context.Context, network, address string) string {
	host, port, ok := splitAddress(address)
	if !ok || host == "" || isIPLiteral(host) || n.shouldSkipResolve(host) {
		return address
	}

	n.mu.RLock()
	resolver := n.resolver
	n.mu.RUnlock()
	if resolver == nil {
		return address
	}

	ips, err := resolver.LookupIP(ctx, gonnecthelpers.FamilyFromNetwork(network), host)
	if err != nil || len(ips) == 0 {
		return address
	}
	ip := gonnecthelpers.PickIP(ips, preferFamily(network))
	if ip == nil {
		return address
	}
	if port == "" {
		return ip.String()
	}
	return net.JoinHostPort(ip.String(), port)
}

func (n *routeNetwork) lookupIPViaRouteResolver(ctx context.Context, network, host string) ([]net.IP, error) {
	n.mu.RLock()
	resolver := n.resolver
	n.mu.RUnlock()
	if resolver == nil {
		return nil, noSuchHost(host)
	}
	ips, err := resolver.LookupIP(ctx, gonnecthelpers.FamilyFromNetwork(network), host)
	if err != nil {
		return nil, err
	}
	ips = filterIPsByNetwork(ips, network)
	if len(ips) == 0 {
		return nil, noSuchHost(host)
	}
	return ips, nil
}

func (n *routeNetwork) lookupAddrViaRouteResolver(ctx context.Context, addr string) ([]string, error) {
	n.mu.RLock()
	resolver := n.resolver
	n.mu.RUnlock()
	if resolver == nil {
		return nil, noSuchHost(addr)
	}
	return resolver.LookupAddr(ctx, addr)
}

func (n *routeNetwork) shouldSkipResolve(host string) bool {
	host = normalizeHost(host)
	n.mu.RLock()
	zones := append([]string{}, n.noResolveZones...)
	n.mu.RUnlock()
	for _, zone := range zones {
		if host == strings.TrimPrefix(zone, ".") || strings.HasSuffix(host, zone) {
			return true
		}
	}
	return false
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

func splitAddress(address string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(address)
	if err == nil {
		return strings.Trim(host, "[]"), port, true
	}
	if strings.Count(address, ":") > 1 {
		if addr, err := netip.ParseAddr(strings.Trim(address, "[]")); err == nil {
			return addr.String(), "", true
		}
		return "", "", false
	}
	return strings.Trim(address, "[]"), "", true
}

func isIPLiteral(host string) bool {
	_, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil
}

func filterIPsByNetwork(ips []net.IP, network string) []net.IP {
	family := gonnecthelpers.FamilyFromNetwork(network)
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			if family == "ip" || family == "ip4" {
				out = append(out, ip)
			}
			continue
		}
		if ip.To16() != nil && (family == "ip" || family == "ip6") {
			out = append(out, ip)
		}
	}
	return out
}

func noSuchHost(host string) error {
	return &net.DNSError{
		Err:        "no such host",
		Name:       host,
		IsNotFound: true,
	}
}

func normalizeHost(host string) string {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	return strings.TrimSuffix(host, ".")
}

func normalizeNoResolveZones(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = normalizeHost(strings.TrimPrefix(strings.TrimSpace(value), "*"))
		value = strings.TrimPrefix(value, ".")
		if value == "" {
			continue
		}
		zone := "." + value
		if _, ok := seen[zone]; ok {
			continue
		}
		seen[zone] = struct{}{}
		out = append(out, zone)
	}
	return out
}

func preferFamily(network string) int {
	switch gonnecthelpers.FamilyFromNetwork(network) {
	case "ip4":
		return 4
	case "ip6":
		return 6
	default:
		return 0
	}
}
