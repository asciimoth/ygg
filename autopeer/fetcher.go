package autopeer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"time"
)

const defaultFetchInterval = time.Hour

type sourceSnapshot struct {
	source  string
	network Network
}

// Fetcher periodically fetches public peers documents from configured sources.
//
// Sources are URL strings or BuiltinSource. Non-built-in sources are fetched
// over the default gonnect.Network or an optional per-source override. When the
// effective network for a source is nil, that source is skipped and its last
// contributed peers are removed from the local aggregate.
//
// Fetcher is safe for concurrent use.
type Fetcher struct {
	logger Logger

	mu             sync.RWMutex
	interval       time.Duration
	active         bool
	closed         bool
	defaultNetwork Network
	sourceOrder    []string
	sourceNetworks map[string]Network
	sourcePeers    map[string][]Peer
	peers          []Peer
	onChange       func([]Peer)

	started chan struct{}
	stop    chan struct{}
	done    chan struct{}
	wake    chan struct{}

	builtinData  []byte
	builtinOnce  sync.Once
	builtinPeers []Peer
	builtinErr   error
}

// NewFetcher constructs a Fetcher.
func NewFetcher(logger Logger, interval time.Duration) *Fetcher {
	if interval <= 0 {
		interval = defaultFetchInterval
	}
	return &Fetcher{
		logger:         logger,
		interval:       interval,
		sourceNetworks: make(map[string]Network),
		sourcePeers:    make(map[string][]Peer),
		started:        make(chan struct{}),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
		wake:           make(chan struct{}, 1),
		builtinData:    builtinPeersJSON,
	}
}

// Start activates the periodic fetch loop. It returns false if already active.
func (f *Fetcher) Start() bool {
	f.mu.Lock()
	if f.active || f.closed {
		f.mu.Unlock()
		return false
	}
	f.active = true
	close(f.started)
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)

	go f.loop()
	return true
}

// Close stops the periodic fetch loop. It is safe to call more than once.
func (f *Fetcher) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	active := f.active
	f.mu.Unlock()

	if !active {
		close(f.done)
		return nil
	}
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
	<-f.done
	return nil
}

// SetDefaultNetwork replaces the default network used for non-overridden
// sources.
func (f *Fetcher) SetDefaultNetwork(network Network) {
	f.mu.Lock()
	f.defaultNetwork = network
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)
	f.signalWake()
}

// SetSourceNetwork sets a source-specific network override. Passing nil
// explicitly disables network fetching for that source until the override is
// removed.
func (f *Fetcher) SetSourceNetwork(source string, network Network) {
	f.mu.Lock()
	f.sourceNetworks[source] = network
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)
	f.signalWake()
}

// RemoveSourceNetwork removes a source-specific network override and falls back
// to the default network.
func (f *Fetcher) RemoveSourceNetwork(source string) {
	f.mu.Lock()
	delete(f.sourceNetworks, source)
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)
	f.signalWake()
}

// SetSources replaces the active source list.
func (f *Fetcher) SetSources(sources []string) {
	f.mu.Lock()
	f.sourceOrder = slices.Clone(sources)
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)
	f.signalWake()
}

// AddSource appends a source if it is not already configured.
func (f *Fetcher) AddSource(source string) {
	f.mu.Lock()
	if !slices.Contains(f.sourceOrder, source) {
		f.sourceOrder = append(f.sourceOrder, source)
	}
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)
	f.signalWake()
}

// RemoveSource removes a source and its contributed peers.
func (f *Fetcher) RemoveSource(source string) {
	f.mu.Lock()
	idx := slices.Index(f.sourceOrder, source)
	if idx >= 0 {
		f.sourceOrder = append(f.sourceOrder[:idx], f.sourceOrder[idx+1:]...)
	}
	delete(f.sourceNetworks, source)
	callbackPeers, changed := f.reconcileLocked()
	f.mu.Unlock()
	f.fireCallback(callbackPeers, changed)
}

// SetOnChange sets the optional aggregate-peers change callback.
func (f *Fetcher) SetOnChange(fn func([]Peer)) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

// Peers returns a snapshot of the current aggregated peers.
func (f *Fetcher) Peers() []Peer {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return clonePeers(f.peers)
}

// Sources returns the current ordered source list.
func (f *Fetcher) Sources() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return slices.Clone(f.sourceOrder)
}

// Interval returns the current fetch interval.
func (f *Fetcher) Interval() time.Duration {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.interval
}

