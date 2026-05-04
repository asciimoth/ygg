package autopeer

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
)

type stubPeerManager struct {
	mu          sync.Mutex
	peerURIs    []string
	connected   []string
	addCalls    []string
	addErr      error
	addErrByURI map[string]error
}

func (s *stubPeerManager) AddPeer(u *url.URL, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addCalls = append(s.addCalls, u.String())
	if err, ok := s.addErrByURI[u.String()]; ok {
		return err
	}
	if s.addErr != nil {
		return s.addErr
	}
	s.peerURIs = append(s.peerURIs, u.String())
	return nil
}

func (s *stubPeerManager) PeerURIs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slicesClone(s.peerURIs)
}

func (s *stubPeerManager) ConnectedPeerURIs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slicesClone(s.connected)
}

func slicesClone(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

type weirdNetwork struct {
	values []int
}

func (n weirdNetwork) IsNative() bool { return false }
func (n weirdNetwork) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) Listen(context.Context, string, string) (net.Listener, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) PacketDial(context.Context, string, string) (gonnect.PacketConn, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) ListenPacket(context.Context, string, string) (gonnect.PacketConn, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) DialTCP(context.Context, string, string, string) (gonnect.TCPConn, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) ListenTCP(context.Context, string, string) (gonnect.TCPListener, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) DialUDP(context.Context, string, string, string) (gonnect.UDPConn, error) {
	return nil, errors.New("not implemented")
}
func (n weirdNetwork) ListenUDP(context.Context, string, string) (gonnect.UDPConn, error) {
	return nil, errors.New("not implemented")
}

func TestManagerCheckNow(t *testing.T) {
	newManager := func(peers []Peer, core *stubPeerManager, config ManagerConfig) (*Manager, *testLogger) {
		logger := &testLogger{}
		fetcher := NewFetcher(logger, time.Hour)
		fetcher.peers = clonePeers(peers)
		manager := NewManager(fetcher)
		if core != nil {
			manager.SetPeerManager(core)
		}
		manager.SetConfig(config)
		manager.randFloat64 = func() float64 { return 0 }
		return manager, logger
	}

	t.Run("idle without filters", func(t *testing.T) {
		manager, logger := newManager(nil, &stubPeerManager{}, ManagerConfig{MinimumConnected: 1})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "no country or transport filters configured") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})

	t.Run("idle without peer manager", func(t *testing.T) {
		manager, logger := newManager(nil, nil, ManagerConfig{
			MinimumConnected: 1,
			Countries:        []string{"georgia"},
		})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "no peer manager configured") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})

	t.Run("idle without conditions", func(t *testing.T) {
		manager, logger := newManager(nil, &stubPeerManager{}, ManagerConfig{
			Countries: []string{"georgia"},
		})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "no conditions configured") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})

	t.Run("conditions already satisfied", func(t *testing.T) {
		core := &stubPeerManager{
			peerURIs:  []string{"tcp://good:1", "tcp://other:2"},
			connected: []string{"tcp://good:1", "tcp://other:2"},
		}
		manager, _ := newManager([]Peer{testPeer("georgia", endpoint("tcp://good:1", "tcp", "90"))}, core, ManagerConfig{
			MinimumConnected:          2,
			MinimumConnectedFromFetch: 1,
			Countries:                 []string{"georgia"},
		})
		manager.checkNow()
		if len(core.addCalls) != 0 {
			t.Fatalf("expected no add calls, got %#v", core.addCalls)
		}
	})

	t.Run("adds highest scored eligible peer", func(t *testing.T) {
		core := &stubPeerManager{
			peerURIs:  []string{"tcp://connected:1", "tcp://existing:2"},
			connected: []string{"tcp://connected:1"},
		}
		manager, logger := newManager([]Peer{
			testPeer("georgia",
				endpoint("tcp://connected:1", "tcp", "10"),
				endpoint("tls://best:2", "tls", "95"),
				endpoint("tcp://existing:2", "tcp", "99"),
				endpoint("quic://ignored:3", "quic", "100"),
			),
			testPeer("france", endpoint("tcp://wrong-country:4", "tcp", "100")),
		}, core, ManagerConfig{
			MinimumConnected: 2,
			Countries:        []string{"  GEORGIA  "},
			TransportSchemes: []string{"tcp", "tls"},
		})
		manager.checkNow()
		if len(core.addCalls) != 1 || core.addCalls[0] != "tls://best:2" {
			t.Fatalf("unexpected add calls: %#v", core.addCalls)
		}
		if !strings.Contains(logger.joined(), "adding tls://best:2") {
			t.Fatalf("missing add log: %q", logger.joined())
		}
	})

	t.Run("uses URL scheme fallback when protocol is empty", func(t *testing.T) {
		core := &stubPeerManager{}
		manager, _ := newManager([]Peer{
			testPeer("georgia", endpoint("quic://fallback:1", "", "50")),
		}, core, ManagerConfig{
			MinimumConnected: 1,
			TransportSchemes: []string{"quic"},
		})
		manager.checkNow()
		if len(core.addCalls) != 1 || core.addCalls[0] != "quic://fallback:1" {
			t.Fatalf("unexpected add calls: %#v", core.addCalls)
		}
	})

	t.Run("no eligible peers remain", func(t *testing.T) {
		core := &stubPeerManager{
			peerURIs: []string{"tcp://existing:1"},
		}
		manager, logger := newManager([]Peer{
			testPeer("georgia", endpoint("tcp://existing:1", "tcp", "50")),
		}, core, ManagerConfig{
			MinimumConnected: 1,
			Countries:        []string{"georgia"},
		})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "no eligible peers remain") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})

	t.Run("rejects invalid candidate URL", func(t *testing.T) {
		manager, logger := newManager([]Peer{
			testPeer("georgia", endpoint("://bad", "tcp", "50")),
		}, &stubPeerManager{}, ManagerConfig{
			MinimumConnected: 1,
			Countries:        []string{"georgia"},
		})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "rejected candidate") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})

	t.Run("logs add errors", func(t *testing.T) {
		core := &stubPeerManager{addErr: errors.New("boom")}
		manager, logger := newManager([]Peer{
			testPeer("georgia", endpoint("tcp://new:1", "tcp", "50")),
		}, core, ManagerConfig{
			MinimumConnected: 1,
			Countries:        []string{"georgia"},
		})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "add tcp://new:1 failed: boom") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})

	t.Run("logs selection errors from invalid uptime", func(t *testing.T) {
		manager, logger := newManager([]Peer{
			testPeer("georgia", endpoint("tcp://new:1", "tcp", "bad")),
		}, &stubPeerManager{}, ManagerConfig{
			MinimumConnected: 1,
			Countries:        []string{"georgia"},
		})
		manager.checkNow()
		if !strings.Contains(logger.joined(), "selection failed") {
			t.Fatalf("unexpected log output: %q", logger.joined())
		}
	})
}

