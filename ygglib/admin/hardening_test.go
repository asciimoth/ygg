package admin

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/asciimoth/ygg/ygglib/core"
)

type timeoutDialError struct{}

func (timeoutDialError) Error() string   { return "timeout" }
func (timeoutDialError) Timeout() bool   { return true }
func (timeoutDialError) Temporary() bool { return true }

func TestUnixSocketInUseClassifiesDialResults(t *testing.T) {
	tests := []struct {
		name string
		err  error
		conn bool
		want bool
	}{
		{name: "successful connection", conn: true, want: true},
		{name: "timeout", err: timeoutDialError{}, want: true},
		{name: "wrapped timeout", err: fmt.Errorf("dial: %w", timeoutDialError{}), want: true},
		{name: "plain dial error", err: errors.New("dial failed"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unixSocketInUse("unused", func(network, address string, timeout time.Duration) (net.Conn, error) {
				if network != "unix" || address != "unused" || timeout != 2*time.Second {
					t.Fatalf("unexpected dial arguments: %q %q %s", network, address, timeout)
				}
				if !tt.conn {
					return nil, tt.err
				}
				server, client := net.Pipe()
				_ = server.Close()
				return client, nil
			})
			if got != tt.want {
				t.Fatalf("unixSocketInUse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdminSocketHandlesEmptyUnixPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UNIX sockets are not available on Windows")
	}
	a := &AdminSocket{log: testLogger{}}
	a.config.listenaddr = "unix://"
	if err := a.startSocket(); err != nil {
		// Some platforms reject an unnamed UNIX listener. Returning that error is
		// also safe and satisfies the startup contract.
		return
	}
	if a.listener == nil {
		t.Fatal("startSocket() listener = nil")
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestAdminSocketReplacesStaleUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UNIX sockets are not available on Windows")
	}
	path := filepath.Join(t.TempDir(), "admin.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale socket fixture: %v", err)
	}

	a := &AdminSocket{log: testLogger{}}
	a.config.listenaddr = ListenAddress("unix://" + path)
	if err := a.startSocket(); err != nil {
		t.Fatalf("startSocket() error = %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replacement socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("replacement mode = %v, want socket", info.Mode())
	}
	if info.Mode().Perm() != 0o660 {
		t.Fatalf("replacement permissions = %o, want 660", info.Mode().Perm())
	}
}

func TestAdminSocketPreservesActiveUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UNIX sockets are not available on Windows")
	}
	path := filepath.Join(t.TempDir(), "admin.sock")
	active, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on fixture socket: %v", err)
	}
	t.Cleanup(func() { _ = active.Close() })

	a := &AdminSocket{log: testLogger{}}
	a.config.listenaddr = ListenAddress("unix://" + path)
	err = a.startSocket()
	if err == nil {
		_ = a.Stop()
		t.Fatal("startSocket() error = nil, want active socket error")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("active socket was removed: %v", statErr)
	}
}

func testPublicKey(fill byte) ed25519.PublicKey {
	key := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for i := range key {
		key[i] = fill
	}
	return key
}

func TestAdminResultConversionsSkipMalformedKeys(t *testing.T) {
	validLow := testPublicKey(1)
	validHigh := testPublicKey(2)
	invalid := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize-1))

	paths := pathEntries([]core.PathEntryInfo{
		{Key: validHigh, Sequence: 2},
		{Key: invalid, Sequence: 99},
		{Key: validLow, Sequence: 1},
	})
	if len(paths) != 2 || paths[0].Sequence != 1 || paths[1].Sequence != 2 {
		t.Fatalf("pathEntries() = %#v, want two sorted valid entries", paths)
	}

	sessions := sessionEntries([]core.SessionInfo{
		{Key: invalid, RXBytes: 99},
		{Key: validLow, RXBytes: 1},
	})
	if len(sessions) != 1 || sessions[0].RXBytes != 1 {
		t.Fatalf("sessionEntries() = %#v, want one valid entry", sessions)
	}

	tree := treeEntries([]core.TreeEntryInfo{
		{Key: invalid, Sequence: 99},
		{Key: validLow, Parent: validHigh, Sequence: 1},
	})
	if len(tree) != 1 || tree[0].Sequence != 1 {
		t.Fatalf("treeEntries() = %#v, want one valid entry", tree)
	}
}
