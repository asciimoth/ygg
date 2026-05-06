package jumper

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/ygg/ygglib/core"
)

type stubCore struct {
	mu       sync.Mutex
	peers    []core.PeerInfo
	nodeInfo map[string]json.RawMessage
	fetches  int
	adds     []string
	removes  []string
	addErr   error
}

func (s *stubCore) AddPeer(u *url.URL, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adds = append(s.adds, u.String())
	if s.addErr != nil {
		return s.addErr
	}
	s.peers = append(s.peers, core.PeerInfo{URI: u.String()})
	return nil
}

func (s *stubCore) RemovePeer(u *url.URL, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removes = append(s.removes, u.String())
	for i, peer := range s.peers {
		if peer.URI == u.String() {
			s.peers = append(s.peers[:i], s.peers[i+1:]...)
			return nil
		}
	}
	return core.ErrLinkNotConfigured
}

func (s *stubCore) GetPeers() []core.PeerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.PeerInfo, len(s.peers))
	copy(out, s.peers)
	return out
}

func (s *stubCore) GetNodeInfo(key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	info, ok := s.nodeInfo[key]
	if !ok {
		return nil, errors.New("missing nodeinfo")
	}
	return info, nil
}

func testKey(seed byte) ed25519.PublicKey {
	key := make([]byte, ed25519.PublicKeySize)
	key[0] = seed
	return ed25519.PublicKey(key)
}

func testNodeInfo(addrs ...string) json.RawMessage {
	bs, err := json.Marshal(AdvertisedNodeInfo(addrs))
	if err != nil {
		panic(err)
	}
	return bs
}

func TestAdvertisedNodeInfo(t *testing.T) {
	info := AdvertisedNodeInfo([]string{" tls://host:1 ", "", "bad", "tcp://host:2", "tls://host:1"})
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal advertised nodeinfo: %v", err)
	}
	got := parseNodeInfoAddresses(raw)
	want := []string{"tls://host:1", "tcp://host:2"}
	if len(got) != len(want) {
		t.Fatalf("unexpected addresses: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected addresses: %#v", got)
		}
	}
}

func TestManagerAddsPublishedAddress(t *testing.T) {
	key := testKey(1)
	target := hex.EncodeToString(key)
	core := &stubCore{nodeInfo: map[string]json.RawMessage{target: testNodeInfo("tls://node-b:1234")}}
	manager := NewManager(core, nil, Config{})

	manager.NotifyTraffic(key)
	manager.checkNow()
	waitFor(t, func() bool {
		manager.checkNow()
		core.mu.Lock()
		defer core.mu.Unlock()
		return len(core.adds) == 1
	})

	if core.adds[0] != "tls://node-b:1234" {
		t.Fatalf("unexpected add calls: %#v", core.adds)
	}
}

func TestManagerSkipsDirectPeer(t *testing.T) {
	key := testKey(2)
	target := hex.EncodeToString(key)
	core := &stubCore{
		peers:    []core.PeerInfo{{URI: "tcp://existing:1", Up: true, Key: key}},
		nodeInfo: map[string]json.RawMessage{target: testNodeInfo("tls://node-b:1234")},
	}
	manager := NewManager(core, nil, Config{})

	manager.NotifyTraffic(key)
	manager.checkNow()

	if len(core.adds) != 0 {
		t.Fatalf("expected no add calls, got %#v", core.adds)
	}
}

func TestManagerSkipsAlreadyConfiguredPublishedAddress(t *testing.T) {
	key := testKey(3)
	target := hex.EncodeToString(key)
	core := &stubCore{
		peers:    []core.PeerInfo{{URI: "tls://node-b:1234"}},
		nodeInfo: map[string]json.RawMessage{target: testNodeInfo("tls://node-b:1234", "tls://node-b:5678")},
	}
	manager := NewManager(core, nil, Config{})

	manager.NotifyTraffic(key)
	manager.checkNow()
	waitFor(t, func() bool {
		manager.checkNow()
		return len(manager.stateForTarget(target).addrs) > 0
	})
	manager.checkNow()

	if len(core.adds) != 0 {
		t.Fatalf("expected no add calls, got %#v", core.adds)
	}
}

func TestManagerFetchesEmptyNodeInfoOnce(t *testing.T) {
	key := testKey(5)
	target := hex.EncodeToString(key)
	core := &stubCore{nodeInfo: map[string]json.RawMessage{target: json.RawMessage(`{}`)}}
	manager := NewManager(core, nil, Config{})

	manager.NotifyTraffic(key)
	manager.checkNow()
	waitFor(t, func() bool {
		manager.checkNow()
		core.mu.Lock()
		defer core.mu.Unlock()
		return core.fetches == 1
	})
	manager.checkNow()
	manager.checkNow()

	core.mu.Lock()
	defer core.mu.Unlock()
	if core.fetches != 1 {
		t.Fatalf("expected one nodeinfo fetch, got %d", core.fetches)
	}
	if len(core.adds) != 0 {
		t.Fatalf("expected no add calls, got %#v", core.adds)
	}
}

func TestManagerRemovesUnusedOwnedLinkAndTriesNext(t *testing.T) {
	key := testKey(4)
	target := hex.EncodeToString(key)
	core := &stubCore{nodeInfo: map[string]json.RawMessage{
		target: testNodeInfo("tls://bad:1", "tls://good:2"),
	}}
	manager := NewManager(core, nil, Config{LinkTimeout: time.Second})

	manager.NotifyTraffic(key)
	manager.checkNow()
	waitFor(t, func() bool {
		manager.checkNow()
		core.mu.Lock()
		defer core.mu.Unlock()
		return len(core.adds) == 1
	})

	manager.mu.Lock()
	owned := manager.owned["tls://bad:1"]
	owned.added = time.Now().Add(-2 * time.Second)
	manager.owned["tls://bad:1"] = owned
	manager.mu.Unlock()

	manager.checkNow()
	manager.checkNow()

	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.removes) != 1 || core.removes[0] != "tls://bad:1" {
		t.Fatalf("unexpected removes: %#v", core.removes)
	}
	if len(core.adds) != 2 || core.adds[1] != "tls://good:2" {
		t.Fatalf("unexpected adds: %#v", core.adds)
	}
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