func TestManagerLifecycleAndHelpers(t *testing.T) {
	logger := &testLogger{}
	fetcher := NewFetcher(logger, time.Hour)
	fetcher.builtinData = []byte(sampleDocument("builtin", "tcp://builtin:1234"))
	manager := NewManager(fetcher)

	if manager.checkInterval() != defaultManagerCheckInterval {
		t.Fatalf("unexpected default check interval: %v", manager.checkInterval())
	}

	manager.SetConfig(ManagerConfig{
		CheckInterval:    5 * time.Millisecond,
		MinimumConnected: 1,
		Countries:        []string{"georgia"},
	})
	if got := manager.Config(); got.CheckInterval != 5*time.Millisecond || len(got.Countries) != 1 {
		t.Fatalf("unexpected config snapshot: %#v", got)
	}
	got := manager.Config()
	got.Countries[0] = "mutated"
	if manager.Config().Countries[0] != "georgia" {
		t.Fatal("Config returned internal slice")
	}

	if !manager.Start() {
		t.Fatal("expected first start to succeed")
	}
	if manager.Start() {
		t.Fatal("expected second start to fail")
	}
	fetcher.AddSource(BuiltinSource)
	waitFor(t, func() bool {
		return strings.Contains(logger.joined(), "no peer manager configured")
	})

	manager.SetPeerManager(&stubPeerManager{})
	waitFor(t, func() bool {
		return strings.Contains(logger.joined(), "no eligible peers remain")
	})

	if err := manager.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	var nilManager *Manager
	nilManager.SetPeerManager(nil)
	nilManager.SetConfig(ManagerConfig{})
	nilManager.signalWake()
	nilManager.checkNow()
	nilManager.logf("ignored")
	if got := nilManager.Config(); got.CheckInterval != 0 || len(got.Countries) != 0 || len(got.TransportSchemes) != 0 {
		t.Fatalf("unexpected nil manager config: %#v", got)
	}
	if nilManager.checkInterval() != defaultManagerCheckInterval {
		t.Fatalf("unexpected nil manager interval: %v", nilManager.checkInterval())
	}
	if nilManager.logger() != nil {
		t.Fatal("expected nil logger")
	}

	closedManager := NewManager(NewFetcher(logger, time.Hour))
	if err := closedManager.Close(); err != nil {
		t.Fatalf("close before start failed: %v", err)
	}
	if closedManager.Start() {
		t.Fatal("closed manager should not start")
	}
	if NewManager(nil).Start() {
		t.Fatal("manager without fetcher should not start")
	}
}

func TestManagerAppliesIntervalConfigUpdatesOnTheFly(t *testing.T) {
	logger := &testLogger{}
	manager := NewManager(NewFetcher(logger, time.Hour))
	manager.SetConfig(ManagerConfig{
		CheckInterval:    time.Hour,
		MinimumConnected: 1,
		Countries:        []string{"georgia"},
	})

	if !manager.Start() {
		t.Fatal("expected start")
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Fatalf("close failed: %v", err)
		}
	}()

	waitFor(t, func() bool {
		return logger.countContains("no peer manager configured") >= 1
	})
	time.Sleep(20 * time.Millisecond)
	if got := logger.countContains("no peer manager configured"); got != 1 {
		t.Fatalf("expected no periodic checks before interval update, got %d", got)
	}

	manager.SetConfig(ManagerConfig{
		CheckInterval:    5 * time.Millisecond,
		MinimumConnected: 1,
		Countries:        []string{"georgia"},
	})

	waitFor(t, func() bool {
		return logger.countContains("no peer manager configured") >= 3
	})
}

