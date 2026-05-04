package autopeer

import (
	"math/rand"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultManagerCheckInterval = time.Minute

// PeerManager is the minimal peer-management surface required by Manager.
type PeerManager interface {
	AddPeer(*url.URL, string) error
	PeerURIs() []string
	ConnectedPeerURIs() []string
}

// ManagerConfig controls the Manager autopeering policy.
type ManagerConfig struct {
	CheckInterval             time.Duration
	MinimumConnected          int
	MinimumConnectedFromFetch int
	Countries                 []string
	TransportSchemes          []string
}

type managerSnapshot struct {
	core       PeerManager
	config     ManagerConfig
	countries  map[string]struct{}
	transports map[string]struct{}
}

type endpointCandidate struct {
	uri   string
	score float64
}

// Manager combines public-peer discovery with runtime peer-management policy.
//
// The manager always owns a Fetcher. When active, it periodically checks the
// configured runtime conditions against the current PeerManager state. If the
// conditions are not met, it selects one eligible endpoint from the fetcher
// snapshot and adds it to the peer manager.
//
// Countries and transport schemes are optional independent filters, but at
// least one of them must be configured before the manager will attempt to add
// any peers.
type Manager struct {
	fetcher *Fetcher

	mu     sync.RWMutex
	core   PeerManager
	config ManagerConfig
	active bool
	closed bool

	stop chan struct{}
	done chan struct{}
	wake chan struct{}

	randFloat64 func() float64
}

// NewManager constructs an autopeering manager around a fetcher.
func NewManager(fetcher *Fetcher) *Manager {
	return &Manager{
		fetcher:     fetcher,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		wake:        make(chan struct{}, 1),
		randFloat64: rand.Float64,
		config:      ManagerConfig{CheckInterval: defaultManagerCheckInterval},
	}
}

// Fetcher returns the underlying public peers fetcher.
func (m *Manager) Fetcher() *Fetcher {
	if m == nil {
		return nil
	}
	return m.fetcher
}

// SetPeerManager replaces the runtime peer manager used for connectivity checks
// and AddPeer calls.
func (m *Manager) SetPeerManager(core PeerManager) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
	m.signalWake()
}

// SetConfig replaces the current autopeering policy.
func (m *Manager) SetConfig(config ManagerConfig) {
	if m == nil {
		return
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = defaultManagerCheckInterval
	}
	config.Countries = slices.Clone(config.Countries)
	config.TransportSchemes = slices.Clone(config.TransportSchemes)

	m.mu.Lock()
	m.config = config
	m.mu.Unlock()
	m.signalWake()
}

