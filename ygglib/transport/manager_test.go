package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/loopback"
)

func TestManagerRegisterAndDialErrors(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	if err := m.RegisterTransport(nil); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("expected ErrNilTransport, got %v", err)
	}

	noSchemes := &stubTransport{}
	if err := m.RegisterTransport(noSchemes); err == nil {
		t.Fatal("expected error for empty schemes")
	}

	emptyScheme := &stubTransport{schemes: []string{""}}
	if err := m.RegisterTransport(emptyScheme); err == nil {
		t.Fatal("expected error for empty scheme")
	}

	transport := &stubTransport{
		schemes: []string{"mock"},
		dialFn: func(context.Context, Network, *url.URL, Options) (Conn, error) {
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
	}
	if err := m.RegisterTransport(transport); err != nil {
		t.Fatalf("register transport: %v", err)
	}
	if err := m.RegisterTransport(transport); !errors.Is(err, ErrTransportAlreadyRegistered) {
		t.Fatalf("expected duplicate registration error, got %v", err)
	}

	if _, err := m.Dial(context.Background(), nil); err == nil {
		t.Fatal("expected nil URL dial error")
	}
	if _, err := m.Listen(context.Background(), nil); err == nil {
		t.Fatal("expected nil URL listen error")
	}

	u := mustParseURL(t, "unknown://example")
	if _, err := m.Dial(context.Background(), u); !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("expected ErrUnsupportedScheme, got %v", err)
	}

	u = mustParseURL(t, "mock://example")
	if _, err := m.Dial(context.Background(), u); !errors.Is(err, ErrNoNetwork) {
		t.Fatalf("expected ErrNoNetwork, got %v", err)
	}

	m.UnregisterTransport("mock")
	if _, err := m.Dial(context.Background(), u); !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("expected ErrUnsupportedScheme after unregister, got %v", err)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second close manager: %v", err)
	}
	if err := m.RegisterTransport(transport); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed after close, got %v", err)
	}
	if _, err := m.Dial(context.Background(), mustParseURL(t, "mock://example")); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed dial, got %v", err)
	}
	if err := m.MapNetwork("[", nil); !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected invalid pattern error, got %v", err)
	}
	if err := m.UnmapNetwork("["); !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected invalid pattern error, got %v", err)
	}
}

