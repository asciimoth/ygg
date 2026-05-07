// lib_tutorial is the runnable companion for ../../lib-tutorial.md.
//
// It builds two in-process Yggdrasil cores, registers a custom transport,
// routes that transport through an explicit host-to-network mapping, attaches a
// VTun to each core, then runs TCP, UDP, and HTTP traffic through the overlay.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/loopback"

	"github.com/asciimoth/ygg/ygglib/autopeer"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/ipv6rwc"
	ygglogger "github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/multicast"
	"github.com/asciimoth/ygg/ygglib/transport"
	yggtun "github.com/asciimoth/ygg/ygglib/tun"
)

const (
	readyTimeout   = 5 * time.Second
	requestTimeout = 10 * time.Second
)

func main() {
	logger := log.New(os.Stdout, "lib_tutorial: ", 0)

	demo, err := newDemo()
	if err != nil {
		logger.Fatal(err)
	}
	defer func() {
		if err := demo.Close(); err != nil {
			logger.Printf("cleanup error: %v", err)
		}
	}()

	tcpBody, err := runTCPDemo(demo.client.vt, demo.server.vt, demo.server.core.Address())
	if err != nil {
		logger.Fatal(err)
	}
	udpBody, err := runUDPDemo(demo.client.vt, demo.server.vt, demo.server.core.Address())
	if err != nil {
		logger.Fatal(err)
	}
	httpBody, err := runHTTPDemo(demo.client.vt, demo.server.vt, demo.server.core.Address())
	if err != nil {
		logger.Fatal(err)
	}

	logger.Printf("server address: %s", demo.server.core.Address())
	logger.Printf("client address: %s", demo.client.core.Address())
	logger.Printf("transport dials: %d", demo.transport.Dials())
	logger.Printf("transport listens: %d", demo.transport.Listens())
	logger.Printf("tcp reply: %q", tcpBody)
	logger.Printf("udp reply: %q", udpBody)
	logger.Printf("http reply: %q", httpBody)
}

type demo struct {
	network   *loopback.LoopbackNetwork
	transport *meteredTransport
	server    *node
	client    *node
}

