package sockstun

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/socksgo"
)

func TestCreateStartsLocalSocksListener(t *testing.T) {
	tun, err := Create(Config{
		Name:    "sockstun-test",
		Listen:  "127.0.0.1:0",
		Address: net.ParseIP("200::1"),
		MTU:     1400,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	addr := tun.SocksAddr()
	if addr == nil {
		t.Fatal("expected socks listener address")
	}
	conn, err := net.Dial(addr.Network(), addr.String())
	if err != nil {
		t.Fatalf("dial socks listener: %v", err)
	}
	_ = conn.Close()

	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := net.Dial(addr.Network(), addr.String()); err == nil {
		t.Fatal("expected socks listener to be closed")
	}
}

func TestSocksServerUsesAllSocksgoDefaultCommandHandlers(t *testing.T) {
	network := &routeNetwork{}
	server, err := newSocksServer(Config{
		Address: net.ParseIP("200::1"),
	}, network)
	if err != nil {
		t.Fatalf("newSocksServer: %v", err)
	}

	if server.Handlers != nil {
		t.Fatal("expected nil handler map so socksgo default handlers are used")
	}
	if server.Resolver != network {
		t.Fatal("expected socks server RESOLVE commands to use route network resolver")
	}
	for cmd := range socksgo.DefaultCommandHandlers {
		if server.GetHandler(cmd) == nil {
			t.Fatalf("missing socksgo default handler for %s", cmd)
		}
	}
}

func TestRouteNetworkResolvesBeforeRouting(t *testing.T) {
	direct := &recordNetwork{}
	n, err := newRouteNetwork(direct, nil, "", DNSConfig{})
	if err != nil {
		t.Fatalf("newRouteNetwork: %v", err)
	}
	n.mu.Lock()
	n.resolver = fakeResolver{ips: []net.IP{net.ParseIP("200::1234")}}
	n.mu.Unlock()

	_, err = n.Dial(context.Background(), "tcp", "mesh.example:80")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if direct.lastAddress != "[200::1234]:80" {
		t.Fatalf("address was not rewritten by DNS, dialed %q", direct.lastAddress)
	}
}

func TestRouteNetworkLookupIPUsesSameResolverPath(t *testing.T) {
	direct := &recordNetwork{}
	n, err := newRouteNetwork(direct, nil, "", DNSConfig{})
	if err != nil {
		t.Fatalf("newRouteNetwork: %v", err)
	}
	n.mu.Lock()
	n.resolver = fakeResolver{ips: []net.IP{net.ParseIP("200::1234"), net.ParseIP("127.0.0.1")}}
	n.mu.Unlock()

	ips, err := n.LookupIP(context.Background(), "tcp6", "mesh.example")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("200::1234")) {
		t.Fatalf("LookupIP returned %#v, want only 200::1234", ips)
	}
}

func TestRouteNetworkLookupIPFallsBackToDirectResolver(t *testing.T) {
	direct := &recordNetwork{
		resolverIPs: []net.IP{net.ParseIP("200::abcd")},
	}
	n, err := newRouteNetwork(direct, nil, "", DNSConfig{})
	if err != nil {
		t.Fatalf("newRouteNetwork: %v", err)
	}
	n.mu.Lock()
	n.resolver = fakeResolver{err: &net.DNSError{Err: "no such host", Name: "custom.example", IsNotFound: true}}
	n.mu.Unlock()

	ips, err := n.LookupIP(context.Background(), "ip6", "custom.example")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("200::abcd")) {
		t.Fatalf("LookupIP returned %#v, want direct resolver result", ips)
	}
}

func TestRouteNetworkSkipsProtectedZones(t *testing.T) {
	direct := &recordNetwork{}
	n, err := newRouteNetwork(direct, nil, "", DNSConfig{NoResolveZones: []string{"*.alt"}})
	if err != nil {
		t.Fatalf("newRouteNetwork: %v", err)
	}
	resolver := &countingResolver{fakeResolver: fakeResolver{ips: []net.IP{net.ParseIP("200::1234")}}}
	n.mu.Lock()
	n.resolver = resolver
	n.mu.Unlock()

	for _, address := range []string{"hidden.onion:80", "peer.i2p:80", "svc.loki:80", "name.alt:80"} {
		if _, err := n.Dial(context.Background(), "tcp", address); err != nil {
			t.Fatalf("Dial(%q): %v", address, err)
		}
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver called %d times for protected zones", resolver.calls)
	}
}

func TestTLSMITMHostnameMatcher(t *testing.T) {
	tests := []struct {
		host    string
		pattern string
		want    bool
	}{
		{host: "svc.ygg", pattern: "*.ygg", want: true},
		{host: "deep.svc.ygg", pattern: "*.ygg", want: true},
		{host: "ygg", pattern: "*.ygg", want: false},
		{host: "svc.meshname.", pattern: "*.meshname", want: true},
		{host: "svc.example", pattern: "svc.example", want: true},
		{host: "other.example", pattern: "svc.example", want: false},
	}
	for _, tt := range tests {
		if got := matchHostnamePattern(tt.host, tt.pattern); got != tt.want {
			t.Fatalf("matchHostnamePattern(%q, %q) = %v, want %v", tt.host, tt.pattern, got, tt.want)
		}
	}
}

