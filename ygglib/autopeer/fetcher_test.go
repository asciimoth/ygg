package autopeer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/loopback"
)

type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) Printf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *testLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *testLogger) countContains(needle string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0
	for _, line := range l.lines {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

type stubNetwork struct {
	dialFn func(context.Context, string, string) (net.Conn, error)
}

func (n *stubNetwork) IsNative() bool { return false }
func (n *stubNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if n.dialFn != nil {
		return n.dialFn(ctx, network, address)
	}
	return nil, errors.New("dial not implemented")
}
func (n *stubNetwork) Listen(context.Context, string, string) (net.Listener, error) {
	return nil, errors.New("not implemented")
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

func TestFetcherBuiltinLifecycleAndManager(t *testing.T) {
	logger := &testLogger{}
	fetcher := NewFetcher(logger, time.Hour)
	fetcher.builtinData = []byte(sampleDocument("builtin", "tcp://builtin:1234"))

	var got [][]Peer
	fetcher.SetOnChange(func(peers []Peer) {
		got = append(got, peers)
	})

	fetcher.AddSource(BuiltinSource)
	peers := fetcher.Peers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 builtin peer, got %d", len(peers))
	}
	if peers[0].Source != BuiltinSource {
		t.Fatalf("unexpected source %q", peers[0].Source)
	}

	peers[0].Country = "mutated"
	if fetcher.Peers()[0].Country == "mutated" {
		t.Fatal("Peers returned internal slice")
	}
	if len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("unexpected callback payloads: %#v", got)
	}

	manager := NewManager(fetcher)
	if manager.Fetcher() != fetcher {
		t.Fatal("Fetcher mismatch")
	}
	if !manager.Start() {
		t.Fatal("expected manager start to activate fetcher")
	}
	if manager.Start() {
		t.Fatal("expected second start to be ignored")
	}
	if NewManager(nil).Start() {
		t.Fatal("nil manager fetcher should not start")
	}
	var nilManager *Manager
	nilManager.SetOnChange(func([]Peer) {})
	if nilManager.Fetcher() != nil {
		t.Fatal("nil manager fetcher should be nil")
	}
	if nilManager.Peers() != nil {
		t.Fatal("nil manager peers should be nil")
	}
	if err := nilManager.Close(); err != nil {
		t.Fatalf("nil manager close failed: %v", err)
	}

	fetcher.RemoveSource(BuiltinSource)
	waitFor(t, func() bool { return len(fetcher.Peers()) == 0 })
	if len(got) != 2 || got[1] != nil {
		t.Fatalf("expected empty callback after removal, got %#v", got)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if fetcher.Start() {
		t.Fatal("closed fetcher should not start")
	}
}

func TestFetcherLoopbackAndOverrides(t *testing.T) {
	defaultNet := loopback.NewLoopbackNetwok()
	overrideNet := loopback.NewLoopbackNetwok()

	var defaultHits atomic.Int32
	var overrideHits atomic.Int32
	runHTTPServer(t, defaultNet, "127.0.0.1:18081", http.StatusOK, sampleDocument("default", "tcp://default:1234"), &defaultHits)
	runHTTPServer(t, overrideNet, "127.0.0.1:18082", http.StatusOK, sampleDocument("override", "tcp://override:1234"), &overrideHits)

	logger := &testLogger{}
	fetcher := NewFetcher(logger, 24*time.Hour)
	sourceA := "http://127.0.0.1:18081/peers.json"
	sourceB := "http://127.0.0.1:18082/peers.json"

	fetcher.SetSources([]string{sourceA, sourceB})
	fetcher.SetSourceNetwork(sourceB, nil)
	if !fetcher.Start() {
		t.Fatal("expected fetcher start")
	}
	time.Sleep(50 * time.Millisecond)
	if len(fetcher.Peers()) != 0 {
		t.Fatal("expected no peers without a default network")
	}

	fetcher.SetDefaultNetwork(defaultNet)
	waitFor(t, func() bool {
		peers := fetcher.Peers()
		return len(peers) == 1 && peers[0].Label == "default"
	})
	if defaultHits.Load() == 0 {
		t.Fatal("default source was not fetched")
	}

	if err := fetcher.FetchNow(context.Background(), sourceB); err != nil {
		t.Fatalf("disabled source fetch returned error: %v", err)
	}
	if overrideHits.Load() != 0 {
		t.Fatal("disabled source should not be dialed")
	}

	fetcher.SetSourceNetwork(sourceB, overrideNet)
	if err := fetcher.FetchNow(context.Background(), sourceB); err != nil {
		t.Fatalf("forced override fetch failed: %v", err)
	}
	waitFor(t, func() bool { return len(fetcher.Peers()) == 2 })
	if overrideHits.Load() == 0 {
		t.Fatal("override source was not fetched")
	}

	fetcher.SetDefaultNetwork(nil)
	waitFor(t, func() bool {
		peers := fetcher.Peers()
		return len(peers) == 1 && peers[0].Label == "override"
	})

	fetcher.RemoveSourceNetwork(sourceB)
	waitFor(t, func() bool { return len(fetcher.Peers()) == 0 })

	fetcher.SetSources([]string{sourceA})
	fetcher.SetDefaultNetwork(defaultNet)
	waitFor(t, func() bool { return len(fetcher.Peers()) == 1 })

	if err := fetcher.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}

func TestFetcherDropsInFlightResultWhenSourceBecomesDisabled(t *testing.T) {
	defaultNet := loopback.NewLoopbackNetwok()
	logger := &testLogger{}
	fetcher := NewFetcher(logger, 24*time.Hour)
	source := "http://127.0.0.1:18083/peers.json"

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	ln, err := defaultNet.Listen(context.Background(), "tcp", "127.0.0.1:18083")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleDocument("default", "tcp://default:1234")))
		}),
	}
	go func() {
		_ = server.Serve(ln)
	}()
	t.Cleanup(func() { _ = server.Close() })

	fetcher.SetSources([]string{source})
	fetcher.SetDefaultNetwork(defaultNet)

	done := make(chan error, 1)
	go func() {
		done <- fetcher.FetchNow(context.Background(), source)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fetch to start")
	}

	fetcher.SetDefaultNetwork(nil)
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fetch returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fetch to finish")
	}

	if peers := fetcher.Peers(); len(peers) != 0 {
		t.Fatalf("expected no peers after disabling source, got %#v", peers)
	}
}