func newDemo() (*demo, error) {
	network := loopback.NewLoopbackNetwok()
	baseTransport := transport.NewTCPTransport()
	metered := &meteredTransport{base: baseTransport}

	server, err := newNode("server", network, metered)
	if err != nil {
		return nil, err
	}
	client, err := newNode("client", network, metered)
	if err != nil {
		_ = server.Close()
		return nil, err
	}

	d := &demo{
		network:   network,
		transport: metered,
		server:    server,
		client:    client,
	}
	if err := d.connect(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func (d *demo) connect() error {
	listenURL := mustParseURL("metered+tcp://127.0.0.1:0")
	listener, err := d.server.core.Listen(listenURL, "")
	if err != nil {
		return fmt.Errorf("start yggdrasil listener: %w", err)
	}

	peerURL := mustParseURL("metered+tcp://" + listener.Addr().String())
	if err := d.client.core.CallPeer(peerURL, ""); err != nil {
		return fmt.Errorf("connect client core to server core: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()
	if err := waitFor(ctx, func() bool {
		return peerIsUp(d.server.core.GetPeers()) && peerIsUp(d.client.core.GetPeers())
	}); err != nil {
		return fmt.Errorf("wait for yggdrasil peering: %w", err)
	}
	return nil
}

func (d *demo) Close() error {
	var firstErr error
	for _, node := range []*node{d.client, d.server} {
		if node == nil {
			continue
		}
		if err := node.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.network != nil {
		if err := d.network.Down(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type meteredTransport struct {
	base    transport.Transport
	dials   atomic.Uint64
	listens atomic.Uint64
}

func (t *meteredTransport) Schemes() []string {
	return []string{"metered+tcp"}
}

func (t *meteredTransport) Dial(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Conn, error) {
	t.dials.Add(1)
	return t.base.Dial(ctx, network, rewriteScheme(u, "tcp"), opts)
}

func (t *meteredTransport) Listen(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Listener, error) {
	t.listens.Add(1)
	return t.base.Listen(ctx, network, rewriteScheme(u, "tcp"), opts)
}

func (t *meteredTransport) Dials() uint64 {
	return t.dials.Load()
}

func (t *meteredTransport) Listens() uint64 {
	return t.listens.Load()
}

type node struct {
	core    *core.Core
	rwc     *ipv6rwc.ReadWriteCloser
	adapter *yggtun.TunAdapter
	vt      *vtun.VTun
}

func newNode(name string, carrierNetwork transport.Network, carrierTransport transport.Transport) (*node, error) {
	cfg := config.GenerateConfig()
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}

	manager, err := newTransportManager(carrierNetwork, carrierTransport, cfg.Certificate)
	if err != nil {
		return nil, err
	}

	logger := ygglogger.Discard()
	coreNode, err := core.New(
		cfg.Certificate,
		logger,
		core.TransportManager{Manager: manager},
	)
	if err != nil {
		return nil, fmt.Errorf("create core: %w", err)
	}

	rwc := ipv6rwc.NewReadWriteCloser(coreNode)
	adapter, err := yggtun.New(rwc, logger, yggtun.InterfaceMTU(1500))
	if err != nil {
		coreNode.Stop()
		_ = rwc.Close()
		return nil, fmt.Errorf("create tun adapter: %w", err)
	}

	vt, err := newVTun(name, coreNode.Address())
	if err != nil {
		_ = adapter.Stop()
		_ = rwc.Close()
		coreNode.Stop()
		return nil, err
	}
	if err := adapter.Attach(vt, yggtun.AttachmentType("vtun")); err != nil {
		_ = vt.Close()
		_ = adapter.Stop()
		_ = rwc.Close()
		coreNode.Stop()
		return nil, fmt.Errorf("attach vtun: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()
	if err := waitFor(ctx, func() bool {
		status := adapter.Status()
		return status.Attached && status.Enabled && status.Type == "vtun"
	}); err != nil {
		_ = vt.Close()
		_ = adapter.Stop()
		_ = rwc.Close()
		coreNode.Stop()
		return nil, fmt.Errorf("wait for vtun attachment: %w", err)
	}

	return &node{
		core:    coreNode,
		rwc:     rwc,
		adapter: adapter,
		vt:      vt,
	}, nil
}

func (n *node) Close() error {
	var firstErr error
	if n.adapter != nil {
		if err := n.adapter.Stop(); err != nil {
			firstErr = err
		}
	}
	if n.rwc != nil {
		if err := n.rwc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if n.core != nil {
		n.core.Stop()
	}
	return firstErr
}

func newTransportManager(
	mappedNetwork transport.Network,
	customTransport transport.Transport,
	cert *tls.Certificate,
) (*transport.Manager, error) {
	manager := transport.NewManager(nil)
	if err := manager.MapNetwork("127.0.0.1", mappedNetwork); err != nil {
		return nil, fmt.Errorf("map carrier network: %w", err)
	}
	if err := manager.RegisterTransport(customTransport); err != nil {
		return nil, fmt.Errorf("register custom transport: %w", err)
	}

	tlsConfig, err := core.GenerateTLSConfig(cert)
	if err != nil {
		return nil, fmt.Errorf("generate tls config: %w", err)
	}
	if err := manager.RegisterTransport(transport.NewTLSTransport(tlsConfig.Clone())); err != nil {
		return nil, fmt.Errorf("register tls transport: %w", err)
	}
	return manager, nil
}

func newVTun(name string, localIP net.IP) (*vtun.VTun, error) {
	addr, ok := netip.AddrFromSlice(localIP)
	if !ok {
		return nil, fmt.Errorf("invalid core address %q", localIP.String())
	}
	vt, err := (&vtun.Opts{
		Name:           name,
		LocalAddrs:     []netip.Addr{addr},
		NoLoopbackAddr: true,
		NetStackOpts: &helpers.Opts{
			MTU: 1500,
		},
	}).Build()
	if err != nil {
		return nil, fmt.Errorf("build vtun: %w", err)
	}
	return vt, nil
}

func runTCPDemo(clientVT, serverVT *vtun.VTun, serverIP net.IP) (string, error) {
	listener, err := serverVT.Listen(context.Background(), "tcp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		return "", fmt.Errorf("listen tcp over vtun: %w", err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			serverErr <- err
			return
		}
		_, err = conn.Write([]byte("tcp:" + string(buf[:n])))
		serverErr <- err
	}()

	conn, err := clientVT.Dial(context.Background(), "tcp6", listener.Addr().String())
	if err != nil {
		return "", fmt.Errorf("dial tcp over vtun: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	if _, err := conn.Write([]byte("ping")); err != nil {
		return "", err
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	if err := <-serverErr; err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func runUDPDemo(clientVT, serverVT *vtun.VTun, serverIP net.IP) (string, error) {
	packetConn, err := serverVT.ListenPacket(context.Background(), "udp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		return "", fmt.Errorf("listen udp over vtun: %w", err)
	}
	defer packetConn.Close()

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, addr, err := packetConn.ReadFrom(buf)
		if err != nil {
			serverErr <- err
			return
		}
		_, err = packetConn.WriteTo([]byte("udp:"+string(buf[:n])), addr)
		serverErr <- err
	}()

	conn, err := clientVT.Dial(context.Background(), "udp6", packetConn.LocalAddr().String())
	if err != nil {
		return "", fmt.Errorf("dial udp over vtun: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	if _, err := conn.Write([]byte("ping")); err != nil {
		return "", err
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	if err := <-serverErr; err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func runHTTPDemo(clientVT, serverVT *vtun.VTun, serverIP net.IP) (string, error) {
	listener, err := serverVT.Listen(context.Background(), "tcp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		return "", fmt.Errorf("listen http over vtun: %w", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "http:pong")
		}),
		ReadHeaderTimeout: requestTimeout,
	}
	serverErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", err
	}
	target := fmt.Sprintf("http://[%s]:%s", serverIP.String(), port)
	client := http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       clientVT.Dial,
		},
		Timeout: requestTimeout,
	}
	resp, err := client.Get(target)
	if err != nil {
		return "", fmt.Errorf("get over vtun: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	select {
	case err := <-serverErr:
		return "", err
	default:
	}
	return string(body), nil
}

func configurePublicAutopeering(coreNode *core.Core, network transport.Network) *autopeer.Manager {
	fetcher := autopeer.NewFetcher(ygglogger.Discard(), time.Hour)
	fetcher.SetDefaultNetwork(network)
	fetcher.SetSources([]string{autopeer.BuiltinSource})

	manager := autopeer.NewManager(fetcher)
	manager.SetPeerManager(coreNode)
	manager.SetConfig(autopeer.ManagerConfig{
		CheckInterval:    time.Minute,
		MinimumConnected: 2,
		Countries:        []string{"germany", "france", "netherlands"},
		TransportSchemes: []string{"tcp", "tls"},
	})
	return manager
}

func startLinkLocalAutopeering(
	coreNode *core.Core,
	network transport.Network,
	ifacePattern string,
) (*multicast.Multicast, error) {
	if network == nil || !network.IsNative() {
		return nil, fmt.Errorf("link-local autopeering requires a native carrier network")
	}
	return multicast.New(
		multicastCoreAdapter{core: coreNode},
		ygglogger.Discard(),
		multicast.ProtocolVersion{
			Major: core.ProtocolVersionMajor,
			Minor: core.ProtocolVersionMinor,
		},
		multicast.MulticastInterface{
			Regex:  regexp.MustCompile(ifacePattern),
			Beacon: true,
			Listen: true,
			Port:   0,
		},
	)
}

type multicastCoreAdapter struct {
	core *core.Core
}

func (a multicastCoreAdapter) ListenLocal(u *url.URL, sintf string) (multicast.Listener, error) {
	return a.core.ListenLocal(u, sintf)
}

func (a multicastCoreAdapter) CallPeer(u *url.URL, sintf string) error {
	return a.core.CallPeer(u, sintf)
}

func (a multicastCoreAdapter) PublicKey() ed25519.PublicKey {
	return a.core.PublicKey()
}

func peerIsUp(peers []core.PeerInfo) bool {
	for _, peer := range peers {
		if peer.Up {
			return true
		}
	}
	return false
}

func waitFor(ctx context.Context, ready func() bool) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func rewriteScheme(u *url.URL, scheme string) *url.URL {
	clone := *u
	clone.Scheme = scheme
	return &clone
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

var _ transport.Transport = (*meteredTransport)(nil)
var _ gonnect.Network = (*loopback.LoopbackNetwork)(nil)