func TestIsYggdrasilAddressIncludesNodeAndSubnetRanges(t *testing.T) {
	tests := map[string]bool{
		"[200::1]:80":    true,
		"[2ff::1]:80":    true,
		"[300::1]:80":    true,
		"[3ff::1]:80":    true,
		"[400::1]:80":    false,
		"127.0.0.1:80":   false,
		"example.com:80": false,
	}
	for address, want := range tests {
		if got := isYggdrasilAddress(address); got != want {
			t.Fatalf("isYggdrasilAddress(%q) = %v, want %v", address, got, want)
		}
	}
}

type recordNetwork struct {
	lastNetwork string
	lastAddress string
	resolverIPs []net.IP
}

func (n *recordNetwork) IsNative() bool { return false }

func (n *recordNetwork) Dial(_ context.Context, network, address string) (net.Conn, error) {
	n.lastNetwork = network
	n.lastAddress = address
	return nil, nil
}

func (n *recordNetwork) Listen(_ context.Context, network, address string) (net.Listener, error) {
	n.lastNetwork = network
	n.lastAddress = address
	return nil, nil
}

func (n *recordNetwork) PacketDial(_ context.Context, network, address string) (gonnect.PacketConn, error) {
	n.lastNetwork = network
	n.lastAddress = address
	return nil, nil
}

func (n *recordNetwork) ListenPacket(_ context.Context, network, address string) (gonnect.PacketConn, error) {
	n.lastNetwork = network
	n.lastAddress = address
	return nil, nil
}

func (n *recordNetwork) DialTCP(_ context.Context, network, _, raddr string) (gonnect.TCPConn, error) {
	n.lastNetwork = network
	n.lastAddress = raddr
	return nil, nil
}

func (n *recordNetwork) ListenTCP(_ context.Context, network, laddr string) (gonnect.TCPListener, error) {
	n.lastNetwork = network
	n.lastAddress = laddr
	return nil, nil
}

func (n *recordNetwork) DialUDP(_ context.Context, network, _, raddr string) (gonnect.UDPConn, error) {
	n.lastNetwork = network
	n.lastAddress = raddr
	return nil, nil
}

func (n *recordNetwork) ListenUDP(_ context.Context, network, laddr string) (gonnect.UDPConn, error) {
	n.lastNetwork = network
	n.lastAddress = laddr
	return nil, nil
}

func (n *recordNetwork) LookupIP(_ context.Context, _, host string) ([]net.IP, error) {
	if len(n.resolverIPs) == 0 {
		return nil, noSuchHost(host)
	}
	return n.resolverIPs, nil
}

func (n *recordNetwork) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
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

func (n *recordNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	ips, err := n.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr)
		}
	}
	return out, nil
}

func (n *recordNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
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

func (n *recordNetwork) LookupAddr(context.Context, string) ([]string, error) { return nil, nil }
func (n *recordNetwork) LookupCNAME(_ context.Context, host string) (string, error) {
	return host, nil
}
func (n *recordNetwork) LookupPort(_ context.Context, network, service string) (int, error) {
	return gonnect.LookupPortOffline(network, service)
}
func (n *recordNetwork) LookupNS(context.Context, string) ([]*net.NS, error) { return nil, nil }
func (n *recordNetwork) LookupMX(context.Context, string) ([]*net.MX, error) { return nil, nil }
func (n *recordNetwork) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, nil
}
func (n *recordNetwork) LookupTXT(context.Context, string) ([]string, error) { return nil, nil }

type fakeResolver struct {
	ips []net.IP
	err error
}

func (r fakeResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.ips, nil
}

func (r fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	ips, err := r.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: ip})
	}
	return out, nil
}

func (r fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	ips, err := r.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr)
		}
	}
	return out, nil
}

func (r fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	ips, err := r.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

func (fakeResolver) LookupAddr(context.Context, string) ([]string, error) { return nil, nil }
func (fakeResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	return host, nil
}
func (fakeResolver) LookupPort(_ context.Context, network, service string) (int, error) {
	return gonnect.LookupPortOffline(network, service)
}
func (fakeResolver) LookupNS(context.Context, string) ([]*net.NS, error) { return nil, nil }
func (fakeResolver) LookupMX(context.Context, string) ([]*net.MX, error) { return nil, nil }
func (fakeResolver) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, nil
}
func (fakeResolver) LookupTXT(context.Context, string) ([]string, error) { return nil, nil }

type countingResolver struct {
	fakeResolver
	calls int
}

func (r *countingResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	r.calls++
	return r.fakeResolver.LookupIP(ctx, network, host)
}
