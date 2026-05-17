package outproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

const (
	DefaultName   = "outproxy"
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
	HandshakeTimeout time.Duration
	Log              Logger
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

	outbound := gonnect.NativeConfig{}.Build()

	network, err := newRouteNetwork(outbound, cfg.Proxies, cfg.DefaultProxyURL, cfg.Log)
	if err != nil {
		_ = vt.Close()
		return nil, err
	}

	listen := yggdrasilListenAddress(cfg.Listen, cfg.Address)
	listener, err := vt.Listen(context.Background(), "tcp", listen)
	if err != nil {
		_ = vt.Close()
		return nil, fmt.Errorf("listen outproxy socks %q: %w", listen, err)
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

func (t *Tun) Network() gonnect.Network {
	if t == nil {
		return nil
	}
	return t.network
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

func yggdrasilListenAddress(listen string, address net.IP) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return listen
	}
	if shouldReplaceListenHost(host) {
		return net.JoinHostPort(address.String(), port)
	}
	return listen
}

func shouldReplaceListenHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}
