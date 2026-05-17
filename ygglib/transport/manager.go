package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
)

const defaultMappingKey = ""

var (
	ErrClosed                     = errors.New("transport manager closed")
	ErrNoNetwork                  = errors.New("transport network unavailable")
	ErrUnsupportedScheme          = errors.New("transport scheme is not registered")
	ErrTransportAlreadyRegistered = errors.New("transport scheme already registered")
	ErrNilTransport               = errors.New("transport is nil")
	ErrInvalidPattern             = errors.New("transport network pattern is invalid")
	ErrSourceInterfaceUnsupported = errors.New("transport source interface is not supported by selected network")
	ErrUnsupportedNetwork         = errors.New("transport network is not supported")
)

const (
	NetworkKindNative = "native"
)

type Conn = net.Conn

type Listener = net.Listener

// Options carries per-call transport hints.
//
// Transports should apply only the fields they understand and ignore the rest.
// The current built-in TCP and TLS transports treat SourceInterface as a
// best-effort hint: they use it when the selected gonnect.Network and
// underlying sockets support it and otherwise fall back to the normal
// dial/listen path.
type Options struct {
	SourceInterface string
}

// Transport creates or accepts carrier connections for one or more URL
// schemes.
//
// Implementations receive the selected gonnect.Network, the original URL and
// per-call Options. They should ignore URL fields or options they do not use.
type Transport interface {
	Schemes() []string
	Dial(ctx context.Context, network Network, u *url.URL, opts Options) (Conn, error)
	Listen(
		ctx context.Context,
		network Network,
		u *url.URL,
		opts Options,
	) (Listener, error)
}

type Network = gonnect.Network

// Manager routes Dial and Listen calls to registered transports and selects the
// gonnect.Network to use from a default network plus optional host-pattern
// mappings.
//
// Manager is safe for concurrent use. Mapping changes are live: replacing,
// adding or removing a matching network closes all affected listeners,
// dialed connections and listener-accepted child connections so no resource
// survives on the wrong network.
//
// Example:
//
//	mgr := transport.NewManager(defaultNet)
//	if err := mgr.RegisterTransport(transport.NewTCPTransport()); err != nil {
//		return err
//	}
//	if err := mgr.RegisterTransport(transport.NewTLSTransport(tlsCfg)); err != nil {
//		return err
//	}
//
//	u, err := url.Parse("tcp://127.0.0.1:9001")
//	if err != nil {
//		return err
//	}
//	conn, err := mgr.Dial(context.Background(), u)
//	if err != nil {
//		return err
//	}
//	defer conn.Close()
//
// Example with host-specific network mapping:
//
//	mgr := transport.NewManager(defaultNet)
//	_ = mgr.RegisterTransport(transport.NewTCPTransport())
//	_ = mgr.MapNetwork("*.onion", torNet)
//	_ = mgr.MapNetwork("*.i2p", i2pNet)
//
//	conn, err := mgr.Dial(context.Background(), mustParseURL("tcp://peer.onion:9001"))
//
// Example with per-call options:
//
//	ln, err := mgr.ListenWithOptions(
//		context.Background(),
//		mustParseURL("tls://[fe80::1]:0?sni=node.example"),
//		transport.Options{SourceInterface: "eth0"},
//	)
type Manager struct {
	mu         sync.RWMutex
	closed     bool
	version    uint64
	nextID     uint64
	defaultNet Network
	transports map[string]Transport
	mappings   map[string]Network
	resources  map[uint64]*resource
}

type snapshot struct {
	version   uint64
	transport Transport
	network   Network
	mapping   string
	host      string
}

type resourceKind uint8

const (
	resourceConn resourceKind = iota
	resourceListener
)

type resource struct {
	id       uint64
	kind     resourceKind
	host     string
	mapping  string
	parentID uint64
	closer   io.Closer
	children map[uint64]struct{}
}

// NewManager creates a Manager using defaultNet for hosts that do not match a
// more specific mapping.
//
// A nil defaultNet is allowed and causes unmatched Dial and Listen calls to be
// rejected until a non-nil default or explicit matching host mapping is set.
func NewManager(defaultNet Network) *Manager {
	return &Manager{
		defaultNet: defaultNet,
		transports: make(map[string]Transport),
		mappings:   make(map[string]Network),
		resources:  make(map[uint64]*resource),
	}
}

