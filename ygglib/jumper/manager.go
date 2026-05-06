package jumper

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/logger"
)

const (
	NodeInfoKey          = "jumper"
	defaultCheckInterval = 10 * time.Second
	defaultLinkTimeout   = 30 * time.Second
	minimumLinkTimeout   = time.Second
)

// Core is the minimal core surface required by Manager.
type Core interface {
	AddPeer(*url.URL, string) error
	RemovePeer(*url.URL, string) error
	GetPeers() []core.PeerInfo
	GetNodeInfo(string) (json.RawMessage, error)
}

// Config controls jumper's runtime behavior.
type Config struct {
	CheckInterval time.Duration
	LinkTimeout   time.Duration
}

type nodeInfoEnvelope struct {
	Jumper nodeInfoAdvertisement `json:"jumper"`
}

type nodeInfoAdvertisement struct {
	Addresses []string `json:"addresses"`
}

type ownedLink struct {
	target string
	uri    string
	added  time.Time
}

type targetState struct {
	pending  bool
	fetching bool
	fetched  bool
	addrs    []string
	tried    map[string]struct{}
}

// Manager watches traffic destinations and opportunistically connects to their
// published jumper addresses.
type Manager struct {
	core Core
	log  logger.Logger

	mu       sync.Mutex
	config   Config
	active   bool
	stopping bool
	closed   bool

	targets map[string]*targetState
	owned   map[string]ownedLink

	stop chan struct{}
	done chan struct{}
	wake chan struct{}
}

// NewManager constructs a jumper manager. Call NotifyTraffic when traffic is
// sent to a remote public key, or register that method with core.AddPathNotify.
func NewManager(core Core, log logger.Logger, config Config) *Manager {
	config = normalizeConfig(config)
	return &Manager{
		core:    core,
		log:     log,
		config:  config,
		targets: map[string]*targetState{},
		owned:   map[string]ownedLink{},
		wake:    make(chan struct{}, 1),
	}
}

// AdvertisedNodeInfo returns the NodeInfo fragment used to publish local
// jumper addresses. Invalid and blank addresses are omitted.
func AdvertisedNodeInfo(addresses []string) map[string]interface{} {
	clean := normalizeAddresses(addresses)
	if len(clean) == 0 {
		return nil
	}
	return map[string]interface{}{
		NodeInfoKey: map[string]interface{}{
			"addresses": clean,
		},
	}
}

// Start starts the manager loop.
func (m *Manager) Start() bool {
	if m == nil || m.core == nil {
		return false
	}
	m.mu.Lock()
	if m.active || m.stopping || m.closed {
		m.mu.Unlock()
		return false
	}
	m.stop = make(chan struct{})
	m.done = make(chan struct{})
	m.active = true
	m.mu.Unlock()
	go m.loop()
	m.signalWake()
	return true
}

// Stop stops the manager loop. Existing connected jumper links are left alone;
// disconnected jumper-owned links are still eligible for cleanup on shutdown.
func (m *Manager) Stop() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if !m.active && !m.stopping {
		m.mu.Unlock()
		return nil
	}
	stop := m.stop
	done := m.done
	if m.stopping {
		m.mu.Unlock()
		<-done
		return nil
	}
	m.stopping = true
	m.mu.Unlock()

	close(stop)
	<-done
	return nil
}

// Close permanently stops the manager.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	_ = m.Stop()
	return nil
}

// NotifyTraffic tells the manager that traffic is being sent to key.
func (m *Manager) NotifyTraffic(key ed25519.PublicKey) {
	if m == nil || len(key) != ed25519.PublicKeySize {
		return
	}
	target := hex.EncodeToString(key)
	m.mu.Lock()
	state := m.stateForTargetLocked(target)
	state.pending = true
	m.mu.Unlock()
	m.signalWake()
}

// SetConfig replaces the current jumper timing configuration.
func (m *Manager) SetConfig(config Config) {
	if m == nil {
		return
	}
	config = normalizeConfig(config)
	m.mu.Lock()
	m.config = config
	m.mu.Unlock()
	m.signalWake()
}

// Config returns the current jumper timing configuration.
func (m *Manager) Config() Config {
	if m == nil {
		return Config{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

// IsStarted reports whether the manager loop is currently active.
func (m *Manager) IsStarted() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active && !m.stopping && !m.closed
}

func (m *Manager) loop() {
	defer func() {
		m.cleanupOwned(false)
		m.mu.Lock()
		m.active = false
		m.stopping = false
		m.mu.Unlock()
		if m.done != nil {
			close(m.done)
		}
	}()

	timer := time.NewTimer(m.checkInterval())
	defer timer.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-m.wake:
			m.checkNow()
			resetTimer(timer, m.checkInterval())
		case <-timer.C:
			m.checkNow()
			resetTimer(timer, m.checkInterval())
		}
	}
}

func (m *Manager) checkNow() {
	if m == nil || m.core == nil {
		return
	}
	m.cleanupOwned(false)

	for _, target := range m.targetsSnapshot() {
		m.checkTarget(target)
	}
}

