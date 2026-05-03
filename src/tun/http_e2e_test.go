package tun

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/native"

	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/src/core"
	"github.com/asciimoth/ygg/src/ipv6rwc"
	"github.com/asciimoth/ygg/transport"
)

type httpTestNode struct {
	core    *core.Core
	rwc     *ipv6rwc.ReadWriteCloser
	adapter *TunAdapter
	vt      *vtun.VTun
}

func TestVTunHTTPPairSwap(t *testing.T) {
	nodeA, nodeB := createConnectedHTTPTestCores(t)

	left := newHTTPTestNode(t, nodeA, "vtun-a-1", 1500, 0, 0)
	right := newHTTPTestNode(t, nodeB, "vtun-b-1", 1480, 4, 2)

	assertTunStatus(t, left.adapter, "vtun-a-1", 1500, 0, 0)
	assertTunStatus(t, right.adapter, "vtun-b-1", 1480, 2, 4)
	exerciseHTTPOverVTun(t, left.vt, right.vt, nodeA.Address(), "phase-1")

	nextLeft := buildTestVTun(t, nodeA.Address(), "vtun-a-2", 1360, 12, 8)
	nextRight := buildTestVTun(t, nodeB.Address(), "vtun-b-2", 1420, 6, 10)
	if err := left.adapter.Replace(nextLeft, AttachmentType("vtun")); err != nil {
		t.Fatalf("replace left vtun: %v", err)
	}
	if err := right.adapter.Replace(nextRight, AttachmentType("vtun")); err != nil {
		t.Fatalf("replace right vtun: %v", err)
	}
	left.vt = nextLeft
	right.vt = nextRight

	assertTunStatus(t, left.adapter, "vtun-a-2", 1360, 8, 12)
	assertTunStatus(t, right.adapter, "vtun-b-2", 1420, 10, 6)
	exerciseHTTPOverVTun(t, left.vt, right.vt, nodeA.Address(), "phase-2")
}

func TestVTunTCPPingPongSwap(t *testing.T) {
	nodeA, nodeB := createConnectedHTTPTestCores(t)

	left := newHTTPTestNode(t, nodeA, "vtun-a-1", 1500, 0, 0)
	right := newHTTPTestNode(t, nodeB, "vtun-b-1", 1480, 4, 2)

	assertTunStatus(t, left.adapter, "vtun-a-1", 1500, 0, 0)
	assertTunStatus(t, right.adapter, "vtun-b-1", 1480, 2, 4)
	exerciseTCPPingPongOverVTun(t, left.vt, right.vt, nodeA.Address(), "tcp-phase-1")

	nextLeft := buildTestVTun(t, nodeA.Address(), "vtun-a-2", 1360, 12, 8)
	nextRight := buildTestVTun(t, nodeB.Address(), "vtun-b-2", 1420, 6, 10)
	if err := left.adapter.Replace(nextLeft, AttachmentType("vtun")); err != nil {
		t.Fatalf("replace left vtun: %v", err)
	}
	if err := right.adapter.Replace(nextRight, AttachmentType("vtun")); err != nil {
		t.Fatalf("replace right vtun: %v", err)
	}
	left.vt = nextLeft
	right.vt = nextRight

	assertTunStatus(t, left.adapter, "vtun-a-2", 1360, 8, 12)
	assertTunStatus(t, right.adapter, "vtun-b-2", 1420, 10, 6)
	exerciseTCPPingPongOverVTun(t, left.vt, right.vt, nodeA.Address(), "tcp-phase-2")
}

