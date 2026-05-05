package sockstun

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/asciimoth/gonnect"
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

type fakeResolver struct {
	ips []net.IP
}

func (r fakeResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
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
