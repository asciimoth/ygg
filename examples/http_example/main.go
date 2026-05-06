// http_example shows the smallest complete in-process setup for this repo:
// two Yggdrasil cores, one VTun attached to each core, an HTTP server on one
// side, and an HTTP client on the other.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/loopback"
	"github.com/asciimoth/gonnect/native"

	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/ipv6rwc"
	ygglogger "github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/transport"
	yggtun "github.com/asciimoth/ygg/ygglib/tun"
)

const (
	serverResponse = "hello over yggdrasil vtun"
	httpTimeout    = 10 * time.Second
	readyTimeout   = 5 * time.Second
)

func main() {
	logger := log.New(os.Stdout, "http_example: ", 0)
	transportNetwork := flag.String("transport-network", "native", "transport network: native or loopback")
	flag.Parse()

	pair, err := newPair(*transportNetwork)
	if err != nil {
		logger.Fatal(err)
	}
	defer func() {
		if err := pair.Close(); err != nil {
			logger.Printf("cleanup error: %v", err)
		}
	}()

	logger.Printf("node A address: %s", pair.left.core.Address())
	logger.Printf("node B address: %s", pair.right.core.Address())
	logger.Printf("transport network: %s", pair.transportMode)

	target, shutdownServer, err := startHTTPServer(pair.left.vt, pair.left.core.Address())
	if err != nil {
		logger.Fatal(err)
	}
	defer shutdownServer()

	logger.Printf("server listening on %s", target)

	body, err := doHTTPRequest(pair.right.vt, target)
	if err != nil {
		logger.Fatal(err)
	}

	logger.Printf("client received %q", body)
}

type pair struct {
	left          *node
	right         *node
	transportMode string
}

type transportExampleNetwork interface {
	transport.Network
	gonnect.UpDown
}

func newPair(transportMode string) (*pair, error) {
	leftNetwork, rightNetwork, cleanupNetworks, err := newTransportNetworks(transportMode)
	if err != nil {
		return nil, err
	}

	left, err := newNode("node-a", leftNetwork, cleanupNetworks == nil)
	if err != nil {
		if cleanupNetworks != nil {
			_ = cleanupNetworks()
		}
		return nil, fmt.Errorf("create node A: %w", err)
	}

	right, err := newNode("node-b", rightNetwork, cleanupNetworks == nil)
	if err != nil {
		_ = left.Close()
		if cleanupNetworks != nil {
			_ = cleanupNetworks()
		}
		return nil, fmt.Errorf("create node B: %w", err)
	}

	pair := &pair{left: left, right: right, transportMode: transportMode}
	if cleanupNetworks != nil {
		pair.left.networkOwner = nil
		pair.right.networkOwner = nil
		pair.left.extraCleanup = cleanupNetworks
	}
	if err := pair.connect(); err != nil {
		_ = pair.Close()
		return nil, err
	}
	return pair, nil
}