func TestManagerNetworkSelectionAndNilMappings(t *testing.T) {
	t.Parallel()

	defaultNet := &stubNetwork{name: "default"}
	wildcardNet := &stubNetwork{name: "wildcard"}
	exactNet := &stubNetwork{name: "exact"}

	var usedMu sync.Mutex
	var used []string

	m := NewManager(defaultNet)
	err := m.RegisterTransport(&stubTransport{
		schemes: []string{"mock"},
		dialFn: func(_ context.Context, network Network, _ *url.URL, _ Options) (Conn, error) {
			usedMu.Lock()
			used = append(used, network.(*stubNetwork).name)
			usedMu.Unlock()
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
	})
	if err != nil {
		t.Fatalf("register transport: %v", err)
	}
	if err := m.MapNetwork("*.example", wildcardNet); err != nil {
		t.Fatalf("map wildcard: %v", err)
	}
	if err := m.MapNetwork("api.example", exactNet); err != nil {
		t.Fatalf("map exact: %v", err)
	}

	for _, rawURL := range []string{
		"mock://other.test",
		"mock://foo.example",
		"mock://api.example",
	} {
		conn, err := m.Dial(context.Background(), mustParseURL(t, rawURL))
		if err != nil {
			t.Fatalf("dial %s: %v", rawURL, err)
		}
		_ = conn.Close()
	}

	usedMu.Lock()
	defer usedMu.Unlock()
	if got, want := used, []string{"default", "wildcard", "exact"}; len(got) != len(want) {
		t.Fatalf("used networks length mismatch: got %v want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("used network[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	}

	m.SetDefaultNetwork(nil)
	if _, err := m.Dial(context.Background(), mustParseURL(t, "mock://other.test")); !errors.Is(err, ErrNoNetwork) {
		t.Fatalf("expected ErrNoNetwork for nil default, got %v", err)
	}

	if err := m.MapNetwork("*.example", nil); err != nil {
		t.Fatalf("remap wildcard nil: %v", err)
	}
	if _, err := m.Dial(context.Background(), mustParseURL(t, "mock://foo.example")); !errors.Is(err, ErrNoNetwork) {
		t.Fatalf("expected ErrNoNetwork for nil mapped network, got %v", err)
	}
}

func TestManagerMappingChangesCloseResources(t *testing.T) {
	t.Parallel()

	m := newLoopbackManager(t, loopback.NewLoopbackNetwok())
	listener, client, server := openManagedPair(t, m, "tcp://127.0.0.1:0")

	if got := resourceCount(m); got != 3 {
		t.Fatalf("unexpected resource count before remap: %d", got)
	}

	if err := m.MapNetwork("127.0.0.1", loopback.NewLoopbackNetwok()); err != nil {
		t.Fatalf("map network: %v", err)
	}
	waitForResourceCount(t, m, 0)

	assertClosed(t, client)
	assertClosed(t, server)
	if err := listener.Close(); err != nil {
		t.Fatalf("listener close after remap: %v", err)
	}
}

func TestManagerUnmapAndCloseCloseMappedResources(t *testing.T) {
	t.Parallel()

	netw := loopback.NewLoopbackNetwok()
	m := newLoopbackManager(t, nil)
	if err := m.MapNetwork("127.0.0.1", netw); err != nil {
		t.Fatalf("map network: %v", err)
	}

	_, client, server := openManagedPair(t, m, "tcp://127.0.0.1:0")
	if got := resourceCount(m); got != 3 {
		t.Fatalf("unexpected resource count before unmap: %d", got)
	}

	if err := m.UnmapNetwork("127.0.0.1"); err != nil {
		t.Fatalf("unmap network: %v", err)
	}
	waitForResourceCount(t, m, 0)
	assertClosed(t, client)
	assertClosed(t, server)

	m = newLoopbackManager(t, loopback.NewLoopbackNetwok())
	_, client, server = openManagedPair(t, m, "tcp://127.0.0.1:0")
	if err := m.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	waitForResourceCount(t, m, 0)
	assertClosed(t, client)
	assertClosed(t, server)
}

func TestManagerSetDefaultNetworkClosesDefaultResources(t *testing.T) {
	t.Parallel()

	m := newLoopbackManager(t, loopback.NewLoopbackNetwok())
	_, client, server := openManagedPair(t, m, "tcp://127.0.0.1:0")
	m.SetDefaultNetwork(loopback.NewLoopbackNetwok())
	waitForResourceCount(t, m, 0)
	assertClosed(t, client)
	assertClosed(t, server)
}

func TestManagerAccessorsAndBuiltinNetworks(t *testing.T) {
	t.Parallel()

	defaultNet, err := NewBuiltinNetwork(NetworkKindNative)
	if err != nil {
		t.Fatalf("NewBuiltinNetwork(native): %v", err)
	}
	mappedNet, err := NewBuiltinNetwork(NetworkKindNative)
	if err != nil {
		t.Fatalf("NewBuiltinNetwork(native mapped): %v", err)
	}

	m := NewManager(defaultNet)
	if err := m.MapNetwork("*.example", mappedNet); err != nil {
		t.Fatalf("MapNetwork: %v", err)
	}

	if got := m.DefaultNetwork(); got != defaultNet {
		t.Fatal("default network accessor returned unexpected instance")
	}
	mappings := m.NetworkMappings()
	if got := mappings["*.example"]; got != mappedNet {
		t.Fatal("network mappings accessor returned unexpected instance")
	}
	delete(mappings, "*.example")
	if len(m.NetworkMappings()) != 1 {
		t.Fatal("network mappings accessor should return a copy")
	}

	if name, ok := BuiltinNetworkName(defaultNet); !ok || name != NetworkKindNative {
		t.Fatalf("unexpected builtin network name: %q %v", name, ok)
	}
	if _, err := NewBuiltinNetwork("nope"); !errors.Is(err, ErrUnsupportedNetwork) {
		t.Fatalf("expected ErrUnsupportedNetwork, got %v", err)
	}
}

func TestManagerRegisterAcceptedConnRejectsUnknownParent(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if _, err := m.registerAcceptedConn(42, left); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("expected net.ErrClosed, got %v", err)
	}
}

func TestManagerDialRetriesOnVersionChange(t *testing.T) {
	t.Parallel()

	firstConn := &countingConn{Conn: mustPipeConn()}
	secondConn := &countingConn{Conn: mustPipeConn()}
	netA := &stubNetwork{name: "A"}
	netB := &stubNetwork{name: "B"}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32

	m := NewManager(netA)
	err := m.RegisterTransport(&stubTransport{
		schemes: []string{"mock"},
		dialFn: func(_ context.Context, network Network, _ *url.URL, _ Options) (Conn, error) {
			switch calls.Add(1) {
			case 1:
				if network != netA {
					t.Fatalf("first dial used wrong network")
				}
				started <- struct{}{}
				<-release
				return firstConn, nil
			case 2:
				if network != netB {
					t.Fatalf("second dial used wrong network")
				}
				return secondConn, nil
			default:
				t.Fatalf("unexpected dial retry count")
				return nil, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("register transport: %v", err)
	}

	done := make(chan net.Conn, 1)
	go func() {
		conn, err := m.Dial(context.Background(), mustParseURL(t, "mock://example"))
		if err != nil {
			t.Errorf("dial failed: %v", err)
			done <- nil
			return
		}
		done <- conn
	}()

	<-started
	m.SetDefaultNetwork(netB)
	close(release)

	conn := <-done
	if conn == nil {
		t.Fatal("expected retried connection")
	}
	if firstConn.closeCount.Load() == 0 {
		t.Fatal("expected stale first connection to be closed")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close retried conn: %v", err)
	}
}

func TestManagerListenRetriesOnVersionChange(t *testing.T) {
	t.Parallel()

	firstListener := &countingListener{Listener: newSingleConnListener()}
	secondListener := &countingListener{Listener: newSingleConnListener()}
	netA := &stubNetwork{name: "A"}
	netB := &stubNetwork{name: "B"}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32

	m := NewManager(netA)
	err := m.RegisterTransport(&stubTransport{
		schemes: []string{"mock"},
		listenFn: func(_ context.Context, network Network, _ *url.URL, _ Options) (Listener, error) {
			switch calls.Add(1) {
			case 1:
				if network != netA {
					t.Fatalf("first listen used wrong network")
				}
				started <- struct{}{}
				<-release
				return firstListener, nil
			case 2:
				if network != netB {
					t.Fatalf("second listen used wrong network")
				}
				return secondListener, nil
			default:
				t.Fatalf("unexpected listen retry count")
				return nil, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("register transport: %v", err)
	}

	done := make(chan net.Listener, 1)
	go func() {
		ln, err := m.Listen(context.Background(), mustParseURL(t, "mock://example"))
		if err != nil {
			t.Errorf("listen failed: %v", err)
			done <- nil
			return
		}
		done <- ln
	}()

	<-started
	m.SetDefaultNetwork(netB)
	close(release)

	ln := <-done
	if ln == nil {
		t.Fatal("expected retried listener")
	}
	if firstListener.closeCount.Load() == 0 {
		t.Fatal("expected stale first listener to be closed")
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close retried listener: %v", err)
	}
}

func TestManagerRetryHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("dial", func(t *testing.T) {
		m := NewManager(&stubNetwork{name: "A"})
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		err := m.RegisterTransport(&stubTransport{
			schemes: []string{"mock"},
			dialFn: func(context.Context, Network, *url.URL, Options) (Conn, error) {
				started <- struct{}{}
				<-release
				return &countingConn{Conn: mustPipeConn()}, nil
			},
		})
		if err != nil {
			t.Fatalf("register transport: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := m.Dial(ctx, mustParseURL(t, "mock://example"))
			done <- err
		}()
		<-started
		m.SetDefaultNetwork(&stubNetwork{name: "B"})
		cancel()
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("listen", func(t *testing.T) {
		m := NewManager(&stubNetwork{name: "A"})
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		err := m.RegisterTransport(&stubTransport{
			schemes: []string{"mock"},
			listenFn: func(context.Context, Network, *url.URL, Options) (Listener, error) {
				started <- struct{}{}
				<-release
				return &countingListener{Listener: newSingleConnListener()}, nil
			},
		})
		if err != nil {
			t.Fatalf("register transport: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := m.Listen(ctx, mustParseURL(t, "mock://example"))
			done <- err
		}()
		<-started
		m.SetDefaultNetwork(&stubNetwork{name: "B"})
		cancel()
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestManagerHelperAndNoOpBranches(t *testing.T) {
	t.Parallel()

	if normalizeHost(nil) != "" {
		t.Fatal("expected empty normalized nil host")
	}
	if got := normalizeHost(&url.URL{Host: ":0"}); got != ":0" {
		t.Fatalf("normalizeHost(:0) = %q", got)
	}
	if got, err := normalizePattern(" *.Example "); err != nil || got != "*.example" {
		t.Fatalf("normalizePattern = %q, %v", got, err)
	}
	if _, err := normalizePattern(" "); !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("expected ErrInvalidPattern for empty pattern, got %v", err)
	}
	if hostMatches("[", "example") {
		t.Fatal("invalid pattern must not match")
	}

	m := NewManager(&stubNetwork{name: "default"})
	if err := m.RegisterTransport(&stubTransport{schemes: []string{"mock"}}); err != nil {
		t.Fatalf("register transport: %v", err)
	}
	m.UnregisterTransport("missing")
	if err := m.UnmapNetwork("missing"); err != nil {
		t.Fatalf("unmap missing: %v", err)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	m.UnregisterTransport("mock")
	m.SetDefaultNetwork(&stubNetwork{name: "later"})
	if err := m.MapNetwork("example", &stubNetwork{name: "later"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed map, got %v", err)
	}
	if err := m.UnmapNetwork("example"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed unmap, got %v", err)
	}

	var closers []io.Closer
	m.collectResourceTreeLocked(1, &closers, map[uint64]struct{}{1: {}})
	m.collectResourceTreeLocked(1, &closers, map[uint64]struct{}{})
	if len(closers) != 0 {
		t.Fatalf("unexpected closers: %d", len(closers))
	}
}

func TestManagerPropagatesTransportErrorsAndCollectClosers(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	netw := &stubNetwork{name: "default"}
	m := NewManager(netw)
	err := m.RegisterTransport(&stubTransport{
		schemes: []string{"mock"},
		dialFn: func(context.Context, Network, *url.URL, Options) (Conn, error) {
			return nil, wantErr
		},
		listenFn: func(context.Context, Network, *url.URL, Options) (Listener, error) {
			return nil, wantErr
		},
	})
	if err != nil {
		t.Fatalf("register transport: %v", err)
	}
	if _, err := m.Dial(context.Background(), mustParseURL(t, "mock://example")); !errors.Is(err, wantErr) {
		t.Fatalf("expected transport dial error, got %v", err)
	}
	if _, err := m.Listen(context.Background(), mustParseURL(t, "mock://example")); !errors.Is(err, wantErr) {
		t.Fatalf("expected transport listen error, got %v", err)
	}
	if _, err := m.Listen(context.Background(), mustParseURL(t, "other://example")); !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("expected unsupported scheme, got %v", err)
	}

	m.resources[1] = &resource{id: 1, closer: nopCloser{}, kind: resourceConn}
	if closers := m.collectClosersLocked(func(*resource) bool { return false }); len(closers) != 0 {
		t.Fatalf("expected no closers, got %d", len(closers))
	}

	noNet := NewManager(nil)
	if err := noNet.RegisterTransport(&stubTransport{schemes: []string{"mock"}, listenFn: func(context.Context, Network, *url.URL, Options) (Listener, error) {
		return nil, nil
	}}); err != nil {
		t.Fatalf("register no-net transport: %v", err)
	}
	if _, err := noNet.Listen(context.Background(), mustParseURL(t, "mock://example")); !errors.Is(err, ErrNoNetwork) {
		t.Fatalf("expected no network error, got %v", err)
	}
}

func TestTrackedListenerAcceptErrorAndChildAfterParentClose(t *testing.T) {
	t.Parallel()

	l := newSingleConnListener()
	_ = l.Close()
	tl := &trackedListener{Listener: l, manager: NewManager(nil)}
	if _, err := tl.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("expected net.ErrClosed from accept, got %v", err)
	}

	m := newLoopbackManager(t, loopback.NewLoopbackNetwok())
	listener, _, server := openManagedPair(t, m, "tcp://127.0.0.1:0")
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close server conn: %v", err)
	}
	m.unregister(999)
}

func TestTCPTransportRoundTrip(t *testing.T) {
	t.Parallel()

	network := loopback.NewLoopbackNetwok()
	transport := NewTCPTransport()
	ln, err := transport.Listen(context.Background(), network, mustParseURL(t, "tcp://127.0.0.1:0"), Options{})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- conn
	}()

	conn, err := transport.Dial(context.Background(), network, mustURLWithHost(t, "tcp", ln.Addr().String()), Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	server := <-accepted
	defer server.Close()

	readDone := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4)
		if _, err := io.ReadFull(server, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		readDone <- buf
	}()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if got := <-readDone; string(got) != "ping" {
		t.Fatalf("unexpected payload: %q", string(got))
	}
}

func TestTLSTransportRoundTripAndSNI(t *testing.T) {
	t.Parallel()

	cert := mustSelfSignedCert(t)
	network := loopback.NewLoopbackNetwok()

	serverTransport := NewTLSTransport(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	clientTransport := NewTLSTransport(&tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})

	ln, err := serverTransport.Listen(context.Background(), network, mustParseURL(t, "tls://127.0.0.1:0"), Options{})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverDone := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			t.Errorf("accepted conn type = %T", conn)
			return
		}
		if err := tlsConn.Handshake(); err != nil {
			t.Errorf("server handshake: %v", err)
			return
		}
		serverDone <- tlsConn.ConnectionState().ServerName
	}()

	u := mustURLWithHost(t, "tls", ln.Addr().String())
	q := u.Query()
	q.Set("sni", "peer.example")
	u.RawQuery = q.Encode()

	conn, err := clientTransport.Dial(context.Background(), network, u, Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if serverName := <-serverDone; serverName != "peer.example" {
		t.Fatalf("server saw SNI %q, want %q", serverName, "peer.example")
	}
}

func TestTLSTransportSchemesAndHostFallback(t *testing.T) {
	t.Parallel()

	cert := mustSelfSignedCert(t)
	network := loopback.NewLoopbackNetwok()
	serverTransport := NewTLSTransport(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	clientTransport := NewTLSTransport(&tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if got := clientTransport.Schemes(); len(got) != 1 || got[0] != "tls" {
		t.Fatalf("unexpected schemes: %v", got)
	}

	ln, err := serverTransport.Listen(context.Background(), network, mustParseURL(t, "tls://127.0.0.1:0"), Options{})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverDone := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		tlsConn := conn.(*tls.Conn)
		if err := tlsConn.Handshake(); err != nil {
			t.Errorf("server handshake: %v", err)
			return
		}
		serverDone <- tlsConn.ConnectionState().ServerName
	}()

	conn, err := clientTransport.Dial(context.Background(), network, mustURLWithHost(t, "tls", "localhost:"+addrPort(t, ln.Addr())), Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if serverName := <-serverDone; serverName != "localhost" {
		t.Fatalf("server saw host fallback SNI %q, want %q", serverName, "localhost")
	}
}

func TestTLSTransportErrors(t *testing.T) {
	t.Parallel()

	transport := NewTLSTransport(&tls.Config{InsecureSkipVerify: true})
	wantErr := errors.New("boom")
	netErr := &stubNetwork{
		dialFn: func(context.Context, string, string) (net.Conn, error) { return nil, wantErr },
		listenFn: func(context.Context, string, string) (net.Listener, error) {
			return nil, wantErr
		},
	}
	if _, err := transport.Dial(context.Background(), netErr, mustParseURL(t, "tls://127.0.0.1:1"), Options{}); !errors.Is(err, wantErr) {
		t.Fatalf("expected dial error, got %v", err)
	}
	if _, err := transport.Listen(context.Background(), netErr, mustParseURL(t, "tls://127.0.0.1:1"), Options{}); !errors.Is(err, wantErr) {
		t.Fatalf("expected listen error, got %v", err)
	}

	client, server := net.Pipe()
	defer server.Close()
	counted := &countingConn{Conn: client}
	go func() {
		_ = server.Close()
	}()
	if _, err := transport.Dial(context.Background(), &stubNetwork{
		dialFn: func(context.Context, string, string) (net.Conn, error) { return counted, nil },
	}, mustParseURL(t, "tls://127.0.0.1:1"), Options{}); err == nil {
		t.Fatal("expected TLS handshake failure")
	}
	if counted.closeCount.Load() == 0 {
		t.Fatal("expected failed handshake connection to be closed")
	}
}

func TestManagerPassesOptionsToTransport(t *testing.T) {
	t.Parallel()

	var got Options
	m := NewManager(&stubNetwork{name: "default"})
	err := m.RegisterTransport(&stubTransport{
		schemes: []string{"mock"},
		dialFn: func(_ context.Context, _ Network, _ *url.URL, opts Options) (Conn, error) {
			got = opts
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
	})
	if err != nil {
		t.Fatalf("register transport: %v", err)
	}

	conn, err := m.DialWithOptions(context.Background(), mustParseURL(t, "mock://example"), Options{
		SourceInterface: "eth0",
	})
	if err != nil {
		t.Fatalf("dial with options: %v", err)
	}
	_ = conn.Close()

	if got.SourceInterface != "eth0" {
		t.Fatalf("source interface = %q, want %q", got.SourceInterface, "eth0")
	}
}

func TestSourceInterfaceIsBestEffort(t *testing.T) {
	t.Parallel()

	transport := NewTCPTransport()
	netw := &stubNetwork{
		dialFn: func(_ context.Context, network, address string) (net.Conn, error) {
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
		listenFn: func(_ context.Context, network, address string) (net.Listener, error) {
			return newSingleConnListener(), nil
		},
	}

	conn, err := transport.Dial(context.Background(), netw, mustParseURL(t, "tcp://example:1234"), Options{
		SourceInterface: "missing0",
	})
	if err != nil {
		t.Fatalf("best-effort dial failed: %v", err)
	}
	_ = conn.Close()

	ln, err := transport.Listen(context.Background(), netw, mustParseURL(t, "tcp://127.0.0.1:0"), Options{
		SourceInterface: "missing0",
	})
	if err != nil {
		t.Fatalf("best-effort listen failed: %v", err)
	}
	_ = ln.Close()
}

func TestListenScopesLinkLocalAddressWhenInterfaceLookupUnavailable(t *testing.T) {
	t.Parallel()

	var gotAddress string
	transport := NewTCPTransport()
	netw := &stubNetwork{
		listenFn: func(_ context.Context, _ string, address string) (net.Listener, error) {
			gotAddress = address
			return newSingleConnListener(), nil
		},
	}

	ln, err := transport.Listen(context.Background(), netw, mustParseURL(t, "tcp://[fe80::1]:0"), Options{
		SourceInterface: "wlp0s20f3",
	})
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	_ = ln.Close()

	if gotAddress != "[fe80::1%wlp0s20f3]:0" {
		t.Fatalf("listen address = %q, want %q", gotAddress, "[fe80::1%wlp0s20f3]:0")
	}
}

func TestNativeListenKeepsLinkLocalZone(t *testing.T) {
	origNativeListen := nativeListen
	t.Cleanup(func() {
		nativeListen = origNativeListen
	})

	var gotNetwork, gotAddress string
	nativeListen = func(_ context.Context, network, address string) (net.Listener, error) {
		gotNetwork = network
		gotAddress = address
		return newSingleConnListener(), nil
	}

	transport := NewTCPTransport()
	netw := &stubNetwork{native: true}
	ln, err := transport.Listen(context.Background(), netw, mustParseURL(t, "tcp://[fe80::1]:0"), Options{
		SourceInterface: "wlp0s20f3",
	})
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	_ = ln.Close()

	if gotNetwork != "tcp6" {
		t.Fatalf("listen network = %q, want %q", gotNetwork, "tcp6")
	}
	if gotAddress != "[fe80::1%wlp0s20f3]:0" {
		t.Fatalf("listen address = %q, want %q", gotAddress, "[fe80::1%wlp0s20f3]:0")
	}
}

type stubTransport struct {
	schemes  []string
	dialFn   func(context.Context, Network, *url.URL, Options) (Conn, error)
	listenFn func(context.Context, Network, *url.URL, Options) (Listener, error)
}

func (t *stubTransport) Schemes() []string {
	return t.schemes
}

func (t *stubTransport) Dial(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (Conn, error) {
	if t.dialFn == nil {
		return nil, errors.New("dial not implemented")
	}
	return t.dialFn(ctx, network, u, opts)
}

func (t *stubTransport) Listen(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (Listener, error) {
	if t.listenFn == nil {
		return nil, errors.New("listen not implemented")
	}
	return t.listenFn(ctx, network, u, opts)
}

type stubNetwork struct {
	name     string
	native   bool
	dialFn   func(context.Context, string, string) (net.Conn, error)
	listenFn func(context.Context, string, string) (net.Listener, error)
}

func (n *stubNetwork) IsNative() bool { return n.native }
func (n *stubNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if n.dialFn != nil {
		return n.dialFn(ctx, network, address)
	}
	return nil, errors.New("dial not implemented")
}
func (n *stubNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if n.listenFn != nil {
		return n.listenFn(ctx, network, address)
	}
	return nil, errors.New("listen not implemented")
}
func (n *stubNetwork) PacketDial(context.Context, string, string) (gonnect.PacketConn, error) {
	return nil, errors.New("not implemented")
}
func (n *stubNetwork) ListenPacket(context.Context, string, string) (gonnect.PacketConn, error) {
	return nil, errors.New("not implemented")
}
func (n *stubNetwork) DialTCP(context.Context, string, string, string) (gonnect.TCPConn, error) {
	return nil, errors.New("not implemented")
}
func (n *stubNetwork) ListenTCP(context.Context, string, string) (gonnect.TCPListener, error) {
	return nil, errors.New("not implemented")
}
func (n *stubNetwork) DialUDP(context.Context, string, string, string) (gonnect.UDPConn, error) {
	return nil, errors.New("not implemented")
}
func (n *stubNetwork) ListenUDP(context.Context, string, string) (gonnect.UDPConn, error) {
	return nil, errors.New("not implemented")
}

type countingConn struct {
	net.Conn
	closeCount atomic.Int32
}

func (c *countingConn) Close() error {
	c.closeCount.Add(1)
	return c.Conn.Close()
}

type countingListener struct {
	net.Listener
	closeCount atomic.Int32
}

func (l *countingListener) Close() error {
	l.closeCount.Add(1)
	return l.Listener.Close()
}

type singleConnListener struct {
	connCh chan net.Conn
	addr   net.Addr
	once   sync.Once
	closed chan struct{}
}

func newSingleConnListener() *singleConnListener {
	return &singleConnListener{
		connCh: make(chan net.Conn),
		addr:   &net.TCPAddr{IP: net.IPv4zero, Port: 0},
		closed: make(chan struct{}),
	}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case conn := <-l.connCh:
		return conn, nil
	}
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.addr
}

func newLoopbackManager(t *testing.T, network gonnect.Network) *Manager {
	t.Helper()
	m := NewManager(network)
	if err := m.RegisterTransport(NewTCPTransport()); err != nil {
		t.Fatalf("register tcp transport: %v", err)
	}
	return m
}

func openManagedPair(t *testing.T, m *Manager, rawURL string) (net.Listener, net.Conn, net.Conn) {
	t.Helper()

	listener, err := m.Listen(context.Background(), mustParseURL(t, rawURL))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- conn
	}()

	client, err := m.Dial(context.Background(), mustURLWithHost(t, "tcp", listener.Addr().String()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-accepted
	return listener, client, server
}

func waitForResourceCount(t *testing.T, m *Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if resourceCount(m) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resource count = %d, want %d", resourceCount(m), want)
}

func resourceCount(m *Manager) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.resources)
}

func assertClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	_, err := conn.Write([]byte("x"))
	if err == nil {
		t.Fatal("expected closed connection")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func mustURLWithHost(t *testing.T, scheme, host string) *url.URL {
	t.Helper()
	return &url.URL{Scheme: scheme, Host: host}
}

func addrPort(t *testing.T, addr net.Addr) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	return port
}

func mustPipeConn() net.Conn {
	left, right := net.Pipe()
	_ = right.Close()
	return left
}

func mustSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "transport-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"peer.example"},
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "transport-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"peer.example"},
	}, pub, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
