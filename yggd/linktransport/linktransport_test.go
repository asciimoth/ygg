package linktransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/transport"
)

func TestTransportEcho(t *testing.T) {
	tlsConfig := testTLSConfig(t)
	payloads := []struct {
		name string
		data []byte
	}{
		{name: "single frame", data: []byte("ping")},
		{name: "multiple internal buffers", data: bytes.Repeat([]byte("yggdrasil"), 32*1024)},
	}

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
			network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build(), nil)
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

			for _, payload := range payloads {
				t.Run(payload.name, func(t *testing.T) {
					if _, err := conn.Write(payload.data); err != nil {
						t.Fatal(err)
					}
					buf := make([]byte, len(payload.data))
					if _, err := io.ReadFull(conn, buf); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(buf, payload.data) {
						t.Fatalf("echoed payload differs: got %d bytes, want %d bytes", len(buf), len(payload.data))
					}
				})
			}
		})
	}
}

func TestSecureWebSocketListenUnsupported(t *testing.T) {
	network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build(), nil)
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

func TestWebSocketListenOrigins(t *testing.T) {
	tests := []struct {
		name       string
		rawURL     string
		origin     string
		wantReject bool
	}{
		{
			name:       "cross origin rejected by default",
			rawURL:     "ws://127.0.0.1:0",
			origin:     "http://127.0.0.1:8000",
			wantReject: true,
		},
		{
			name:   "wildcard accepts cross origin",
			rawURL: "ws://127.0.0.1:0?origin=*",
			origin: "http://127.0.0.1:8000",
		},
		{
			name:   "configured origin is accepted",
			rawURL: "ws://127.0.0.1:0?origin=example.com",
			origin: "https://example.com",
		},
		{
			name:   "second configured origin is accepted",
			rawURL: "ws://127.0.0.1:0?origin=example.com&origin=example.net",
			origin: "https://example.net",
		},
		{
			name:       "unconfigured origin is rejected",
			rawURL:     "ws://127.0.0.1:0?origin=example.com",
			origin:     "https://example.net",
			wantReject: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build(), nil)
			if err := network.Up(); err != nil {
				t.Fatal(err)
			}

			listenURL, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			listener, err := NewWebSocketTransport().Listen(ctx, network, listenURL, transport.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })

			if !tt.wantReject {
				errCh := make(chan error, 1)
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						errCh <- err
						return
					}
					errCh <- conn.Close()
				}()
				t.Cleanup(func() {
					select {
					case err := <-errCh:
						if err != nil {
							t.Error(err)
						}
					case <-time.After(time.Second):
						t.Error("timed out waiting for websocket accept")
					}
				})
			}

			headers := http.Header{}
			headers.Set("Origin", tt.origin)
			c, _, err := websocket.Dial(ctx, "ws://"+listener.Addr().String(), &websocket.DialOptions{
				HTTPHeader:   headers,
				Subprotocols: []string{websocketSubprotocol},
			})
			if tt.wantReject {
				if err == nil {
					_ = c.Close(websocket.StatusNormalClosure, "")
					t.Fatal("expected cross-origin websocket dial to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Subprotocol() != websocketSubprotocol {
				t.Fatalf("unexpected subprotocol %q", c.Subprotocol())
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		})
	}
}

func TestWebSocketListenRejectsMissingSubprotocol(t *testing.T) {
	network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build(), nil)
	if err := network.Up(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := NewWebSocketTransport().Listen(
		ctx,
		network,
		&url.URL{Scheme: "ws", Host: "127.0.0.1:0"},
		transport.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	conn, _, err := websocket.Dial(ctx, "ws://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	_, _, err = conn.Read(ctx)
	if status := websocket.CloseStatus(err); status != websocket.StatusPolicyViolation {
		t.Fatalf("unexpected close status %d: %v", status, err)
	}
}

func TestQUICCloneTLSConfig(t *testing.T) {
	tests := []struct {
		name       string
		base       *tls.Config
		rawURL     string
		serverName string
	}{
		{
			name:       "host name supplies server name",
			rawURL:     "quic://peer.example:1234",
			serverName: "peer.example",
		},
		{
			name:   "IP address does not supply server name",
			rawURL: "quic://192.0.2.1:1234",
		},
		{
			name:       "SNI query overrides configured server name",
			base:       &tls.Config{ServerName: "configured.example"},
			rawURL:     "quic://192.0.2.1:1234?sni=override.example",
			serverName: "override.example",
		},
		{
			name:       "configured server name is preserved",
			base:       &tls.Config{ServerName: "configured.example"},
			rawURL:     "quic://peer.example:1234",
			serverName: "configured.example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			transport := NewQUICTransport(tt.base)
			got := transport.cloneTLSConfig(u)
			if got.ServerName != tt.serverName {
				t.Errorf("server name = %q, want %q", got.ServerName, tt.serverName)
			}
			if got.MinVersion != tls.VersionTLS12 {
				t.Errorf("minimum TLS version = %d, want %d", got.MinVersion, tls.VersionTLS12)
			}
			if got.MaxVersion != tls.VersionTLS13 {
				t.Errorf("maximum TLS version = %d, want %d", got.MaxVersion, tls.VersionTLS13)
			}
			if tt.base != nil && got == tt.base {
				t.Error("cloneTLSConfig returned the caller-owned TLS configuration")
			}
		})
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