func (p *pair) connect() error {
	listenURL, err := url.Parse("tcp://127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("parse listen url: %w", err)
	}

	listener, err := p.left.core.Listen(listenURL, "")
	if err != nil {
		return fmt.Errorf("listen on node A: %w", err)
	}

	peerURL, err := url.Parse("tcp://" + listener.Addr().String())
	if err != nil {
		return fmt.Errorf("build peer url: %w", err)
	}
	if err := p.right.core.CallPeer(peerURL, ""); err != nil {
		return fmt.Errorf("connect node B to node A: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()

	if err := waitFor(ctx, func() bool {
		return peerIsUp(p.left.core.GetPeers()) && peerIsUp(p.right.core.GetPeers())
	}); err != nil {
		return fmt.Errorf("wait for peering: %w", err)
	}
	return nil
}

func (p *pair) Close() error {
	var firstErr error
	for _, node := range []*node{p.left, p.right} {
		if node == nil {
			continue
		}
		if err := node.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type node struct {
	core         *core.Core
	networkOwner gonnect.UpDown
	extraCleanup func() error
	rwc          *ipv6rwc.ReadWriteCloser
	adapter      *yggtun.TunAdapter
	vt           *vtun.VTun
}

func newTransportNetworks(mode string) (transportExampleNetwork, transportExampleNetwork, func() error, error) {
	switch mode {
	case "native":
		left := &native.Network{}
		if err := left.Up(); err != nil {
			return nil, nil, nil, fmt.Errorf("bring left transport network up: %w", err)
		}
		right := &native.Network{}
		if err := right.Up(); err != nil {
			_ = left.Down()
			return nil, nil, nil, fmt.Errorf("bring right transport network up: %w", err)
		}
		return left, right, nil, nil
	case "loopback":
		shared := loopback.NewLoopbackNetwok()
		if err := shared.Up(); err != nil {
			return nil, nil, nil, fmt.Errorf("bring shared transport network up: %w", err)
		}
		return shared, shared, shared.Down, nil
	default:
		return nil, nil, nil, fmt.Errorf("unsupported transport network %q", mode)
	}
}

func newNode(name string, network transportExampleNetwork, ownsNetwork bool) (*node, error) {
	cfg := config.GenerateConfig()
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}

	manager, err := newTransportManager(network, cfg.Certificate)
	if err != nil {
		if ownsNetwork {
			_ = network.Down()
		}
		return nil, err
	}

	yggLogger := ygglogger.Discard()
	coreNode, err := core.New(
		cfg.Certificate,
		yggLogger,
		core.TransportManager{Manager: manager},
	)
	if err != nil {
		if ownsNetwork {
			_ = network.Down()
		}
		return nil, fmt.Errorf("create core: %w", err)
	}

	rwc := ipv6rwc.NewReadWriteCloser(coreNode)
	adapter, err := yggtun.New(rwc, yggLogger, yggtun.InterfaceMTU(1500))
	if err != nil {
		coreNode.Stop()
		_ = rwc.Close()
		if ownsNetwork {
			_ = network.Down()
		}
		return nil, fmt.Errorf("create tun adapter: %w", err)
	}

	vt, err := buildVTun(coreNode.Address(), name)
	if err != nil {
		_ = adapter.Stop()
		_ = rwc.Close()
		coreNode.Stop()
		if ownsNetwork {
			_ = network.Down()
		}
		return nil, err
	}

	if err := adapter.Attach(vt, yggtun.AttachmentType("vtun")); err != nil {
		_ = vt.Close()
		_ = adapter.Stop()
		_ = rwc.Close()
		coreNode.Stop()
		if ownsNetwork {
			_ = network.Down()
		}
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
		if ownsNetwork {
			_ = network.Down()
		}
		return nil, fmt.Errorf("wait for vtun attachment: %w", err)
	}

	var networkOwner gonnect.UpDown
	if ownsNetwork {
		networkOwner = network
	}
	return &node{
		core:         coreNode,
		networkOwner: networkOwner,
		rwc:          rwc,
		adapter:      adapter,
		vt:           vt,
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
	if n.networkOwner != nil {
		if err := n.networkOwner.Down(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if n.extraCleanup != nil {
		if err := n.extraCleanup(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func newTransportManager(network transport.Network, cert *tls.Certificate) (*transport.Manager, error) {
	manager := transport.NewManager(network)
	if err := manager.RegisterTransport(transport.NewTCPTransport()); err != nil {
		return nil, fmt.Errorf("register tcp transport: %w", err)
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

func buildVTun(localIP net.IP, name string) (*vtun.VTun, error) {
	addr, ok := netip.AddrFromSlice(localIP)
	if !ok {
		return nil, fmt.Errorf("invalid yggdrasil address %q", localIP.String())
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
		return nil, fmt.Errorf("build vtun %s: %w", name, err)
	}

	return vt, nil
}

func startHTTPServer(serverVT *vtun.VTun, serverIP net.IP) (target string, shutdown func(), err error) {
	listener, err := serverVT.Listen(context.Background(), "tcp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		return "", nil, fmt.Errorf("listen on server vtun: %w", err)
	}

	serverErrCh := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, serverResponse)
		}),
		ReadHeaderTimeout: httpTimeout,
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return "", nil, fmt.Errorf("read listener port: %w", err)
	}

	shutdown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		select {
		case err := <-serverErrCh:
			log.Printf("http server error: %v", err)
		default:
		}
	}

	return fmt.Sprintf("http://[%s]:%s", serverIP.String(), port), shutdown, nil
}

func doHTTPRequest(clientVT *vtun.VTun, target string) (string, error) {
	client := http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       clientVT.Dial,
		},
		Timeout: httpTimeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()

	var lastErr error
	var responseBody string
	if err := waitFor(ctx, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			lastErr = err
			return false
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			return false
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			return false
		}
		if string(body) != serverResponse {
			lastErr = fmt.Errorf("unexpected response body %q", string(body))
			return false
		}

		responseBody = string(body)
		lastErr = nil
		return true
	}); err != nil {
		return "", fmt.Errorf("perform http request: %w: %v", err, lastErr)
	}

	return responseBody, nil
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
