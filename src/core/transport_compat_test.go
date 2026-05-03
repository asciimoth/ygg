package core

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"net/url"
	"testing"

	"github.com/asciimoth/gonnect/native"
	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/transport"
)

func TestTransportTCPInterop(t *testing.T) {
	testTransportInterop(t, "tcp")
}

func TestTransportTLSInterop(t *testing.T) {
	testTransportInterop(t, "tls")
}

func testTransportInterop(t *testing.T, scheme string) {
	t.Run("core listener accepts external transport dial", func(t *testing.T) {
		nodeA, nodeB := newCompatNodes(t)
		listenURL, err := url.Parse(scheme + "://127.0.0.1:0")
		require_NoError(t, err)
		coreListener, err := nodeA.Listen(listenURL, "")
		require_NoError(t, err)
		defer coreListener.Cancel()

		manager := newCompatManager(t, scheme, nodeB.config.tls)
		defer manager.Close()

		conn, err := manager.Dial(context.Background(), &url.URL{
			Scheme: scheme,
			Host:   coreListener.Addr().String(),
		})
		require_NoError(t, err)
		defer conn.Close()

		go func() {
			_ = nodeB.links.handler(linkTypeEphemeral, linkOptions{}, conn, nil, false)
		}()
		requireTransportConnected(t, nodeA, nodeB)
	})

	t.Run("external transport listener accepts core dial", func(t *testing.T) {
		nodeA, nodeB := newCompatNodes(t)
		manager := newCompatManager(t, scheme, nodeA.config.tls)
		defer manager.Close()

		transportListener, err := manager.Listen(context.Background(), &url.URL{
			Scheme: scheme,
			Host:   "127.0.0.1:0",
		})
		require_NoError(t, err)
		defer transportListener.Close()

		done := make(chan error, 1)
		go func() {
			conn, err := transportListener.Accept()
			if err != nil {
				done <- err
				return
			}
			go func() {
				_ = nodeA.links.handler(linkTypeIncoming, linkOptions{}, conn, nil, false)
			}()
			done <- nil
		}()

		peerURL, err := url.Parse(scheme + "://" + transportListener.Addr().String())
		require_NoError(t, err)
		require_NoError(t, nodeB.CallPeer(peerURL, ""))
		require_NoError(t, <-done)
		requireTransportConnected(t, nodeA, nodeB)
	})
}

func newCompatNodes(t *testing.T) (*Core, *Core) {
	t.Helper()

	cfgA := config.GenerateConfig()
	cfgB := config.GenerateConfig()
	require_NoError(t, cfgA.GenerateSelfSignedCertificate())
	require_NoError(t, cfgB.GenerateSelfSignedCertificate())

	nodeA, err := New(cfgA.Certificate, GetLoggerWithPrefix("nodeA ", false), TransportManager{Manager: newCoreTransportManager(t, cfgA.Certificate)})
	require_NoError(t, err)
	t.Cleanup(nodeA.Stop)

	nodeB, err := New(cfgB.Certificate, GetLoggerWithPrefix("nodeB ", false), TransportManager{Manager: newCoreTransportManager(t, cfgB.Certificate)})
	require_NoError(t, err)
	t.Cleanup(nodeB.Stop)

	return nodeA, nodeB
}

func newCompatManager(t *testing.T, scheme string, tlsConfig *tls.Config) *transport.Manager {
	t.Helper()

	network := &native.Network{}
	require_NoError(t, network.Up())

	manager := transport.NewManager(network)
	switch scheme {
	case "tcp":
		require_NoError(t, manager.RegisterTransport(transport.NewTCPTransport()))
	case "tls":
		require_NoError(t, manager.RegisterTransport(transport.NewTLSTransport(tlsConfig.Clone())))
	default:
		t.Fatalf("unsupported scheme %q", scheme)
	}
	return manager
}

func requireTransportConnected(t *testing.T, nodeA, nodeB *Core) {
	t.Helper()

	if !WaitConnected(nodeA, nodeB) {
		t.Fatal("nodes did not connect")
	}

	msgLen := 512
	done := CreateEchoListener(t, nodeA, msgLen, 1)

	msg := make([]byte, msgLen)
	_, _ = rand.Read(msg[40:])
	msg[0] = 0x60
	copy(msg[8:24], nodeB.Address())
	copy(msg[24:40], nodeA.Address())

	_, err := nodeB.WriteTo(msg, nodeA.LocalAddr())
	require_NoError(t, err)

	buf := make([]byte, msgLen)
	_, _, err = nodeB.ReadFrom(buf)
	require_NoError(t, err)

	if string(msg[40:]) != string(buf[40:]) {
		t.Fatal("unexpected echo payload")
	}
	<-done
}
