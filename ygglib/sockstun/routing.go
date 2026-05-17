package sockstun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/mnlib"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/ygg/ygglib/logger"
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
	dns             DNSConfig
	noResolveZones  []string
	resolver        gonnect.Resolver
	log             Logger
}

func newRouteNetwork(direct gonnect.Network, cfgs []ProxyConfig, defaultProxyURL string, dns DNSConfig, logs ...Logger) (*routeNetwork, error) {
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
	n.log.Debugf("sockstun routing configured proxy_routes=%d default_proxy=%q", len(routes), defaultProxyURL)
	for i, route := range routes {
		n.log.Debugf("sockstun routing proxy_route index=%d filter=%q proxy_url=%q", i, route.cfg.Filter, route.cfg.ProxyURL)
	}
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
	zones := append([]string{}, n.noResolveZones...)
	n.mu.Unlock()
	n.log.Debugf("sockstun DNS configured fallback_server=%q no_resolve_zones=%q", cfg.FallbackServer, strings.Join(zones, ","))
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
	address = n.resolveAddress(ctx, "Dial", network, address)
	return n.dialUnresolved(ctx, network, address)
}

func (n *routeNetwork) dialUnresolved(ctx context.Context, network, address string) (net.Conn, error) {
	return n.networkFor("Dial", network, address).Dial(ctx, network, address)
}

func (n *routeNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	address = n.resolveAddress(ctx, "Listen", network, address)
	return n.networkFor("Listen", network, address).Listen(ctx, network, address)
}

func (n *routeNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	address = n.resolveAddress(ctx, "PacketDial", network, address)
	return n.networkFor("PacketDial", network, address).PacketDial(ctx, network, address)
}

func (n *routeNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	address = n.resolveAddress(ctx, "ListenPacket", network, address)
	return n.networkFor("ListenPacket", network, address).ListenPacket(ctx, network, address)
}

func (n *routeNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	raddr = n.resolveAddress(ctx, "DialTCP", network, raddr)
	return n.networkFor("DialTCP", network, raddr).DialTCP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenTCP(ctx context.Context, network, laddr string) (gonnect.TCPListener, error) {
	laddr = n.resolveAddress(ctx, "ListenTCP", network, laddr)
	return n.networkFor("ListenTCP", network, laddr).ListenTCP(ctx, network, laddr)
}

func (n *routeNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	raddr = n.resolveAddress(ctx, "DialUDP", network, raddr)
	return n.networkFor("DialUDP", network, raddr).DialUDP(ctx, network, laddr, raddr)
}

func (n *routeNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	laddr = n.resolveAddress(ctx, "ListenUDP", network, laddr)
	return n.networkFor("ListenUDP", network, laddr).ListenUDP(ctx, network, laddr)
}

func (n *routeNetwork) ListenPacketConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.PacketConn, error) {
	address = n.resolveAddress(ctx, "ListenPacketConfig", network, address)
	return n.networkFor("ListenPacketConfig", network, address).ListenPacketConfig(ctx, lc, network, address)
}

func (n *routeNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, laddr string) (gonnect.UDPConn, error) {
	laddr = n.resolveAddress(ctx, "ListenUDPConfig", network, laddr)
	return n.networkFor("ListenUDPConfig", network, laddr).ListenUDPConfig(ctx, lc, network, laddr)
}

func (n *routeNetwork) ListenMulticastUDP(ctx context.Context, network, address string, opts gonnect.MulticastOptions) (gonnect.MulticastPacketConn, error) {
	address = n.resolveAddress(ctx, "ListenMulticastUDP", network, address)
	return n.networkFor("ListenMulticastUDP", network, address).ListenMulticastUDP(ctx, network, address, opts)
}

func (n *routeNetwork) Interfaces() ([]gonnect.NetworkInterface, error) {
	return n.direct.Interfaces()
}

func (n *routeNetwork) InterfaceAddrs() ([]net.Addr, error) {
	return n.direct.InterfaceAddrs()
}

func (n *routeNetwork) InterfaceMulticastAddrs() ([]net.Addr, error) {
	return n.direct.InterfaceMulticastAddrs()
}

func (n *routeNetwork) InterfacesByIndex(index int) ([]gonnect.NetworkInterface, error) {
	return n.direct.InterfacesByIndex(index)
}

