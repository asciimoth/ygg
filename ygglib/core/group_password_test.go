package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	iwt "github.com/Arceliar/ironwood/types"
	"github.com/asciimoth/ygg/ygglib/config"
)

const groupPasswordTestTimeout = 5 * time.Second

// newGroupPasswordTestCore creates a core without transport peerings. Tests
// attach in-memory streams directly to Ironwood so they can construct exact
// routing topologies without depending on operating-system sockets.
func newGroupPasswordTestCore(t *testing.T, password string) *Core {
	t.Helper()

	cfg := config.GenerateConfig()
	options := []SetupOption{
		TransportManager{Manager: newCoreTransportManager(t, cfg.Certificate)},
		// Include the option for an empty value too. This verifies that an
		// explicitly empty password keeps public-overlay behavior.
		GroupPassword(password),
	}
	node, err := New(cfg.Certificate, nil, options...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(node.Stop)
	return node
}

// connectGroupPasswordTestCores attaches a direct routing link. The returned
// channel reports both HandleConn results after the nodes are stopped.
func connectGroupPasswordTestCores(t *testing.T, left, right *Core) <-chan error {
	t.Helper()

	leftConn, rightConn := net.Pipe()
	done := make(chan error, 2)
	go func() {
		done <- left.PacketConn.HandleConn(right.PublicKey(), leftConn, 0)
	}()
	go func() {
		done <- right.PacketConn.HandleConn(left.PublicKey(), rightConn, 0)
	}()

	waitForGroupPasswordRoutes(t, left, right)
	return done
}

func waitForGroupPasswordRoutes(t *testing.T, nodes ...*Core) {
	t.Helper()

	deadline := time.Now().Add(groupPasswordTestTimeout)
	for {
		ready := true
		for _, node := range nodes {
			if len(node.GetTree()) < 2 {
				ready = false
				break
			}
		}
		if ready {
			// Tree entries appear before path announcements have fully settled.
			// Match the existing topology tests and allow that asynchronous work
			// to finish before starting an encrypted session.
			time.Sleep(2 * time.Second)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Ironwood routes")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type groupPasswordReadResult struct {
	data []byte
	from net.Addr
	err  error
}

// readGroupPasswordPacket starts the encrypted receive loop. Calling ReadFrom
// on both session endpoints is required because it also processes handshake
// messages and acknowledgements inside Ironwood.
func readGroupPasswordPacket(pc net.PacketConn, timeout time.Duration) <-chan groupPasswordReadResult {
	result := make(chan groupPasswordReadResult, 1)
	go func() {
		buffer := make([]byte, 1024)
		if err := pc.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			result <- groupPasswordReadResult{err: err}
			return
		}
		n, from, err := pc.ReadFrom(buffer)
		result <- groupPasswordReadResult{data: append([]byte(nil), buffer[:n]...), from: from, err: err}
	}()
	return result
}

func requireGroupPasswordDelivery(t *testing.T, sender, receiver *Core) {
	t.Helper()

	// The sender read keeps its half of session negotiation active. It normally
	// remains blocked until test cleanup closes the core.
	_ = readGroupPasswordPacket(sender.PacketConn, groupPasswordTestTimeout)
	received := readGroupPasswordPacket(receiver.PacketConn, groupPasswordTestTimeout)
	payload := []byte("authenticated group traffic")
	if n, err := sender.PacketConn.WriteTo(payload, receiver.LocalAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	} else if n != len(payload) {
		t.Fatalf("WriteTo wrote %d bytes, want %d", n, len(payload))
	}

	result := <-received
	if result.err != nil {
		t.Fatalf("ReadFrom: %v", result.err)
	}
	if !bytes.Equal(result.data, payload) {
		t.Fatalf("payload = %q, want %q", result.data, payload)
	}
	if result.from.String() != sender.LocalAddr().String() {
		t.Fatalf("sender = %v, want %v", result.from, sender.LocalAddr())
	}
}

func TestGroupPasswordDirectAuthentication(t *testing.T) {
	tests := []struct {
		name          string
		leftPassword  string
		rightPassword string
		wantDelivery  bool
	}{
		{name: "both empty", wantDelivery: true},
		{name: "same password", leftPassword: "shared high entropy test password", rightPassword: "shared high entropy test password", wantDelivery: true},
		{name: "different passwords", leftPassword: "first password", rightPassword: "second password"},
		{name: "protected to unprotected", leftPassword: "private group"},
		{name: "unprotected to protected", rightPassword: "private group"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := newGroupPasswordTestCore(t, tt.leftPassword)
			right := newGroupPasswordTestCore(t, tt.rightPassword)
			connectGroupPasswordTestCores(t, left, right)

			if tt.wantDelivery {
				requireGroupPasswordDelivery(t, left, right)
				return
			}

			leftRead := readGroupPasswordPacket(left.PacketConn, 500*time.Millisecond)
			rightRead := readGroupPasswordPacket(right.PacketConn, 500*time.Millisecond)
			payload := []byte("must not pass authentication")
			for i := 0; i < 1024; i++ {
				if _, err := left.PacketConn.WriteTo(payload, right.LocalAddr()); err != nil {
					t.Fatalf("failed-authentication WriteTo %d: %v", i, err)
				}
			}

			for side, resultChannel := range map[string]<-chan groupPasswordReadResult{
				"left": leftRead, "right": rightRead,
			} {
				result := <-resultChannel
				if result.err == nil {
					t.Fatalf("%s received unauthenticated payload %q", side, result.data)
				}
				if !errors.Is(result.err, iwt.ErrTimeout) {
					t.Fatalf("%s ReadFrom error = %v, want timeout", side, result.err)
				}
			}
			if sessions := left.Debug.GetSessions(); len(sessions) != 0 {
				t.Fatalf("failed authentication created left sessions: %#v", sessions)
			}
			if sessions := right.Debug.GetSessions(); len(sessions) != 0 {
				t.Fatalf("failed authentication created right sessions: %#v", sessions)
			}
		})
	}
}

func TestGroupPasswordTrafficThroughNonMemberRouter(t *testing.T) {
	left := newGroupPasswordTestCore(t, "member group password")
	router := newGroupPasswordTestCore(t, "not the member password")
	right := newGroupPasswordTestCore(t, "member group password")

	connectGroupPasswordTestCores(t, left, router)
	connectGroupPasswordTestCores(t, router, right)
	waitForGroupPasswordRoutes(t, left, router, right)

	requireGroupPasswordDelivery(t, left, right)
	if sessions := router.Debug.GetSessions(); len(sessions) != 0 {
		t.Fatalf("transit router created an end-to-end session: %#v", sessions)
	}
}

func TestGroupPasswordFailedAuthenticationCleansUpLinkGoroutines(t *testing.T) {
	left := newGroupPasswordTestCore(t, "left group")
	right := newGroupPasswordTestCore(t, "right group")
	done := connectGroupPasswordTestCores(t, left, right)

	_ = readGroupPasswordPacket(left.PacketConn, 250*time.Millisecond)
	_ = readGroupPasswordPacket(right.PacketConn, 250*time.Millisecond)
	for i := 0; i < 256; i++ {
		payload := []byte(fmt.Sprintf("rejected-%d", i))
		if _, err := left.PacketConn.WriteTo(payload, right.LocalAddr()); err != nil {
			t.Fatalf("WriteTo %d: %v", i, err)
		}
	}

	left.Stop()
	right.Stop()
	deadline := time.After(groupPasswordTestTimeout)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, io.EOF) {
				t.Fatalf("HandleConn shutdown error: %v", err)
			}
		case <-deadline:
			t.Fatal("HandleConn goroutine did not stop after failed authentication")
		}
	}
}