func (m *Manager) RegisterTransport(t Transport) error {
	if t == nil {
		return ErrNilTransport
	}
	schemes := t.Schemes()
	if len(schemes) == 0 {
		return fmt.Errorf("%w: no schemes", ErrInvalidPattern)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	for _, scheme := range schemes {
		scheme = normalizeScheme(scheme)
		if scheme == "" {
			return fmt.Errorf("%w: empty scheme", ErrInvalidPattern)
		}
		if _, exists := m.transports[scheme]; exists {
			return fmt.Errorf("%w: %s", ErrTransportAlreadyRegistered, scheme)
		}
	}
	for _, scheme := range schemes {
		m.transports[normalizeScheme(scheme)] = t
	}
	m.version++
	return nil
}

func (m *Manager) UnregisterTransport(scheme string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	scheme = normalizeScheme(scheme)
	if _, ok := m.transports[scheme]; ok {
		delete(m.transports, scheme)
		m.version++
	}
}

func (m *Manager) HasTransport(scheme string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return false
	}
	_, ok := m.transports[normalizeScheme(scheme)]
	return ok
}

func (m *Manager) DefaultNetwork() Network {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultNet
}

func (m *Manager) NetworkMappings() map[string]Network {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]Network, len(m.mappings))
	for pattern, network := range m.mappings {
		out[pattern] = network
	}
	return out
}

func (m *Manager) SetDefaultNetwork(network Network) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.defaultNet = network
	m.version++
	closers := m.collectClosersLocked(func(res *resource) bool {
		return res.mapping == defaultMappingKey
	})
	m.mu.Unlock()
	closeAll(closers)
}

func (m *Manager) MapNetwork(pattern string, network Network) error {
	pattern, err := normalizePattern(pattern)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	m.mappings[pattern] = network
	m.version++
	closers := m.collectClosersLocked(func(res *resource) bool {
		return hostMatches(pattern, res.host)
	})
	m.mu.Unlock()
	closeAll(closers)
	return nil
}

func (m *Manager) UnmapNetwork(pattern string) error {
	pattern, err := normalizePattern(pattern)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if _, ok := m.mappings[pattern]; !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.mappings, pattern)
	m.version++
	closers := m.collectClosersLocked(func(res *resource) bool {
		return res.mapping == pattern
	})
	m.mu.Unlock()
	closeAll(closers)
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.version++
	closers := m.collectClosersLocked(func(res *resource) bool {
		return true
	})
	m.mu.Unlock()
	closeAll(closers)
	return nil
}

func (m *Manager) Dial(ctx context.Context, u *url.URL) (Conn, error) {
	return m.DialWithOptions(ctx, u, Options{})
}