func TestVTunUDPPingPongSwap(t *testing.T) {
	nodeA, nodeB := createConnectedHTTPTestCores(t)

	left := newHTTPTestNode(t, nodeA, "vtun-a-1", 1500, 0, 0)
	right := newHTTPTestNode(t, nodeB, "vtun-b-1", 1480, 4, 2)

	assertTunStatus(t, left.adapter, "vtun-a-1", 1500, 0, 0)
	assertTunStatus(t, right.adapter, "vtun-b-1", 1480, 2, 4)
	exerciseUDPPingPongOverVTun(t, left.vt, right.vt, nodeA.Address(), "udp-phase-1")

	nextLeft := buildTestVTun(t, nodeA.Address(), "vtun-a-2", 1360, 12, 8)
	nextRight := buildTestVTun(t, nodeB.Address(), "vtun-b-2", 1420, 6, 10)
	if err := left.adapter.Replace(nextLeft, AttachmentType("vtun")); err != nil {
		t.Fatalf("replace left vtun: %v", err)
	}
	if err := right.adapter.Replace(nextRight, AttachmentType("vtun")); err != nil {
		t.Fatalf("replace right vtun: %v", err)
	}
	left.vt = nextLeft
	right.vt = nextRight

	assertTunStatus(t, left.adapter, "vtun-a-2", 1360, 8, 12)
	assertTunStatus(t, right.adapter, "vtun-b-2", 1420, 10, 6)
	exerciseUDPPingPongOverVTun(t, left.vt, right.vt, nodeA.Address(), "udp-phase-2")
}

