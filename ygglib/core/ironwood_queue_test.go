package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	iwn "github.com/Arceliar/ironwood/network"
	iwt "github.com/Arceliar/ironwood/types"
)

const queueTestTimeout = 5 * time.Second

// pausableConn lets a test stop one peer writer without stopping its reader.
// Ironwood owns the connection after HandleConn starts, so the test controls
// the write path through this wrapper instead of writing to the stream itself.
type pausableConn struct {
	net.Conn

	mu      sync.Mutex
	resume  chan struct{}
	written atomic.Uint64
}

func (c *pausableConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	resume := c.resume
	c.mu.Unlock()
	if resume != nil {
		<-resume
	}
	n, err := c.Conn.Write(p)
	c.written.Add(uint64(n))
	return n, err
}

func (c *pausableConn) pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resume != nil {
		panic("pausableConn is already paused")
	}
	c.resume = make(chan struct{})
}

func (c *pausableConn) unpause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resume == nil {
		return
	}
	close(c.resume)
	c.resume = nil
}

func (c *pausableConn) Close() error {
	c.unpause()
	return c.Conn.Close()
}

// newQueueTestPeers creates directly connected plaintext Ironwood peers. The
// core uses the encrypted wrapper, but queue limits are implemented by this
// underlying network PacketConn and use the same option value.
func newQueueTestPeers(t *testing.T) (*iwn.PacketConn, *iwn.PacketConn, *pausableConn) {
	t.Helper()

	_, secretA, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, secretB, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	options := []iwn.Option{
		iwn.WithPeerMaxMessageSize(ironwoodPeerMaxMessageSize),
		iwn.WithPeerKeepAliveDelay(time.Minute),
		iwn.WithPeerTimeout(time.Minute),
	}
	peerA, err := iwn.NewPacketConn(secretA, options...)
	if err != nil {
		t.Fatal(err)
	}
	peerB, err := iwn.NewPacketConn(secretB, options...)
	if err != nil {
		_ = peerA.Close()
		t.Fatal(err)
	}

	connA, connB := net.Pipe()
	controlledA := &pausableConn{Conn: connA}
	go func() { _ = peerA.HandleConn(secretB.Public().(ed25519.PublicKey), controlledA, 0) }()
	go func() { _ = peerB.HandleConn(secretA.Public().(ed25519.PublicKey), connB, 0) }()

	t.Cleanup(func() {
		controlledA.unpause()
		_ = peerA.Close()
		_ = peerB.Close()
	})
	waitForDirectTraffic(t, peerA, peerB)

	return peerA, peerB, controlledA
}

// waitForDirectTraffic waits for routing setup by exchanging application
// traffic. A peer can be registered before its route is ready, so peer-count
// snapshots are not a sufficient readiness check.
func waitForDirectTraffic(t *testing.T, sender *iwn.PacketConn, receiver *iwn.PacketConn) {
	t.Helper()
	const packetSize = 64
	packet := queueTestPacket(packetSize, 0)
	buffer := make([]byte, packetSize)
	if err := receiver.SetReadDeadline(time.Now().Add(queueTestTimeout)); err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		n   int
		err error
	}
	result := make(chan readResult, 1)
	go func() {
		n, _, err := receiver.ReadFrom(buffer)
		result <- readResult{n: n, err: err}
	}()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		writeQueueTestPacket(t, sender, receiver, packet)
		select {
		case received := <-result:
			if received.err != nil {
				t.Fatalf("readiness ReadFrom: %v", received.err)
			}
			if received.n != packetSize || binary.BigEndian.Uint64(buffer) != 0 {
				t.Fatalf("unexpected readiness packet: size %d, sequence %d", received.n, binary.BigEndian.Uint64(buffer))
			}
			if err := receiver.SetReadDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			return
		case <-ticker.C:
		}
	}
}

func waitForQueueTest(t *testing.T, name string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(queueTestTimeout)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", name)
		}
		runtime.Gosched()
	}
}

func queueTestPacket(size int, sequence uint64) []byte {
	packet := make([]byte, size)
	binary.BigEndian.PutUint64(packet, sequence)
	return packet
}