func (n *routeNetwork) InterfacesByName(name string) ([]gonnect.NetworkInterface, error) {
	return n.direct.InterfacesByName(name)
}

func (n *routeNetwork) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	originalHost := host
	host = normalizeHost(host)
	n.log.Debugf("sockstun resolver LookupIP start network=%q host=%q normalized_host=%q", network, originalHost, host)
	if host == "" {
		n.log.Debugf("sockstun resolver LookupIP failed network=%q host=%q reason=empty-host", network, originalHost)
		return nil, noSuchHost(host)
	}
	if ip := net.ParseIP(host); ip != nil {
		ips := filterIPsByNetwork([]net.IP{ip}, network)
		if len(ips) == 0 {
			n.log.Debugf("sockstun resolver LookupIP literal filtered network=%q host=%q ip=%q", network, host, ip.String())
			return nil, noSuchHost(host)
		}
		n.log.Debugf("sockstun resolver LookupIP literal network=%q host=%q ips=%q", network, host, formatIPs(ips))
		return ips, nil
	}
	if skip, zone := n.shouldSkipResolve(host); !skip {
		if ips, err := n.lookupIPViaRouteResolver(ctx, network, host); err == nil && len(ips) > 0 {
			n.log.Debugf("sockstun resolver LookupIP route-resolver hit network=%q host=%q ips=%q", network, host, formatIPs(ips))
			return ips, nil
		} else if err != nil {
			n.log.Debugf("sockstun resolver LookupIP route-resolver miss network=%q host=%q err=%v", network, host, err)
		} else {
			n.log.Debugf("sockstun resolver LookupIP route-resolver miss network=%q host=%q reason=empty-result", network, host)
		}
	} else {
		n.log.Debugf("sockstun resolver LookupIP skipped route-resolver network=%q host=%q zone=%q", network, host, zone)
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		ips, err := resolver.LookupIP(ctx, network, host)
		if err != nil {
			n.log.Debugf("sockstun resolver LookupIP direct failed network=%q host=%q err=%v", network, host, err)
			return nil, err
		}
		n.log.Debugf("sockstun resolver LookupIP direct hit network=%q host=%q ips=%q", network, host, formatIPs(ips))
		return ips, nil
	}
	n.log.Debugf("sockstun resolver LookupIP failed network=%q host=%q reason=no-direct-resolver", network, host)
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
		n.log.Debugf("sockstun resolver LookupAddr route-resolver hit addr=%q names=%q", addr, strings.Join(names, ","))
		return names, nil
	} else if err != nil {
		n.log.Debugf("sockstun resolver LookupAddr route-resolver miss addr=%q err=%v", addr, err)
	} else {
		n.log.Debugf("sockstun resolver LookupAddr route-resolver miss addr=%q reason=empty-result", addr)
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		names, err := resolver.LookupAddr(ctx, addr)
		if err != nil {
			n.log.Debugf("sockstun resolver LookupAddr direct failed addr=%q err=%v", addr, err)
			return nil, err
		}
		n.log.Debugf("sockstun resolver LookupAddr direct hit addr=%q names=%q", addr, strings.Join(names, ","))
		return names, nil
	}
	n.log.Debugf("sockstun resolver LookupAddr failed addr=%q reason=no-direct-resolver", addr)
	return nil, noSuchHost(addr)
}

