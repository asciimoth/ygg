package sockstun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

const (
	DefaultName   = "sockstun"
	DefaultListen = "127.0.0.1:1080"
)

type Config struct {
	Name             string
	Listen           string
	Address          net.IP
	MTU              uint64
	MWO              int
	MRO              int
	Proxies          []ProxyConfig
	DefaultProxyURL  string
	DNS              DNSConfig
	HandshakeTimeout time.Duration
}

type Tun struct {
	*vtun.VTun

	listener net.Listener
	network  *routeNetwork
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	once     sync.Once
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func Create(cfg Config) (*Tun, error) {
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1500
	}

	addr, ok := netip.AddrFromSlice(cfg.Address)
	if !ok {
		return nil, fmt.Errorf("invalid VTun address %q", cfg.Address.String())
	}

	vt, err := (&vtun.Opts{
		Name:           cfg.Name,
		LocalAddrs:     []netip.Addr{addr},
		NoLoopbackAddr: true,
		MWO:            cfg.MWO,
		MRO:            cfg.MRO,
		NetStackOpts: &helpers.Opts{
			MTU: int(cfg.MTU),
		},
	}).Build()
	if err != nil {
		return nil, fmt.Errorf("build VTun: %w", err)
	}

	network, err := newRouteNetwork(vt, cfg.Proxies, cfg.DefaultProxyURL, cfg.DNS)
	if err != nil {
		_ = vt.Close()
		return nil, err
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		_ = vt.Close()
		return nil, fmt.Errorf("listen socks %q: %w", cfg.Listen, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Tun{
		VTun:     vt,
		listener: listener,
		network:  network,
		cancel:   cancel,
		conns:    make(map[net.Conn]struct{}),
	}

	server := newSocksServer(cfg, network)

	t.wg.Add(1)
	go t.serve(ctx, server)

	return t, nil
}

func newSocksServer(cfg Config, network *routeNetwork) *socksgo.Server {
	return &socksgo.Server{
		Auth:              (&protocol.AuthHandlers{}).Add(&protocol.NoAuthHandler{}),
		Dialer:            network.Dial,
		Listener:          network.Listen,
		PacketDialer:      network.PacketDial,
		PacketListener:    network.ListenPacket,
		Resolver:          network,
		DefaultListenHost: cfg.Address.String(),
		HandshakeTimeout:  cfg.HandshakeTimeout,
	}
}

func (t *Tun) SocksAddr() net.Addr {
	if t == nil || t.listener == nil {
		return nil
	}
	return t.listener.Addr()
}

func (t *Tun) SetProxies(cfgs []ProxyConfig, defaultProxyURL string) error {
	if t == nil || t.network == nil {
		return net.ErrClosed
	}
	return t.network.SetProxies(cfgs, defaultProxyURL)
}

func (t *Tun) Proxies() []ProxyConfig {
	if t == nil || t.network == nil {
		return nil
	}
	return t.network.Proxies()
}

func (t *Tun) DefaultProxyURL() string {
	if t == nil || t.network == nil {
		return ""
	}
	return t.network.DefaultProxyURL()
}

func (t *Tun) SetDNS(cfg DNSConfig) error {
	if t == nil || t.network == nil {
		return net.ErrClosed
	}
	return t.network.SetDNS(cfg)
}

func (t *Tun) DNS() DNSConfig {
	if t == nil || t.network == nil {
		return DNSConfig{}
	}
	return t.network.DNS()
}

func (t *Tun) Close() error {
	var err error
	t.once.Do(func() {
		t.cancel()
		if t.listener != nil {
			err = t.listener.Close()
		}
		t.closeConns()
		t.wg.Wait()
		if vtErr := t.VTun.Close(); vtErr != nil && err == nil {
			err = vtErr
		}
	})
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (t *Tun) serve(ctx context.Context, server *socksgo.Server) {
	defer t.wg.Done()
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				return
			}
		}
		t.addConn(conn)
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			defer t.removeConn(conn)
			_ = server.Accept(ctx, conn, false)
		}()
	}
}

func (t *Tun) addConn(conn net.Conn) {
	t.mu.Lock()
	t.conns[conn] = struct{}{}
	t.mu.Unlock()
}

func (t *Tun) removeConn(conn net.Conn) {
	t.mu.Lock()
	delete(t.conns, conn)
	t.mu.Unlock()
}

func (t *Tun) closeConns() {
	t.mu.Lock()
	conns := make([]net.Conn, 0, len(t.conns))
	for conn := range t.conns {
		conns = append(conns, conn)
	}
	t.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}