func TestFetcherErrorsAndParsing(t *testing.T) {
	logger := &testLogger{}
	fetcher := NewFetcher(logger, time.Hour)
	fetcher.SetSources([]string{"http://127.0.0.1:19000/peers.json"})
	fetcher.SetDefaultNetwork(&stubNetwork{
		dialFn: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("boom")
		},
	})

	if err := fetcher.FetchNow(context.Background(), "http://127.0.0.1:19000/peers.json"); err == nil {
		t.Fatal("expected fetch error")
	}
	if !strings.Contains(logger.joined(), "boom") {
		t.Fatalf("expected logged dial error, got %q", logger.joined())
	}

	invalidNet := loopback.NewLoopbackNetwok()
	runHTTPServer(t, invalidNet, "127.0.0.1:19001", http.StatusInternalServerError, "bad", nil)
	sourceErr := "http://127.0.0.1:19001/peers.json"
	fetcher.SetSources([]string{sourceErr})
	fetcher.SetDefaultNetwork(invalidNet)
	if err := fetcher.FetchNow(context.Background(), sourceErr); err == nil {
		t.Fatal("expected HTTP status error")
	}

	parseCases := []struct {
		name string
		body string
	}{
		{
			name: "missing schema version",
			body: `{"generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[]}`,
		},
		{
			name: "missing generated at",
			body: `{"schema_version":"1","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[]}`,
		},
		{
			name: "missing public peers repo",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_branch":"y","uptime_page":"z"},"peers":[]}`,
		},
		{
			name: "missing public peers branch",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","uptime_page":"z"},"peers":[]}`,
		},
		{
			name: "missing uptime page",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y"},"peers":[]}`,
		},
		{
			name: "missing continent",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"country":"b","source_file":"c","endpoints":[{"url":"tcp://x:1","protocol":"tcp","host":"x","port":1,"normalized":true}]}]}`,
		},
		{
			name: "missing country",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","source_file":"c","endpoints":[{"url":"tcp://x:1","protocol":"tcp","host":"x","port":1,"normalized":true}]}]}`,
		},
		{
			name: "missing source file",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","country":"b","endpoints":[{"url":"tcp://x:1","protocol":"tcp","host":"x","port":1,"normalized":true}]}]}`,
		},
		{
			name: "missing endpoints",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","country":"b","source_file":"c","endpoints":[]}]} `,
		},
		{
			name: "missing url",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","country":"b","source_file":"c","endpoints":[{"protocol":"tcp","host":"x","port":1,"normalized":true}]}]}`,
		},
		{
			name: "missing protocol",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","country":"b","source_file":"c","endpoints":[{"url":"tcp://x:1","host":"x","port":1,"normalized":true}]}]}`,
		},
		{
			name: "missing host",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","country":"b","source_file":"c","endpoints":[{"url":"tcp://x:1","protocol":"tcp","port":1,"normalized":true}]}]}`,
		},
		{
			name: "invalid port",
			body: `{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[{"continent":"a","country":"b","source_file":"c","endpoints":[{"url":"tcp://x:0","protocol":"tcp","host":"x","port":0,"normalized":true}]}]}`,
		},
	}
	for _, tc := range parseCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePeers("test", []byte(tc.body)); err == nil {
				t.Fatalf("expected parse failure for %s", tc.name)
			}
		})
	}

	if _, err := parsePeers("test", 42); err == nil {
		t.Fatal("expected unsupported parser input error")
	}
	if _, err := parsePeers("test", []byte(`{"schema_version":"1","generated_at":"2026-05-04T00:00:00Z","sources":{"public_peers_repo":"x","public_peers_branch":"y","uptime_page":"z"},"peers":[],"extra":true}`)); err == nil {
		t.Fatal("expected unknown field error")
	}

	peers, err := parsePeers("sample", []byte(sampleDocument("parsed", "tcp://parsed:1234")))
	if err != nil {
		t.Fatalf("parse valid document failed: %v", err)
	}
	if len(peers) != 1 || peers[0].Source != "sample" {
		t.Fatalf("unexpected parsed peers: %#v", peers)
	}

	fetcher = NewFetcher(logger, time.Hour)
	fetcher.builtinData = []byte(`{"bad":true}`)
	fetcher.AddSource(BuiltinSource)
	if !strings.Contains(logger.joined(), BuiltinSource) {
		t.Fatalf("expected builtin parse error log, got %q", logger.joined())
	}

	if err := fetcher.FetchNow(context.Background(), "missing"); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestFetcherAdditionalCoverage(t *testing.T) {
	fetcher := NewFetcher(nil, 0)
	if fetcher.interval != defaultFetchInterval {
		t.Fatalf("expected default interval %v, got %v", defaultFetchInterval, fetcher.interval)
	}
	fetcher.builtinData = []byte(sampleDocument("builtin", "tcp://builtin:1234"))

	manager := NewManager(fetcher)
	var callbackCount int
	manager.SetOnChange(func([]Peer) {
		callbackCount++
	})
	fetcher.AddSource(BuiltinSource)
	if len(manager.Peers()) != 1 {
		t.Fatalf("expected manager peers to proxy fetcher state, got %#v", manager.Peers())
	}
	if err := fetcher.FetchNow(context.Background(), BuiltinSource); err != nil {
		t.Fatalf("builtin fetch now failed: %v", err)
	}
	if callbackCount == 0 {
		t.Fatal("expected manager callback to be installed on fetcher")
	}
	if network, ok := fetcher.networkForSourceLocked(BuiltinSource); ok || network != nil {
		t.Fatal("builtin source should not resolve to a network")
	}
	if _, err := fetcher.fetchSource(context.Background(), "://bad", &stubNetwork{}); err == nil {
		t.Fatal("expected invalid URL error")
	}
	if err := fetcher.Close(); err != nil {
		t.Fatalf("close before start failed: %v", err)
	}
	if err := fetcher.Close(); err != nil {
		t.Fatalf("second close before start failed: %v", err)
	}
}