// Config returns a copy of the current autopeering policy.
func (m *Manager) Config() ManagerConfig {
	if m == nil {
		return ManagerConfig{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	config := m.config
	config.Countries = slices.Clone(config.Countries)
	config.TransportSchemes = slices.Clone(config.TransportSchemes)
	return config
}

// Start starts the manager loop and the underlying fetcher.
func (m *Manager) Start() bool {
	if m == nil || m.fetcher == nil {
		return false
	}

	m.mu.Lock()
	if m.active || m.closed {
		m.mu.Unlock()
		return false
	}
	m.active = true
	m.mu.Unlock()

	m.fetcher.Start()
	go m.loop()
	m.signalWake()
	return true
}

// Close stops the manager loop and the underlying fetcher.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	var fetcherErr error
	if m.fetcher != nil {
		fetcherErr = m.fetcher.Close()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fetcherErr
	}
	m.closed = true
	active := m.active
	m.mu.Unlock()

	if !active {
		close(m.done)
		return fetcherErr
	}
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	<-m.done
	return fetcherErr
}

// Peers returns the current public peers snapshot.
func (m *Manager) Peers() []Peer {
	if m == nil || m.fetcher == nil {
		return nil
	}
	return m.fetcher.Peers()
}

// SetOnChange installs the fetcher change callback.
func (m *Manager) SetOnChange(fn func([]Peer)) {
	if m == nil || m.fetcher == nil {
		return
	}
	m.fetcher.SetOnChange(fn)
}

func (m *Manager) loop() {
	defer func() {
		m.mu.Lock()
		m.active = false
		m.mu.Unlock()
		close(m.done)
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

func (m *Manager) checkInterval() time.Duration {
	if m == nil {
		return defaultManagerCheckInterval
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config.CheckInterval <= 0 {
		return defaultManagerCheckInterval
	}
	return m.config.CheckInterval
}

func (m *Manager) checkNow() {
	if m == nil || m.fetcher == nil {
		return
	}

	snapshot := m.snapshot()
	if len(snapshot.countries) == 0 && len(snapshot.transports) == 0 {
		m.logf("autopeer manager idle: no country or transport filters configured")
		return
	}
	if snapshot.core == nil {
		m.logf("autopeer manager idle: no peer manager configured")
		return
	}
	if snapshot.config.MinimumConnected <= 0 && snapshot.config.MinimumConnectedFromFetch <= 0 {
		m.logf("autopeer manager idle: no conditions configured")
		return
	}

	connected := uniqueNonEmpty(snapshot.core.ConnectedPeerURIs())
	filtered, selected, err := m.selectCandidate(snapshot, uniqueNonEmpty(snapshot.core.PeerURIs()))
	if err != nil {
		m.logf("autopeer manager selection failed: %v", err)
		return
	}

	connectedTotal := len(connected)
	connectedPublic := countIntersection(connected, filtered)
	if connectedTotal >= snapshot.config.MinimumConnected &&
		connectedPublic >= snapshot.config.MinimumConnectedFromFetch {
		return
	}

	if selected == nil {
		m.logf(
			"autopeer manager conditions unmet: connected=%d/%d public=%d/%d, but no eligible peers remain",
			connectedTotal, snapshot.config.MinimumConnected,
			connectedPublic, snapshot.config.MinimumConnectedFromFetch,
		)
		return
	}

	peerURL, err := url.Parse(selected.uri)
	if err != nil {
		m.logf("autopeer manager rejected candidate %q: %v", selected.uri, err)
		return
	}

	m.logf(
		"autopeer manager conditions unmet: connected=%d/%d public=%d/%d, adding %s",
		connectedTotal, snapshot.config.MinimumConnected,
		connectedPublic, snapshot.config.MinimumConnectedFromFetch,
		selected.uri,
	)
	if err := snapshot.core.AddPeer(peerURL, ""); err != nil {
		m.logf("autopeer manager add %s failed: %v", selected.uri, err)
		return
	}
	m.logf("autopeer manager added %s", selected.uri)
}

func (m *Manager) snapshot() managerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	config := m.config
	config.Countries = slices.Clone(config.Countries)
	config.TransportSchemes = slices.Clone(config.TransportSchemes)
	return managerSnapshot{
		core:       m.core,
		config:     config,
		countries:  normalizeSet(config.Countries),
		transports: normalizeSet(config.TransportSchemes),
	}
}

func (m *Manager) selectCandidate(
	snapshot managerSnapshot,
	existing map[string]struct{},
) (map[string]struct{}, *endpointCandidate, error) {
	filtered := make(map[string]struct{})
	var selected *endpointCandidate
	for _, peer := range m.fetcher.Peers() {
		if !peerMatchesCountries(peer, snapshot.countries) {
			continue
		}
		for _, endpoint := range peer.Endpoints {
			if !endpointMatchesSchemes(endpoint, snapshot.transports) {
				continue
			}
			if strings.TrimSpace(endpoint.URL) == "" {
				continue
			}
			filtered[endpoint.URL] = struct{}{}
			if _, ok := existing[endpoint.URL]; ok {
				continue
			}
			score, err := m.endpointScore(endpoint)
			if err != nil {
				return filtered, nil, err
			}
			candidate := &endpointCandidate{uri: endpoint.URL, score: score}
			if selected == nil || candidate.score > selected.score {
				selected = candidate
			}
		}
	}
	return filtered, selected, nil
}

func (m *Manager) endpointScore(endpoint Endpoint) (float64, error) {
	score, err := uptimeScore(endpoint)
	if err != nil {
		return 0, err
	}
	return score + (m.randFloat64() / 1000), nil
}

func uptimeScore(endpoint Endpoint) (float64, error) {
	if endpoint.Uptime == nil {
		return 0, nil
	}

	raw := strings.TrimSpace(endpoint.Uptime.Uptime7DRaw)
	raw = strings.TrimSuffix(raw, "%")
	if raw == "" {
		return endpoint.Uptime.Uptime7DPercent, nil
	}

	score, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	return score, nil
}

func peerMatchesCountries(peer Peer, countries map[string]struct{}) bool {
	if len(countries) == 0 {
		return true
	}
	_, ok := countries[strings.ToLower(strings.TrimSpace(peer.Country))]
	return ok
}

func endpointMatchesSchemes(endpoint Endpoint, schemes map[string]struct{}) bool {
	if len(schemes) == 0 {
		return true
	}
	scheme := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
	if scheme == "" {
		parsed, err := url.Parse(endpoint.URL)
		if err == nil {
			scheme = strings.ToLower(parsed.Scheme)
		}
	}
	_, ok := schemes[scheme]
	return ok
}

func normalizeSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	return set
}

func uniqueNonEmpty(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	return set
}

func countIntersection(a, b map[string]struct{}) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	count := 0
	for key := range a {
		if _, ok := b[key]; ok {
			count++
		}
	}
	return count
}

func (m *Manager) signalWake() {
	if m == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) logf(format string, args ...interface{}) {
	if m == nil {
		return
	}
	logger := m.logger()
	if logger != nil {
		logger.Printf(format, args...)
	}
}

func (m *Manager) logger() Logger {
	if m == nil || m.fetcher == nil {
		return nil
	}
	return m.fetcher.logger
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