func (n *routeNetwork) LookupCNAME(ctx context.Context, host string) (string, error) {
	if skip, zone := n.shouldSkipResolve(host); !skip {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if cname, err := resolver.LookupCNAME(ctx, host); err == nil && cname != "" {
				n.log.Debugf("sockstun resolver LookupCNAME route-resolver hit host=%q cname=%q", host, cname)
				return cname, nil
			} else if err != nil {
				n.log.Debugf("sockstun resolver LookupCNAME route-resolver miss host=%q err=%v", host, err)
			} else {
				n.log.Debugf("sockstun resolver LookupCNAME route-resolver miss host=%q reason=empty-result", host)
			}
		} else {
			n.log.Debugf("sockstun resolver LookupCNAME skipped route-resolver host=%q reason=no-resolver", host)
		}
	} else {
		n.log.Debugf("sockstun resolver LookupCNAME skipped route-resolver host=%q zone=%q", host, zone)
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
	if skip, zone := n.shouldSkipResolve(name); !skip {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if records, err := resolver.LookupNS(ctx, name); err == nil && len(records) > 0 {
				n.log.Debugf("sockstun resolver LookupNS route-resolver hit name=%q records=%d", name, len(records))
				return records, nil
			} else if err != nil {
				n.log.Debugf("sockstun resolver LookupNS route-resolver miss name=%q err=%v", name, err)
			} else {
				n.log.Debugf("sockstun resolver LookupNS route-resolver miss name=%q reason=empty-result", name)
			}
		} else {
			n.log.Debugf("sockstun resolver LookupNS skipped route-resolver name=%q reason=no-resolver", name)
		}
	} else {
		n.log.Debugf("sockstun resolver LookupNS skipped route-resolver name=%q zone=%q", name, zone)
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupNS(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	if skip, zone := n.shouldSkipResolve(name); !skip {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if records, err := resolver.LookupMX(ctx, name); err == nil && len(records) > 0 {
				n.log.Debugf("sockstun resolver LookupMX route-resolver hit name=%q records=%d", name, len(records))
				return records, nil
			} else if err != nil {
				n.log.Debugf("sockstun resolver LookupMX route-resolver miss name=%q err=%v", name, err)
			} else {
				n.log.Debugf("sockstun resolver LookupMX route-resolver miss name=%q reason=empty-result", name)
			}
		} else {
			n.log.Debugf("sockstun resolver LookupMX skipped route-resolver name=%q reason=no-resolver", name)
		}
	} else {
		n.log.Debugf("sockstun resolver LookupMX skipped route-resolver name=%q zone=%q", name, zone)
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupMX(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	if skip, zone := n.shouldSkipResolve(name); !skip {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			cname, records, err := resolver.LookupSRV(ctx, service, proto, name)
			if err == nil && len(records) > 0 {
				n.log.Debugf("sockstun resolver LookupSRV route-resolver hit service=%q proto=%q name=%q cname=%q records=%d", service, proto, name, cname, len(records))
				return cname, records, nil
			} else if err != nil {
				n.log.Debugf("sockstun resolver LookupSRV route-resolver miss service=%q proto=%q name=%q err=%v", service, proto, name, err)
			} else {
				n.log.Debugf("sockstun resolver LookupSRV route-resolver miss service=%q proto=%q name=%q reason=empty-result", service, proto, name)
			}
		} else {
			n.log.Debugf("sockstun resolver LookupSRV skipped route-resolver service=%q proto=%q name=%q reason=no-resolver", service, proto, name)
		}
	} else {
		n.log.Debugf("sockstun resolver LookupSRV skipped route-resolver service=%q proto=%q name=%q zone=%q", service, proto, name, zone)
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupSRV(ctx, service, proto, name)
	}
	return "", nil, noSuchHost(name)
}

func (n *routeNetwork) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if skip, zone := n.shouldSkipResolve(name); !skip {
		n.mu.RLock()
		resolver := n.resolver
		n.mu.RUnlock()
		if resolver != nil {
			if records, err := resolver.LookupTXT(ctx, name); err == nil && len(records) > 0 {
				n.log.Debugf("sockstun resolver LookupTXT route-resolver hit name=%q records=%d", name, len(records))
				return records, nil
			} else if err != nil {
				n.log.Debugf("sockstun resolver LookupTXT route-resolver miss name=%q err=%v", name, err)
			} else {
				n.log.Debugf("sockstun resolver LookupTXT route-resolver miss name=%q reason=empty-result", name)
			}
		} else {
			n.log.Debugf("sockstun resolver LookupTXT skipped route-resolver name=%q reason=no-resolver", name)
		}
	} else {
		n.log.Debugf("sockstun resolver LookupTXT skipped route-resolver name=%q zone=%q", name, zone)
	}
	if resolver, ok := n.direct.(gonnect.Resolver); ok {
		return resolver.LookupTXT(ctx, name)
	}
	return nil, noSuchHost(name)
}

