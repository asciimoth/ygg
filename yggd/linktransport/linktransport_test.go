package linktransport

import (
	"context"
	"crypto/tls"
	"io"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/native"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/transport"
)

func TestTransportEcho(t *testing.T) {
	tlsConfig := testTLSConfig(t)

	tests := []struct {
		name      string
		transport transport.Transport
		rawURL    string
		dialURL   func(*url.URL) *url.URL
	}{
		{
			name:      "unix",
			transport: NewUNIXTransport(),
			rawURL:    "unix://" + filepath.Join(t.TempDir(), "link.sock"),
			dialURL:   cloneURL,
		},
		{
			name:      "ws",
			transport: NewWebSocketTransport(),
			rawURL:    "ws://127.0.0.1:0",
			dialURL:   hostDialURL,
		},
		{
			name:      "quic",
			transport: NewQUICTransport(tlsConfig.Clone()),
			rawURL:    "quic://127.0.0.1:0",
			dialURL:   hostDialURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := &native.Network{}
			if err := network.Up(); err != nil {
				t.Fatal(err)
			}

			manager := transport.NewManager(network)
			if err := manager.RegisterTransport(tt.transport); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close() })

			listenURL, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			listener, err := manager.Listen(ctx, listenURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })

			errCh := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					errCh <- err
					return
				}
				defer conn.Close()
				_, err = io.Copy(conn, conn)
				errCh <- err
			}()

			conn, err := manager.Dial(ctx, tt.dialURL(&url.URL{
				Scheme: listenURL.Scheme,
				Host:   listener.Addr().String(),
				Path:   listenURL.Path,
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			if _, err := conn.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			if _, err := io.ReadFull(conn, buf); err != nil {
				t.Fatal(err)
			}
			if string(buf) != "ping" {
				t.Fatalf("unexpected echo %q", string(buf))
			}
		})
	}
}

func TestSecureWebSocketListenUnsupported(t *testing.T) {
	network := &native.Network{}
	if err := network.Up(); err != nil {
		t.Fatal(err)
	}
	_, err := NewSecureWebSocketTransport(testTLSConfig(t)).Listen(
		context.Background(),
		network,
		&url.URL{Scheme: "wss", Host: "127.0.0.1:0"},
		transport.Options{},
	)
	if err == nil {
		t.Fatal("expected wss listen to be unsupported")
	}
}

func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	cfg := config.GenerateConfig()
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := core.GenerateTLSConfig(cfg.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	return tlsConfig
}

func cloneURL(u *url.URL) *url.URL {
	clone := *u
	return &clone
}

func hostDialURL(u *url.URL) *url.URL {
	return &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
	}
}