func TestManagerHelperFunctions(t *testing.T) {
	manager := NewManager(NewFetcher(nil, time.Hour))
	manager.config.CheckInterval = 0
	if got := manager.checkInterval(); got != defaultManagerCheckInterval {
		t.Fatalf("unexpected normalized interval: %v", got)
	}

	if !peerMatchesCountries(Peer{Country: "Georgia"}, normalizeSet([]string{"georgia"})) {
		t.Fatal("expected country match")
	}
	if peerMatchesCountries(Peer{Country: "France"}, normalizeSet([]string{"georgia"})) {
		t.Fatal("unexpected country match")
	}
	if !endpointMatchesSchemes(Endpoint{Protocol: "TCP"}, normalizeSet([]string{"tcp"})) {
		t.Fatal("expected protocol match")
	}
	if endpointMatchesSchemes(Endpoint{Protocol: "TLS"}, normalizeSet([]string{"tcp"})) {
		t.Fatal("unexpected protocol match")
	}
	if !endpointMatchesSchemes(Endpoint{URL: "ws://peer:1"}, normalizeSet([]string{"ws"})) {
		t.Fatal("expected URL scheme fallback match")
	}

	if got, err := uptimeScore(Endpoint{}); err != nil || got != 0 {
		t.Fatalf("unexpected empty uptime score: %v %v", got, err)
	}
	if got, err := uptimeScore(Endpoint{Uptime: &Uptime{Uptime7DPercent: 12.5}}); err != nil || got != 12.5 {
		t.Fatalf("unexpected percent uptime score: %v %v", got, err)
	}
	if _, err := uptimeScore(Endpoint{Uptime: &Uptime{Uptime7DRaw: "nope"}}); err == nil {
		t.Fatal("expected invalid uptime parse error")
	}

	if count := countIntersection(
		map[string]struct{}{"a": {}, "b": {}, "c": {}},
		map[string]struct{}{"a": {}},
	); count != 1 {
		t.Fatalf("unexpected intersection count: %d", count)
	}

	set := normalizeSet([]string{" tcp ", "", "TCP"})
	if len(set) != 1 {
		t.Fatalf("unexpected normalized set: %#v", set)
	}
	unique := uniqueNonEmpty([]string{"a", "", "a", " b "})
	if len(unique) != 2 {
		t.Fatalf("unexpected unique set: %#v", unique)
	}

	fetcher := NewFetcher(nil, time.Hour)
	fetcher.peers = []Peer{
		testPeer("georgia",
			Endpoint{URL: "", Protocol: "tcp"},
			endpoint("tcp://best:1", "tcp", "90"),
			endpoint("tcp://worse:2", "tcp", "10"),
		),
	}
	manager = NewManager(fetcher)
	manager.SetConfig(ManagerConfig{Countries: []string{"georgia"}})
	manager.randFloat64 = func() float64 { return 0 }
	filtered, selected, err := manager.selectCandidate(manager.snapshot(), map[string]struct{}{})
	if err != nil {
		t.Fatalf("selectCandidate failed: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered candidates: %#v", filtered)
	}
	if selected == nil || selected.uri != "tcp://best:1" {
		t.Fatalf("unexpected selected candidate: %#v", selected)
	}

	if !sameNetwork(nil, nil) {
		t.Fatal("expected nil networks to match")
	}
	if sameNetwork(nil, &stubNetwork{}) {
		t.Fatal("unexpected nil/non-nil network match")
	}
	netA := &stubNetwork{}
	if !sameNetwork(netA, netA) {
		t.Fatal("expected identical comparable networks to match")
	}
	if sameNetwork(&stubNetwork{}, &stubNetwork{}) {
		t.Fatal("unexpected distinct comparable networks match")
	}
	if sameNetwork(weirdNetwork{}, weirdNetwork{}) {
		t.Fatal("unexpected non-comparable networks match")
	}

	timer := time.NewTimer(time.Hour)
	resetTimer(timer, time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(time.Second):
		t.Fatal("reset timer did not fire")
	}
}

func testPeer(country string, endpoints ...Endpoint) Peer {
	return Peer{
		Continent:  "europe",
		Country:    country,
		SourceFile: "test.md",
		Endpoints:  endpoints,
	}
}

func endpoint(uri, protocol, uptime string) Endpoint {
	return Endpoint{
		URL:      uri,
		Protocol: protocol,
		Host:     "peer.example",
		Port:     1234,
		Uptime: &Uptime{
			Uptime7DRaw: uptime,
		},
	}
}