func (m *Manager) checkTarget(target string) {
	if m.targetDirectlyConnected(target) || m.targetHasPublishedLinkConfigured(target) {
		return
	}

	state := m.stateForTarget(target)
	if state == nil || !state.pending {
		return
	}
	if len(state.addrs) == 0 {
		if !state.fetched {
			m.fetchTargetNodeInfo(target)
		}
		return
	}

	for _, addr := range state.addrs {
		if _, ok := state.tried[addr]; ok {
			continue
		}
		if uriConfigured(m.core.GetPeers(), addr) {
			return
		}
		u, err := url.Parse(addr)
		if err != nil {
			m.markTried(target, addr)
			m.logf("jumper rejected %s from %s: %v", addr, target, err)
			continue
		}
		if err := m.core.AddPeer(u, ""); err != nil {
			if errors.Is(err, core.ErrLinkAlreadyConfigured) {
				return
			}
			m.markTried(target, addr)
			m.logf("jumper add %s for %s failed: %v", addr, target, err)
			continue
		}
		m.rememberOwned(target, u.String())
		m.markTried(target, addr)
		m.logf("jumper added %s for %s", addr, target)
		return
	}
}

func (m *Manager) fetchTargetNodeInfo(target string) {
	m.mu.Lock()
	state := m.stateForTargetLocked(target)
	if state.fetching {
		m.mu.Unlock()
		return
	}
	state.fetching = true
	m.mu.Unlock()

	go func() {
		info, err := m.core.GetNodeInfo(target)
		addrs := parseNodeInfoAddresses(info)
		if err != nil {
			m.logf("jumper nodeinfo fetch for %s failed: %v", target, err)
		}

		m.mu.Lock()
		state := m.stateForTargetLocked(target)
		state.fetching = false
		state.fetched = true
		state.addrs = addrs
		if state.tried == nil {
			state.tried = map[string]struct{}{}
		}
		m.mu.Unlock()
		m.signalWake()
	}()
}

func (m *Manager) cleanupOwned(force bool) {
	if m == nil || m.core == nil {
		return
	}
	peers := m.core.GetPeers()
	config := m.configSnapshot()
	now := time.Now()
	var remove []ownedLink

	m.mu.Lock()
	for uri, owned := range m.owned {
		if peerUpWithURI(peers, uri) && !force {
			continue
		}
		if !force && now.Sub(owned.added) < config.LinkTimeout {
			continue
		}
		remove = append(remove, owned)
		delete(m.owned, uri)
	}
	m.mu.Unlock()

	for _, owned := range remove {
		u, err := url.Parse(owned.uri)
		if err != nil {
			continue
		}
		if err := m.core.RemovePeer(u, ""); err != nil && !errors.Is(err, core.ErrLinkNotConfigured) {
			m.logf("jumper remove %s failed: %v", owned.uri, err)
			continue
		}
		m.logf("jumper removed unused %s for %s", owned.uri, owned.target)
		m.signalWake()
	}
}

func (m *Manager) targetDirectlyConnected(target string) bool {
	key, err := hex.DecodeString(target)
	if err != nil {
		return false
	}
	for _, peer := range m.core.GetPeers() {
		if peer.Up && bytes.Equal(peer.Key, key) {
			return true
		}
	}
	return false
}

func (m *Manager) targetHasPublishedLinkConfigured(target string) bool {
	state := m.stateForTarget(target)
	if state == nil || len(state.addrs) == 0 {
		return false
	}
	peers := m.core.GetPeers()
	for _, addr := range state.addrs {
		if uriConfigured(peers, addr) {
			return true
		}
	}
	return false
}

func parseNodeInfoAddresses(info json.RawMessage) []string {
	var envelope nodeInfoEnvelope
	if len(info) == 0 || json.Unmarshal(info, &envelope) != nil {
		return nil
	}
	return normalizeAddresses(envelope.Jumper.Addresses)
}

func normalizeAddresses(addresses []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, addr := range addresses {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		u, err := url.Parse(addr)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		addr = u.String()
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func normalizeConfig(config Config) Config {
	if config.CheckInterval <= 0 {
		config.CheckInterval = defaultCheckInterval
	}
	if config.LinkTimeout <= 0 {
		config.LinkTimeout = defaultLinkTimeout
	}
	if config.LinkTimeout < minimumLinkTimeout {
		config.LinkTimeout = minimumLinkTimeout
	}
	return config
}

func uriConfigured(peers []core.PeerInfo, uri string) bool {
	for _, peer := range peers {
		if peer.URI == uri {
			return true
		}
	}
	return false
}

func peerUpWithURI(peers []core.PeerInfo, uri string) bool {
	for _, peer := range peers {
		if peer.URI == uri && peer.Up {
			return true
		}
	}
	return false
}

func (m *Manager) targetsSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	targets := make([]string, 0, len(m.targets))
	for target := range m.targets {
		targets = append(targets, target)
	}
	slices.Sort(targets)
	return targets
}

func (m *Manager) stateForTarget(target string) *targetState {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.targets[target]
	if state == nil {
		return nil
	}
	cp := *state
	cp.addrs = slices.Clone(state.addrs)
	cp.tried = make(map[string]struct{}, len(state.tried))
	for uri := range state.tried {
		cp.tried[uri] = struct{}{}
	}
	return &cp
}

func (m *Manager) stateForTargetLocked(target string) *targetState {
	state := m.targets[target]
	if state == nil {
		state = &targetState{tried: map[string]struct{}{}}
		m.targets[target] = state
	}
	return state
}

func (m *Manager) markTried(target, uri string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateForTargetLocked(target).tried[uri] = struct{}{}
}

func (m *Manager) rememberOwned(target, uri string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.owned[uri] = ownedLink{target: target, uri: uri, added: time.Now()}
}

func (m *Manager) configSnapshot() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

func (m *Manager) checkInterval() time.Duration {
	return m.configSnapshot().CheckInterval
}

func (m *Manager) signalWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) logf(format string, args ...interface{}) {
	if m != nil && m.log != nil {
		m.log.Infof(format, args...)
	}
}

func resetTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}