func (m *Manager) DialWithOptions(
	ctx context.Context,
	u *url.URL,
	opts Options,
) (Conn, error) {
	if u == nil {
		return nil, fmt.Errorf("transport dial: nil url")
	}
	for {
		ss, err := m.snapshotFor(u)
		if err != nil {
			return nil, err
		}
		conn, err := ss.transport.Dial(ctx, ss.network, u, opts)
		if err != nil {
			return nil, err
		}
		tracked, retry, err := m.commitConn(ss, conn)
		if retry {
			_ = conn.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		return tracked, err
	}
}

func (m *Manager) Listen(ctx context.Context, u *url.URL) (Listener, error) {
	return m.ListenWithOptions(ctx, u, Options{})
}

func (m *Manager) ListenWithOptions(
	ctx context.Context,
	u *url.URL,
	opts Options,
) (Listener, error) {
	if u == nil {
		return nil, fmt.Errorf("transport listen: nil url")
	}
	for {
		ss, err := m.snapshotFor(u)
		if err != nil {
			return nil, err
		}
		listener, err := ss.transport.Listen(ctx, ss.network, u, opts)
		if err != nil {
			return nil, err
		}
		tracked, retry, err := m.commitListener(ss, listener)
		if retry {
			_ = listener.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		return tracked, err
	}
}

func (m *Manager) snapshotFor(u *url.URL) (snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return snapshot{}, ErrClosed
	}

	scheme := normalizeScheme(u.Scheme)
	transport, ok := m.transports[scheme]
	if !ok {
		return snapshot{}, fmt.Errorf("%w: %s", ErrUnsupportedScheme, scheme)
	}
	host := normalizeHost(u)
	network, mapping := m.resolveNetworkLocked(host)
	if network == nil {
		return snapshot{}, fmt.Errorf("%w for host %q", ErrNoNetwork, host)
	}
	return snapshot{
		version:   m.version,
		transport: transport,
		network:   network,
		mapping:   mapping,
		host:      host,
	}, nil
}

func (m *Manager) commitConn(ss snapshot, conn net.Conn) (net.Conn, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.version != ss.version {
		return nil, true, nil
	}
	id := m.nextResourceIDLocked()
	wrapped := &trackedConn{
		Conn: conn,
		done: func() {
			m.unregister(id)
		},
	}
	m.resources[id] = &resource{
		id:      id,
		kind:    resourceConn,
		host:    ss.host,
		mapping: ss.mapping,
		closer:  wrapped,
	}
	return wrapped, false, nil
}

func (m *Manager) commitListener(ss snapshot, listener net.Listener) (net.Listener, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.version != ss.version {
		return nil, true, nil
	}
	id := m.nextResourceIDLocked()
	wrapped := &trackedListener{
		Listener: listener,
		manager:  m,
		id:       id,
		done: func() {
			m.unregister(id)
		},
	}
	m.resources[id] = &resource{
		id:       id,
		kind:     resourceListener,
		host:     ss.host,
		mapping:  ss.mapping,
		closer:   wrapped,
		children: make(map[uint64]struct{}),
	}
	return wrapped, false, nil
}

func (m *Manager) registerAcceptedConn(parentID uint64, conn net.Conn) (net.Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, ok := m.resources[parentID]
	if !ok || parent.kind != resourceListener {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	id := m.nextResourceIDLocked()
	wrapped := &trackedConn{
		Conn: conn,
		done: func() {
			m.unregister(id)
		},
	}
	m.resources[id] = &resource{
		id:       id,
		kind:     resourceConn,
		host:     parent.host,
		mapping:  parent.mapping,
		parentID: parentID,
		closer:   wrapped,
	}
	parent.children[id] = struct{}{}
	return wrapped, nil
}

func (m *Manager) unregister(id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, ok := m.resources[id]
	if !ok {
		return
	}
	delete(m.resources, id)
	if res.parentID != 0 {
		if parent, ok := m.resources[res.parentID]; ok && parent.children != nil {
			delete(parent.children, id)
		}
	}
}

func (m *Manager) nextResourceIDLocked() uint64 {
	m.nextID++
	return m.nextID
}

func (m *Manager) resolveNetworkLocked(host string) (Network, string) {
	bestPattern := ""
	bestScore := -1
	bestWildcards := 0
	for pattern := range m.mappings {
		if !hostMatches(pattern, host) {
			continue
		}
		score, wildcards := patternScore(pattern)
		if score > bestScore || (score == bestScore && wildcards < bestWildcards) {
			bestPattern = pattern
			bestScore = score
			bestWildcards = wildcards
		}
	}
	if bestScore >= 0 {
		return m.mappings[bestPattern], bestPattern
	}
	return m.defaultNet, defaultMappingKey
}

func (m *Manager) collectClosersLocked(match func(*resource) bool) []io.Closer {
	var closers []io.Closer
	seen := make(map[uint64]struct{})
	for id, res := range m.resources {
		if !match(res) {
			continue
		}
		m.collectResourceTreeLocked(id, &closers, seen)
	}
	return closers
}

func (m *Manager) collectResourceTreeLocked(id uint64, closers *[]io.Closer, seen map[uint64]struct{}) {
	if _, ok := seen[id]; ok {
		return
	}
	res, ok := m.resources[id]
	if !ok {
		return
	}
	seen[id] = struct{}{}
	*closers = append(*closers, res.closer)
	for childID := range res.children {
		m.collectResourceTreeLocked(childID, closers, seen)
	}
}

type trackedConn struct {
	net.Conn
	once sync.Once
	done func()
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		if c.done != nil {
			c.done()
		}
		err = c.Conn.Close()
	})
	return err
}

type trackedListener struct {
	net.Listener
	manager *Manager
	id      uint64
	once    sync.Once
	done    func()
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.manager.registerAcceptedConn(l.id, conn)
}

func (l *trackedListener) Close() error {
	var err error
	l.once.Do(func() {
		if l.done != nil {
			l.done()
		}
		err = l.Listener.Close()
	})
	return err
}

func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}

func NewBuiltinNetwork(kind string) (Network, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case NetworkKindNative:
		network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build())
		if err := network.Up(); err != nil {
			return nil, err
		}
		return network, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedNetwork, kind)
	}
}

func BuiltinNetworkName(network Network) (string, bool) {
	if _, ok := network.(*gonnect.NativeNetwork); ok {
		return NetworkKindNative, true
	}
	if _, ok := gonnect.GetWrapped(network).(*gonnect.NativeNetwork); ok {
		return NetworkKindNative, true
	}
	return "", false
}

func normalizeScheme(scheme string) string {
	return strings.ToLower(strings.TrimSpace(scheme))
}

func normalizeHost(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		host = u.Host
	}
	return strings.ToLower(strings.TrimSpace(host))
}

func normalizePattern(pattern string) (string, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return "", ErrInvalidPattern
	}
	if _, err := path.Match(pattern, "example"); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPattern, err)
	}
	return pattern, nil
}

func hostMatches(pattern, host string) bool {
	ok, err := path.Match(pattern, host)
	return err == nil && ok
}

func patternScore(pattern string) (score int, wildcards int) {
	for _, r := range pattern {
		if r == '*' || r == '?' || r == '[' || r == ']' {
			wildcards++
			continue
		}
		score++
	}
	return score, wildcards
}
