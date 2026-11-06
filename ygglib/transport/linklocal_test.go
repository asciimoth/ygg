package transport

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/asciimoth/gonnect"
)

type scopedRecordingNetwork struct {
	*stubNetwork
	wrapped                gonnect.Network
	lookupCalls            int
	lookupIPs              []net.IP
	dialCalls              int
	dialTCPCalls           int
	listenCalls            int
	network, local, remote string
}

func (n *scopedRecordingNetwork) GetWrapped() any { return n.wrapped }

func (n *scopedRecordingNetwork) InterfacesByName(name string) ([]gonnect.NetworkInterface, error) {
	return []gonnect.NetworkInterface{&gonnect.LiteralInterface{
		NameVal:  name,
		AddrsVal: []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::2"), Mask: net.CIDRMask(64, 128)}},
	}}, nil
}

func (n *scopedRecordingNetwork) LookupIP(context.Context, string, string) ([]net.IP, error) {
	n.lookupCalls++
	if n.lookupIPs != nil {
		return n.lookupIPs, nil
	}
	return nil, errors.New("unexpected DNS lookup")
}

func (n *scopedRecordingNetwork) Dial(context.Context, string, string) (net.Conn, error) {
	n.dialCalls++
	return nil, errors.New("unexpected ordinary dial")
}

func (n *scopedRecordingNetwork) DialTCP(_ context.Context, network, local, remote string) (gonnect.TCPConn, error) {
	n.dialTCPCalls++
	n.network, n.local, n.remote = network, local, remote
	return nil, errRecordedDial
}

func (n *scopedRecordingNetwork) Listen(_ context.Context, network, address string) (net.Listener, error) {
	n.listenCalls++
	n.network, n.remote = network, address
	return newSingleConnListener(), nil
}

var errRecordedDial = errors.New("dial reached selected network")

func TestScopedTCPDialUsesSelectedNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, url, remote string
	}{
		{"unscoped", "tcp://[fe80::1]:9001", "[fe80::1%eth0]:9001"},
		{"explicit zone", "tcp://[fe80::1%25eth9]:9001", "[fe80::1%eth9]:9001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &stubNetwork{native: true}
			n := &scopedRecordingNetwork{stubNetwork: &stubNetwork{native: true}, wrapped: inner}
			_, err := tcpDial(context.Background(), n, mustParseURL(t, tc.url), Options{SourceInterface: "eth0"})
			if !errors.Is(err, errRecordedDial) {
				t.Fatalf("dial error = %v, want selected network error", err)
			}
			if n.dialTCPCalls != 1 || n.dialCalls != 0 || n.lookupCalls != 0 {
				t.Fatalf("selected network calls: DialTCP=%d Dial=%d LookupIP=%d", n.dialTCPCalls, n.dialCalls, n.lookupCalls)
			}
			if n.network != "tcp6" || n.remote != tc.remote || n.local != "[fe80::2%eth0]:0" {
				t.Fatalf("DialTCP(%q, %q, %q), want tcp6, [fe80::2%%eth0]:0, %q", n.network, n.local, n.remote, tc.remote)
			}
		})
	}
}

func TestScopedTCPListenUsesSelectedNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, url, address string
	}{
		{"unscoped", "tcp://[fe80::1]:0", "[fe80::1%eth0]:0"},
		{"explicit zone", "tcp://[fe80::1%25eth9]:0", "[fe80::1%eth9]:0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &scopedRecordingNetwork{stubNetwork: &stubNetwork{native: true}, wrapped: &stubNetwork{native: true}}
			ln, err := tcpListen(context.Background(), n, mustParseURL(t, tc.url), Options{SourceInterface: "eth0"})
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()
			if n.listenCalls != 1 || n.network != "tcp" || n.remote != tc.address {
				t.Fatalf("Listen calls=%d network=%q address=%q, want 1, tcp, %q", n.listenCalls, n.network, n.remote, tc.address)
			}
		})
	}
}

func TestTCPHostnameLookupUsesSelectedNetwork(t *testing.T) {
	n := &scopedRecordingNetwork{
		stubNetwork: &stubNetwork{native: true},
		wrapped:     &stubNetwork{native: true},
		lookupIPs:   []net.IP{net.ParseIP("fe80::1")},
	}
	network, remote, err := resolveTCPNetworkAndRemote(context.Background(), n, mustParseURL(t, "tcp://peer.example:9001"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n.lookupCalls != 1 || network != "tcp6" || remote.String() != "[fe80::1]:9001" {
		t.Fatalf("LookupIP calls=%d network=%q remote=%q", n.lookupCalls, network, remote)
	}
}