func TestFetcherTickerAndFetchAllError(t *testing.T) {
	logger := &testLogger{}
	var dials atomic.Int32
	network := &stubNetwork{
		dialFn: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("tick boom")
		},
	}

	fetcher := NewFetcher(logger, 10*time.Millisecond)
	source := "http://127.0.0.1:19999/peers.json"
	fetcher.SetSources([]string{BuiltinSource, source})
	fetcher.SetDefaultNetwork(network)

	if got := fetcher.fetchList(); len(got) != 1 || got[0].source != source {
		t.Fatalf("unexpected fetch list: %#v", got)
	}
	if !fetcher.Start() {
		t.Fatal("expected start")
	}
	waitFor(t, func() bool { return dials.Load() >= 2 })
	if !strings.Contains(logger.joined(), "tick boom") {
		t.Fatalf("expected periodic fetch error log, got %q", logger.joined())
	}
	if err := fetcher.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}

func sampleDocument(label, endpoint string) string {
	payload := Document{
		SchemaVersion: "1.0.0",
		GeneratedAt:   "2026-05-04T00:00:00Z",
		Sources: DocumentSources{
			PublicPeersRepo:   "https://example.com/repo.git",
			PublicPeersBranch: "main",
			UptimePage:        "https://example.com/uptime",
		},
		Peers: []Peer{{
			Continent:  "europe",
			Country:    "georgia",
			SourceFile: "europe/georgia.md",
			Label:      label,
			Endpoints: []Endpoint{{
				URL:        endpoint,
				Protocol:   "tcp",
				Host:       "peer.example",
				Port:       1234,
				Normalized: true,
				Annotations: []string{
					"test",
				},
				Uptime: &Uptime{
					Status:          "online",
					StatusClass:     "good",
					StatusSince:     "now",
					Uptime7DPercent: 100,
					Uptime7DRaw:     "100%",
					ObservedCountry: "georgia",
					ObservedAddress: endpoint,
					ObservedAt:      "now",
				},
			}},
		}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func runHTTPServer(
	t *testing.T,
	network *loopback.LoopbackNetwork,
	address string,
	status int,
	body string,
	hits *atomic.Int32,
) {
	t.Helper()
	ln, err := network.Listen(context.Background(), "tcp", address)
	if err != nil {
		t.Fatalf("listen %s failed: %v", address, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits != nil {
				hits.Add(1)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}),
	}
	go func() {
		_ = server.Serve(ln)
	}()
	t.Cleanup(func() { _ = server.Close() })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