// FetchNow fetches one source immediately, outside the normal periodic order.
func (f *Fetcher) FetchNow(ctx context.Context, source string) error {
	if source == BuiltinSource {
		f.mu.Lock()
		callbackPeers, changed := f.reconcileLocked()
		f.mu.Unlock()
		f.fireCallback(callbackPeers, changed)
		return nil
	}

	f.mu.RLock()
	network, ok := f.networkForSourceLocked(source)
	known := slices.Contains(f.sourceOrder, source)
	f.mu.RUnlock()
	if !known {
		return fmt.Errorf("autopeer source not configured: %s", source)
	}
	if !ok || network == nil {
		return nil
	}

	peers, err := f.fetchSource(ctx, source, network)
	if err != nil {
		f.logf("autopeer fetch %s failed: %v", source, err)
		return err
	}

	callbackPeers, changed := f.replaceSourcePeers(source, network, peers)
	f.fireCallback(callbackPeers, changed)
	return nil
}

func (f *Fetcher) loop() {
	defer func() {
		f.mu.Lock()
		f.active = false
		f.mu.Unlock()
		close(f.done)
	}()

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	f.fetchAll(context.Background())
	for {
		select {
		case <-f.stop:
			return
		case <-f.wake:
			f.fetchAll(context.Background())
		case <-ticker.C:
			f.fetchAll(context.Background())
		}
	}
}

func (f *Fetcher) fetchAll(ctx context.Context) {
	for _, item := range f.fetchList() {
		peers, err := f.fetchSource(ctx, item.source, item.network)
		if err != nil {
			f.logf("autopeer fetch %s failed: %v", item.source, err)
			continue
		}
		callbackPeers, changed := f.replaceSourcePeers(item.source, item.network, peers)
		f.fireCallback(callbackPeers, changed)
	}
}

func (f *Fetcher) fetchList() []sourceSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()

	list := make([]sourceSnapshot, 0, len(f.sourceOrder))
	for _, source := range f.sourceOrder {
		if source == BuiltinSource {
			continue
		}
		network, ok := f.networkForSourceLocked(source)
		if !ok || network == nil {
			continue
		}
		list = append(list, sourceSnapshot{source: source, network: network})
	}
	return list
}

func (f *Fetcher) replaceSourcePeers(source string, fetchedVia Network, peers []Peer) ([]Peer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !slices.Contains(f.sourceOrder, source) {
		return nil, false
	}
	if source != BuiltinSource {
		network, ok := f.networkForSourceLocked(source)
		if !ok || network == nil || !sameNetwork(network, fetchedVia) {
			delete(f.sourcePeers, source)
			return f.rebuildPeersLocked()
		}
	}
	f.sourcePeers[source] = clonePeers(peers)
	return f.rebuildPeersLocked()
}

func sameNetwork(a, b Network) bool {
	if a == nil || b == nil {
		return a == b
	}
	ta := reflect.TypeOf(a)
	tb := reflect.TypeOf(b)
	if ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}

func (f *Fetcher) reconcileLocked() ([]Peer, bool) {
	active := make(map[string]struct{}, len(f.sourceOrder))
	for _, source := range f.sourceOrder {
		active[source] = struct{}{}
	}
	for source := range f.sourcePeers {
		if _, ok := active[source]; !ok {
			delete(f.sourcePeers, source)
			continue
		}
		if source == BuiltinSource {
			continue
		}
		network, ok := f.networkForSourceLocked(source)
		if !ok || network == nil {
			delete(f.sourcePeers, source)
		}
	}

	if slices.Contains(f.sourceOrder, BuiltinSource) {
		peers, err := f.getBuiltinPeers()
		if err != nil {
			f.logf("autopeer fetch %s failed: %v", BuiltinSource, err)
			delete(f.sourcePeers, BuiltinSource)
		} else {
			f.sourcePeers[BuiltinSource] = peers
		}
	} else {
		delete(f.sourcePeers, BuiltinSource)
	}

	return f.rebuildPeersLocked()
}

func (f *Fetcher) rebuildPeersLocked() ([]Peer, bool) {
	updated := make([]Peer, 0)
	for _, source := range f.sourceOrder {
		updated = append(updated, f.sourcePeers[source]...)
	}
	if reflect.DeepEqual(f.peers, updated) {
		return nil, false
	}
	f.peers = clonePeers(updated)
	return clonePeers(updated), true
}