func (n *routeNetwork) resolveAddress(ctx context.Context, op, network, address string) string {
	host, port, ok := splitAddress(address)
	if !ok {
		n.log.Debugf("sockstun resolver %s address not resolved network=%q address=%q reason=split-failed", op, network, address)
		return address
	}
	if host == "" {
		n.log.Debugf("sockstun resolver %s address not resolved network=%q address=%q reason=empty-host", op, network, address)
		return address
	}
	if isIPLiteral(host) {
		n.log.Debugf("sockstun resolver %s address not resolved network=%q address=%q host=%q reason=ip-literal", op, network, address, host)
		return address
	}
	if skip, zone := n.shouldSkipResolve(host); skip {
		n.log.Debugf("sockstun resolver %s address not resolved network=%q address=%q host=%q zone=%q", op, network, address, host, zone)
		return address
	}

	n.mu.RLock()
	resolver := n.resolver
	n.mu.RUnlock()
	if resolver == nil {
		n.log.Debugf("sockstun resolver %s address not resolved network=%q address=%q host=%q reason=no-resolver", op, network, address, host)
		return address
	}

	n.log.Debugf("sockstun resolver %s resolving address network=%q address=%q host=%q family=%q", op, network, address, host, gonnect.FamilyFromNetwork(network))
	ips, err := resolver.LookupIP(ctx, gonnect.FamilyFromNetwork(network), host)
	if err != nil || len(ips) == 0 {
		if err != nil {
			n.log.Debugf("sockstun resolver %s address lookup failed network=%q address=%q host=%q err=%v", op, network, address, host, err)
		} else {
			n.log.Debugf("sockstun resolver %s address lookup failed network=%q address=%q host=%q reason=empty-result", op, network, address, host)
		}
		return address
	}
	ip := gonnect.PickIP(ips, preferFamily(network))
	if ip == nil {
		n.log.Debugf("sockstun resolver %s address lookup had no preferred IP network=%q address=%q host=%q ips=%q", op, network, address, host, formatIPs(ips))
		return address
	}
	if port == "" {
		n.log.Debugf("sockstun resolver %s address rewritten network=%q from=%q to=%q ips=%q", op, network, address, ip.String(), formatIPs(ips))
		return ip.String()
	}
	resolved := net.JoinHostPort(ip.String(), port)
	n.log.Debugf("sockstun resolver %s address rewritten network=%q from=%q to=%q ips=%q", op, network, address, resolved, formatIPs(ips))
	return resolved
}

func (n *routeNetwork) lookupIPViaRouteResolver(ctx context.Context, network, host string) ([]net.IP, error) {
	n.mu.RLock()
	resolver := n.resolver
	n.mu.RUnlock()
	if resolver == nil {
		return nil, noSuchHost(host)
	}
	ips, err := resolver.LookupIP(ctx, gonnect.FamilyFromNetwork(network), host)
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

func (n *routeNetwork) shouldSkipResolve(host string) (bool, string) {
	host = normalizeHost(host)
	n.mu.RLock()
	zones := append([]string{}, n.noResolveZones...)
	n.mu.RUnlock()
	for _, zone := range zones {
		if host == strings.TrimPrefix(zone, ".") || strings.HasSuffix(host, zone) {
			return true, zone
		}
	}
	return false, ""
}

func (n *routeNetwork) networkFor(op, network, address string) gonnect.Network {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, route := range n.routes {
		if route.filter(network, address) {
			n.log.Debugf("sockstun routing %s network=%q address=%q selected=proxy filter=%q proxy_url=%q", op, network, address, route.cfg.Filter, route.cfg.ProxyURL)
			return route.network
		}
	}
	if n.defaultProxy != nil && !isYggdrasilAddress(address) {
		n.log.Debugf("sockstun routing %s network=%q address=%q selected=default-proxy proxy_url=%q", op, network, address, n.defaultProxyURL)
		return n.defaultProxy
	}
	n.log.Debugf("sockstun routing %s network=%q address=%q selected=direct yggdrasil_address=%t", op, network, address, isYggdrasilAddress(address))
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
	family := gonnect.FamilyFromNetwork(network)
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

func formatIPs(ips []net.IP) string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		out = append(out, ip.String())
	}
	return strings.Join(out, ",")
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
	switch gonnect.FamilyFromNetwork(network) {
	case "ip4":
		return 4
	case "ip6":
		return 6
	default:
		return 0
	}
}
