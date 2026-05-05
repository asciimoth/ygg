package core

import (
	"context"
	"crypto/tls"
	"net/url"
	"testing"

	"github.com/asciimoth/gonnect/native"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/transport"
)

func TestNormalizeTransportURL(t *testing.T) {
	t.Run("non-socks unchanged", func(t *testing.T) {
		u, err := url.Parse("tcp://127.0.0.1:1234")
		require_NoError(t, err)

		nu := normalizeTransportURL(u)
		if nu != u {
			t.Fatal("expected non-socks URL to be returned unchanged")
		}
	})

	t.Run("socks rewritten to tcp target", func(t *testing.T) {
		u, err := url.Parse("socks://localhost:9050/example.org:1234?priority=7")
		require_NoError(t, err)

		nu := normalizeTransportURL(u)
		if nu == u {
			t.Fatal("expected socks URL to be copied")
		}
		if got, want := nu.String(), "tcp://example.org:1234?priority=7"; got != want {
			t.Fatalf("unexpected normalized URL %q, want %q", got, want)
		}
		if got, want := u.String(), "socks://localhost:9050/example.org:1234?priority=7"; got != want {
			t.Fatalf("original URL was modified: got %q, want %q", got, want)
		}
	})
}

func TestCallPeerWithSocksTransportAlias(t *testing.T) {
	nodeA, nodeB := newSilentCompatNodes(t)

	listenURL, err := url.Parse("tcp://127.0.0.1:0")
	require_NoError(t, err)
	coreListener, err := nodeA.Listen(listenURL, "")
	require_NoError(t, err)
	defer coreListener.Cancel()

	peerURL, err := url.Parse("socks://127.0.0.1:9050/" + coreListener.Addr().String())
	require_NoError(t, err)
	require_NoError(t, nodeB.CallPeer(peerURL, ""))

	requireTransportConnected(t, nodeA, nodeB)
}

func TestListenWithSocksTransportAlias(t *testing.T) {
	nodeA, nodeB := newSilentCompatNodes(t)

	listenURL, err := url.Parse("socks://127.0.0.1:9050/127.0.0.1:0")
	require_NoError(t, err)
	coreListener, err := nodeA.Listen(listenURL, "")
	require_NoError(t, err)
	defer coreListener.Cancel()

	conn, err := nodeB.TransportManager().Dial(context.Background(), &url.URL{
		Scheme: "tcp",
		Host:   coreListener.Addr().String(),
	})
	require_NoError(t, err)
	defer conn.Close()

	go func() {
		_ = nodeB.links.handler(linkTypeEphemeral, linkOptions{}, conn, nil, false)
	}()

	requireTransportConnected(t, nodeA, nodeB)
}

func newSilentCompatNodes(t *testing.T) (*Core, *Core) {
	t.Helper()

	cfgA := config.GenerateConfig()
	cfgB := config.GenerateConfig()
	require_NoError(t, cfgA.GenerateSelfSignedCertificate())
	require_NoError(t, cfgB.GenerateSelfSignedCertificate())

	nodeA, err := New(cfgA.Certificate, nil, TransportManager{Manager: newSilentCompatManager(t, cfgA.Certificate)})
	require_NoError(t, err)
	t.Cleanup(nodeA.Stop)

	nodeB, err := New(cfgB.Certificate, nil, TransportManager{Manager: newSilentCompatManager(t, cfgB.Certificate)})
	require_NoError(t, err)
	t.Cleanup(nodeB.Stop)

	return nodeA, nodeB
}

func newSilentCompatManager(t *testing.T, cert *tls.Certificate) *transport.Manager {
	t.Helper()

	network := &native.Network{}
	require_NoError(t, network.Up())

	manager := transport.NewManager(network)
	require_NoError(t, manager.RegisterTransport(transport.NewTCPTransport()))
	require_NoError(t, manager.RegisterTransport(transport.NewTLSTransport((&tls.Config{
		Certificates:       []tls.Certificate{*cert},
		InsecureSkipVerify: true,
	}).Clone())))

	return manager
}