func createConnectedHTTPTestCores(t *testing.T) (*core.Core, *core.Core) {
	t.Helper()

	cfgA := config.GenerateConfig()
	cfgB := config.GenerateConfig()
	if err := cfgA.GenerateSelfSignedCertificate(); err != nil {
		t.Fatalf("generate cert A: %v", err)
	}
	if err := cfgB.GenerateSelfSignedCertificate(); err != nil {
		t.Fatalf("generate cert B: %v", err)
	}

	logger := testLogger{}
	nodeA, err := core.New(
		cfgA.Certificate,
		logger,
		core.TransportManager{Manager: newHTTPTestTransportManager(t, cfgA.Certificate)},
	)
	if err != nil {
		t.Fatalf("new core A: %v", err)
	}
	t.Cleanup(nodeA.Stop)

	nodeB, err := core.New(
		cfgB.Certificate,
		logger,
		core.TransportManager{Manager: newHTTPTestTransportManager(t, cfgB.Certificate)},
	)
	if err != nil {
		t.Fatalf("new core B: %v", err)
	}
	t.Cleanup(nodeB.Stop)

	listenURL, err := url.Parse("tcp://127.0.0.1:0")
	if err != nil {
		t.Fatalf("parse listen url: %v", err)
	}
	listener, err := nodeA.Listen(listenURL, "")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	peerURL, err := url.Parse("tcp://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("parse peer url: %v", err)
	}
	if err := nodeB.CallPeer(peerURL, ""); err != nil {
		t.Fatalf("call peer: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	return nodeA, nodeB
}

func newHTTPTestTransportManager(t *testing.T, cert *tls.Certificate) *transport.Manager {
	t.Helper()

	network := &native.Network{}
	if err := network.Up(); err != nil {
		t.Fatalf("network up: %v", err)
	}

	manager := transport.NewManager(network)
	if err := manager.RegisterTransport(transport.NewTCPTransport()); err != nil {
		t.Fatalf("register tcp transport: %v", err)
	}
	tlsConfig, err := core.GenerateTLSConfig(cert)
	if err != nil {
		t.Fatalf("generate tls config: %v", err)
	}
	if err := manager.RegisterTransport(transport.NewTLSTransport(tlsConfig.Clone())); err != nil {
		t.Fatalf("register tls transport: %v", err)
	}
	return manager
}

func newHTTPTestNode(t *testing.T, node *core.Core, name string, mtu, mwo, mro int) *httpTestNode {
	t.Helper()

	rwc := ipv6rwc.NewReadWriteCloser(node)
	adapter, err := New(rwc, testLogger{}, InterfaceName("none"), InterfaceMTU(uint64(mtu)))
	if err != nil {
		t.Fatalf("new tun adapter %s: %v", name, err)
	}
	vt := buildTestVTun(t, node.Address(), name, mtu, mwo, mro)
	if err := adapter.Attach(vt, AttachmentType("vtun")); err != nil {
		t.Fatalf("attach %s: %v", name, err)
	}

	httpNode := &httpTestNode{
		core:    node,
		rwc:     rwc,
		adapter: adapter,
		vt:      vt,
	}
	t.Cleanup(func() {
		_ = httpNode.adapter.Stop()
		_ = httpNode.rwc.Close()
	})
	return httpNode
}

func buildTestVTun(t *testing.T, localIP net.IP, name string, mtu, mwo, mro int) *vtun.VTun {
	t.Helper()

	addr, ok := netip.AddrFromSlice(localIP)
	if !ok {
		t.Fatalf("invalid local ip %q", localIP.String())
	}
	vt, err := (&vtun.Opts{
		Name:           name,
		LocalAddrs:     []netip.Addr{addr},
		NoLoopbackAddr: true,
		NetStackOpts: &helpers.Opts{
			MTU: mtu,
		},
		MWO: mwo,
		MRO: mro,
	}).Build()
	if err != nil {
		t.Fatalf("build vtun %s: %v", name, err)
	}
	return vt
}

func assertTunStatus(t *testing.T, adapter *TunAdapter, name string, mtu uint64, mro, mwo int) {
	t.Helper()

	waitFor(t, func() bool {
		status := adapter.Status()
		return status.Attached &&
			status.Enabled &&
			status.Type == "vtun" &&
			status.Name == name &&
			status.MTU == mtu &&
			status.MRO == mro &&
			status.MWO == mwo
	})
}

func exerciseHTTPOverVTun(t *testing.T, serverVT, clientVT *vtun.VTun, serverIP net.IP, wantBody string) {
	t.Helper()

	target, shutdown := startVTunHTTPServer(t, serverVT, serverIP, wantBody)
	defer shutdown()

	waitForHTTPResponse(t, clientVT, target, wantBody)
}

func exerciseTCPPingPongOverVTun(t *testing.T, serverVT, clientVT *vtun.VTun, serverIP net.IP, phase string) {
	t.Helper()

	address, shutdown := startVTunTCPServer(t, serverVT, serverIP)
	defer shutdown()

	runConcurrentPingPongClients(t, 8, func(worker int) error {
		conn, err := clientVT.Dial(context.Background(), "tcp6", address)
		if err != nil {
			return err
		}
		defer conn.Close()

		for round := 0; round < 64; round++ {
			_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
			payload := makePingPongPayload(phase, "tcp", worker, round, 1024)
			if err := writeFrame(conn, payload); err != nil {
				return err
			}
			echo, err := readFrame(conn)
			if err != nil {
				return err
			}
			if !bytes.Equal(echo, payload) {
				return fmt.Errorf("tcp echo mismatch worker=%d round=%d", worker, round)
			}
		}
		return nil
	})
}

func exerciseUDPPingPongOverVTun(t *testing.T, serverVT, clientVT *vtun.VTun, serverIP net.IP, phase string) {
	t.Helper()

	address, shutdown := startVTunUDPServer(t, serverVT, serverIP)
	defer shutdown()

	runConcurrentPingPongClients(t, 8, func(worker int) error {
		conn, err := clientVT.Dial(context.Background(), "udp6", address)
		if err != nil {
			return err
		}
		defer conn.Close()

		buf := make([]byte, 2048)
		for round := 0; round < 128; round++ {
			payload := makePingPongPayload(phase, "udp", worker, round, 1024)
			delivered := false
			for attempt := 0; attempt < 5; attempt++ {
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := conn.Write(payload); err != nil {
					return err
				}
				n, err := conn.Read(buf)
				if err != nil {
					if ne, ok := err.(net.Error); ok && ne.Timeout() {
						continue
					}
					return err
				}
				if !bytes.Equal(buf[:n], payload) {
					return fmt.Errorf("udp echo mismatch worker=%d round=%d", worker, round)
				}
				delivered = true
				break
			}
			if !delivered {
				return fmt.Errorf("udp echo timeout worker=%d round=%d", worker, round)
			}
		}
		return nil
	})
}

func startVTunHTTPServer(t *testing.T, serverVT *vtun.VTun, serverIP net.IP, responseBody string) (string, func()) {
	t.Helper()

	listener, err := serverVT.Listen(context.Background(), "tcp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		t.Fatalf("listen http on %s: %v", serverIP.String(), err)
	}

	serverErrCh := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, responseBody)
		}),
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr %q: %v", listener.Addr().String(), err)
	}
	target := fmt.Sprintf("http://[%s]:%s", serverIP.String(), port)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		select {
		case err := <-serverErrCh:
			t.Fatalf("http server error: %v", err)
		default:
		}
	}
	return target, shutdown
}