func writeQueueTestPacket(t *testing.T, sender *iwn.PacketConn, receiver *iwn.PacketConn, packet []byte) {
	t.Helper()
	n, err := sender.WriteTo(packet, receiver.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	if n != len(packet) {
		t.Fatalf("WriteTo wrote %d bytes, want %d", n, len(packet))
	}
}

// readThroughSequence reads queued packets through the requested marker. It
// returns the number of packets, including the marker. A deadline turns a lost
// marker or a stalled queue into a deterministic test failure.
func readThroughSequence(t *testing.T, receiver *iwn.PacketConn, packetSize int, marker uint64) int {
	t.Helper()
	if err := receiver.SetReadDeadline(time.Now().Add(queueTestTimeout)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := receiver.SetReadDeadline(time.Time{}); err != nil {
			t.Error(err)
		}
	}()

	buffer := make([]byte, packetSize)
	for count := 1; ; count++ {
		n, _, err := receiver.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("ReadFrom before marker %d: %v", marker, err)
		}
		if n != packetSize {
			t.Fatalf("ReadFrom returned %d bytes, want %d", n, packetSize)
		}
		if binary.BigEndian.Uint64(buffer) == marker {
			return count
		}
	}
}

func TestIronwoodPeerMaximumControlsMTU(t *testing.T) {
	_, secret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := iwn.NewPacketConn(secret, iwn.WithPeerMaxMessageSize(ironwoodPeerMaxMessageSize))
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()

	// A direct traffic message uses 77 bytes for the packet type, empty paths,
	// public keys, and the largest encoded watermark.
	const directTrafficOverhead = 77
	wantMTU := ironwoodPeerMaxMessageSize - directTrafficOverhead
	if got := packetConn.MTU(); got != wantMTU {
		t.Fatalf("MTU = %d, want %d for a %d-byte peer maximum", got, wantMTU, ironwoodPeerMaxMessageSize)
	}

	oversized := make([]byte, wantMTU+1)
	if _, err := packetConn.WriteTo(oversized, iwt.Addr(make([]byte, ed25519.PublicKeySize))); !errors.Is(err, iwt.ErrOversizedMessage) {
		t.Fatalf("oversized WriteTo error = %v, want %v", err, iwt.ErrOversizedMessage)
	}
}

func TestIronwoodSendQueueIsBoundedAndRecovers(t *testing.T) {
	peerA, peerB, connA := newQueueTestPeers(t)

	const (
		packetSize  = 4096
		packetCount = 256
		marker      = math.MaxUint64
		recovery    = marker - 1
	)
	connA.pause()
	for sequence := uint64(1); sequence <= packetCount; sequence++ {
		writeQueueTestPacket(t, peerA, peerB, queueTestPacket(packetSize, sequence))
	}
	writeQueueTestPacket(t, peerA, peerB, queueTestPacket(packetSize, marker))
	// Let the peer actor process all writes while its stream is blocked.
	time.Sleep(100 * time.Millisecond)
	connA.unpause()

	// One packet can be in the writer in addition to the bounded queue. Wire
	// headers make the real retained packet count smaller than this limit.
	maxPackets := int(ironwoodPeerMaxMessageSize)/packetSize + 2
	if count := readThroughSequence(t, peerB, packetSize, marker); count > maxPackets {
		t.Fatalf("received %d saturated send-queue packets, want at most %d", count, maxPackets)
	}

	writeQueueTestPacket(t, peerA, peerB, queueTestPacket(packetSize, recovery))
	if count := readThroughSequence(t, peerB, packetSize, recovery); count != 1 {
		t.Fatalf("recovery read returned %d packets, want 1", count)
	}
}

func TestIronwoodReceiveQueueIsBoundedAndRecovers(t *testing.T) {
	peerA, peerB, connA := newQueueTestPeers(t)

	const (
		packetSize  = 32 * 1024
		packetCount = 4
		marker      = math.MaxUint64
		recovery    = marker - 1
	)
	for sequence := uint64(1); sequence <= packetCount; sequence++ {
		written := connA.written.Load()
		writeQueueTestPacket(t, peerA, peerB, queueTestPacket(packetSize, sequence))
		// Wait for this packet to enter the remote receive path. This prevents
		// the local send queue from becoming the limit under test.
		waitForQueueTest(t, fmt.Sprintf("packet %d transmission", sequence), func() bool {
			return connA.written.Load() > written
		})
	}
	written := connA.written.Load()
	writeQueueTestPacket(t, peerA, peerB, queueTestPacket(packetSize, marker))
	waitForQueueTest(t, "marker transmission", func() bool {
		return connA.written.Load() > written
	})
	// Give the remote actor time to place the marker in its receive queue.
	time.Sleep(50 * time.Millisecond)

	maxPackets := int(ironwoodPeerMaxMessageSize)/packetSize + 1
	if count := readThroughSequence(t, peerB, packetSize, marker); count > maxPackets {
		t.Fatalf("received %d saturated receive-queue packets, want at most %d", count, maxPackets)
	}

	writeQueueTestPacket(t, peerA, peerB, queueTestPacket(packetSize, recovery))
	if count := readThroughSequence(t, peerB, packetSize, recovery); count != 1 {
		t.Fatalf("recovery read returned %d packets, want 1", count)
	}
}