func (f *Fetcher) getBuiltinPeers() ([]Peer, error) {
	f.builtinOnce.Do(func() {
		f.builtinPeers, f.builtinErr = parsePeers(BuiltinSource, f.builtinData)
	})
	return clonePeers(f.builtinPeers), f.builtinErr
}

func (f *Fetcher) networkForSourceLocked(source string) (Network, bool) {
	if source == BuiltinSource {
		return nil, false
	}
	network, ok := f.sourceNetworks[source]
	if ok {
		return network, true
	}
	return f.defaultNetwork, true
}

func (f *Fetcher) fetchSource(
	ctx context.Context,
	source string,
	network Network,
) ([]Peer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, dialNetwork, addr string) (net.Conn, error) {
				return network.Dial(ctx, dialNetwork, addr)
			},
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
			ForceAttemptHTTP2: true,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	return parsePeers(source, json.NewDecoder(resp.Body))
}

func parsePeers(source string, input any) ([]Peer, error) {
	var decoder *json.Decoder
	switch v := input.(type) {
	case []byte:
		decoder = json.NewDecoder(bytes.NewReader(v))
	case *json.Decoder:
		decoder = v
	default:
		return nil, errors.New("unsupported parser input")
	}
	decoder.DisallowUnknownFields()

	var doc Document
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if err := validateDocument(doc); err != nil {
		return nil, err
	}

	peers := clonePeers(doc.Peers)
	for i := range peers {
		peers[i].Source = source
	}
	return peers, nil
}

func validateDocument(doc Document) error {
	if doc.SchemaVersion == "" {
		return errors.New("missing schema_version")
	}
	if doc.GeneratedAt == "" {
		return errors.New("missing generated_at")
	}
	if doc.Sources.PublicPeersRepo == "" {
		return errors.New("missing sources.public_peers_repo")
	}
	if doc.Sources.PublicPeersBranch == "" {
		return errors.New("missing sources.public_peers_branch")
	}
	if doc.Sources.UptimePage == "" {
		return errors.New("missing sources.uptime_page")
	}
	for i, peer := range doc.Peers {
		if peer.Continent == "" {
			return fmt.Errorf("peer %d: missing continent", i)
		}
		if peer.Country == "" {
			return fmt.Errorf("peer %d: missing country", i)
		}
		if peer.SourceFile == "" {
			return fmt.Errorf("peer %d: missing source_file", i)
		}
		if len(peer.Endpoints) == 0 {
			return fmt.Errorf("peer %d: missing endpoints", i)
		}
		for j, endpoint := range peer.Endpoints {
			if endpoint.URL == "" {
				return fmt.Errorf("peer %d endpoint %d: missing url", i, j)
			}
			if endpoint.Protocol == "" {
				return fmt.Errorf("peer %d endpoint %d: missing protocol", i, j)
			}
			if endpoint.Host == "" {
				return fmt.Errorf("peer %d endpoint %d: missing host", i, j)
			}
			if endpoint.Port < 1 || endpoint.Port > 65535 {
				return fmt.Errorf("peer %d endpoint %d: invalid port", i, j)
			}
		}
	}
	return nil
}

func clonePeers(in []Peer) []Peer {
	if len(in) == 0 {
		return nil
	}
	out := make([]Peer, len(in))
	for i, peer := range in {
		out[i] = peer
		if len(peer.Endpoints) > 0 {
			out[i].Endpoints = make([]Endpoint, len(peer.Endpoints))
			for j, endpoint := range peer.Endpoints {
				out[i].Endpoints[j] = endpoint
				out[i].Endpoints[j].Annotations = slices.Clone(endpoint.Annotations)
				if endpoint.Uptime != nil {
					uptime := *endpoint.Uptime
					out[i].Endpoints[j].Uptime = &uptime
				}
			}
		}
	}
	return out
}

func (f *Fetcher) signalWake() {
	select {
	case <-f.started:
		select {
		case f.wake <- struct{}{}:
		default:
		}
	default:
	}
}

func (f *Fetcher) fireCallback(peers []Peer, changed bool) {
	if !changed {
		return
	}
	f.mu.RLock()
	callback := f.onChange
	f.mu.RUnlock()
	if callback != nil {
		callback(clonePeers(peers))
	}
}

func (f *Fetcher) logf(format string, args ...any) {
	if f.logger != nil {
		f.logger.Printf(format, args...)
	}
}