func startVTunTCPServer(t *testing.T, serverVT *vtun.VTun, serverIP net.IP) (string, func()) {
	t.Helper()

	listener, err := serverVT.Listen(context.Background(), "tcp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		t.Fatalf("listen tcp on %s: %v", serverIP.String(), err)
	}

	serverErrCh := make(chan error, 1)
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				select {
				case serverErrCh <- err:
				default:
				}
				return
			}
			wg.Add(1)
			go func(conn net.Conn) {
				defer wg.Done()
				defer conn.Close()
				for {
					_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
					payload, err := readFrame(conn)
					if err != nil {
						if err == io.EOF {
							return
						}
						if ne, ok := err.(net.Error); ok && ne.Timeout() {
							return
						}
						select {
						case serverErrCh <- err:
						default:
						}
						return
					}
					if err := writeFrame(conn, payload); err != nil {
						select {
						case serverErrCh <- err:
						default:
						}
						return
					}
				}
			}(conn)
		}
	}()

	shutdown := func() {
		_ = listener.Close()
		wg.Wait()
		select {
		case err := <-serverErrCh:
			if !isClosedNetworkError(err) {
				t.Fatalf("tcp server error: %v", err)
			}
		default:
		}
	}
	return listener.Addr().String(), shutdown
}

func startVTunUDPServer(t *testing.T, serverVT *vtun.VTun, serverIP net.IP) (string, func()) {
	t.Helper()

	conn, err := serverVT.ListenPacket(context.Background(), "udp6", net.JoinHostPort(serverIP.String(), "0"))
	if err != nil {
		t.Fatalf("listen udp on %s: %v", serverIP.String(), err)
	}

	serverErrCh := make(chan error, 1)
	stopCh := make(chan struct{})
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					select {
					case <-stopCh:
						return
					default:
						continue
					}
				}
				select {
				case serverErrCh <- err:
				default:
				}
				return
			}
			if _, err := conn.WriteTo(buf[:n], addr); err != nil {
				select {
				case serverErrCh <- err:
				default:
				}
				return
			}
		}
	}()

	shutdown := func() {
		close(stopCh)
		_ = conn.Close()
		select {
		case err := <-serverErrCh:
			if !isClosedNetworkError(err) {
				t.Fatalf("udp server error: %v", err)
			}
		default:
		}
	}
	return conn.LocalAddr().String(), shutdown
}

func waitForHTTPResponse(t *testing.T, clientVT *vtun.VTun, target, wantBody string) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := doVTunHTTPGet(clientVT, target)
		if err == nil && body == wantBody {
			return
		}
		if err == nil {
			lastErr = fmt.Errorf("unexpected body %q", body)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("http request to %s did not return %q: %v", target, wantBody, lastErr)
}

func doVTunHTTPGet(clientVT *vtun.VTun, target string) (string, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return clientVT.Dial(ctx, network, addr)
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
	}

	resp, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", strconv.Quote(resp.Status))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func runConcurrentPingPongClients(t *testing.T, workers int, fn func(worker int) error) {
	t.Helper()

	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			errCh <- fn(worker)
		}(worker)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func makePingPongPayload(phase, proto string, worker, round, size int) []byte {
	header := fmt.Sprintf("%s/%s/%02d/%03d/", phase, proto, worker, round)
	payload := make([]byte, size)
	copy(payload, []byte(header))
	for i := len(header); i < len(payload); i++ {
		payload[i] = byte('a' + (worker+round+i)%26)
	}
	return payload
}

func writeFrame(w io.Writer, payload []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func isClosedNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "endpoint is in invalid state")
}
