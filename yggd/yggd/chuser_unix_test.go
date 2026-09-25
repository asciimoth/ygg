//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestChuserRejectsInvalidUserSpecifications(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "empty group", input: "0:"},
		{name: "group only", input: ":0"},
		{name: "invalid username", input: "#user"},
		{name: "negative user ID", input: "-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := chuser(tt.input, ""); err == nil {
				t.Fatalf("chuser(%q, %q) succeeded; want an error", tt.input, "")
			}
		})
	}
}

func TestFilesystemAdminSocketPath(t *testing.T) {
	tests := []struct {
		name    string
		address string
		path    string
		ok      bool
	}{
		{name: "empty"},
		{name: "disabled", address: "none"},
		{name: "disabled mixed case", address: "NoNe"},
		{name: "TCP", address: "tcp://127.0.0.1:9001"},
		{name: "host and port", address: "127.0.0.1:9001"},
		{name: "malformed URL", address: "unix:///%zz"},
		{name: "empty UNIX path", address: "unix://"},
		{name: "abstract UNIX opaque address", address: "unix:@admin"},
		{name: "abstract UNIX host", address: "unix://@admin"},
		{name: "filesystem UNIX path", address: "unix:///run/yggdrasil.sock", path: "/run/yggdrasil.sock", ok: true},
		{name: "mixed-case UNIX scheme", address: "UnIx:///tmp/yggdrasil.sock", path: "/tmp/yggdrasil.sock", ok: true},
		{name: "at sign in filesystem name", address: "unix:///@admin", path: "/@admin", ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, ok := filesystemAdminSocketPath(tt.address)
			if path != tt.path || ok != tt.ok {
				t.Fatalf("filesystemAdminSocketPath(%q) = (%q, %t), want (%q, %t)", tt.address, path, ok, tt.path, tt.ok)
			}
		})
	}
}

func TestChownAdminSocketCallsChownForFilesystemSocket(t *testing.T) {
	const (
		uid = 123
		gid = 456
	)
	path := filepath.Join(t.TempDir(), "admin.sock")
	var called bool
	err := chownAdminSocket("unix://"+path, uid, gid, func(gotPath string, gotUID, gotGID int) error {
		called = true
		if gotPath != path || gotUID != uid || gotGID != gid {
			t.Fatalf("chown arguments = (%q, %d, %d), want (%q, %d, %d)", gotPath, gotUID, gotGID, path, uid, gid)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("chown was not called for a filesystem-backed UNIX socket")
	}
}

func TestChownAdminSocketSkipsNonFilesystemAddresses(t *testing.T) {
	addresses := []string{
		"",
		"none",
		"tcp://127.0.0.1:9001",
		"unix://",
		"unix:@admin",
		"unix://@admin",
	}
	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			err := chownAdminSocket(address, 123, 456, func(string, int, int) error {
				t.Fatal("chown was called for a non-filesystem admin address")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChownAdminSocketWrapsErrorWithContext(t *testing.T) {
	sentinel := errors.New("permission denied")
	path := filepath.Join(t.TempDir(), "admin.sock")
	err := chownAdminSocket("unix://"+path, 123, 456, func(string, int, int) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap %v", err, sentinel)
	}
	for _, want := range []string{path, "123:456"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestChangeUserChangesSocketOwnershipBeforeDroppingPrivileges(t *testing.T) {
	usr := currentUser(t)
	uid := parseID(t, "user", usr.Uid)
	gid := parseID(t, "group", usr.Gid)
	path := filepath.Join(t.TempDir(), "admin.sock")
	var calls []string
	operations := privilegeOperations{
		chown: func(gotPath string, gotUID, gotGID int) error {
			calls = append(calls, fmt.Sprintf("chown %s %d:%d", gotPath, gotUID, gotGID))
			return nil
		},
		setgroups: func(groups []int) error {
			calls = append(calls, fmt.Sprintf("setgroups %v", groups))
			return nil
		},
		setgid: func(gotGID int) error {
			calls = append(calls, fmt.Sprintf("setgid %d", gotGID))
			return nil
		},
		setuid: func(gotUID int) error {
			calls = append(calls, fmt.Sprintf("setuid %d", gotUID))
			return nil
		},
	}

	if err := changeUser(usr.Uid+":"+usr.Gid, "unix://"+path, operations); err != nil {
		t.Fatal(err)
	}
	want := []string{
		fmt.Sprintf("chown %s %d:%d", path, uid, gid),
		fmt.Sprintf("setgroups [%d]", gid),
		fmt.Sprintf("setgid %d", gid),
		fmt.Sprintf("setuid %d", uid),
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("operation order:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestChangeUserStopsWhenSocketOwnershipChangeFails(t *testing.T) {
	usr := currentUser(t)
	sentinel := errors.New("chown failed")
	var privilegeDropCalled bool
	operations := privilegeOperations{
		chown: func(string, int, int) error { return sentinel },
		setgroups: func([]int) error {
			privilegeDropCalled = true
			return nil
		},
		setgid: func(int) error {
			privilegeDropCalled = true
			return nil
		},
		setuid: func(int) error {
			privilegeDropCalled = true
			return nil
		},
	}

	err := changeUser(usr.Uid, "unix:///tmp/admin.sock", operations)
	if !errors.Is(err, sentinel) {
		t.Fatalf("changeUser error = %v, want wrapped %v", err, sentinel)
	}
	if privilegeDropCalled {
		t.Fatal("privilege drop continued after the socket ownership change failed")
	}
}

func TestChangeUserSkipsSocketOwnershipForTCP(t *testing.T) {
	usr := currentUser(t)
	operations := noOpPrivilegeOperations()
	operations.chown = func(string, int, int) error {
		t.Fatal("chown was called for a TCP admin endpoint")
		return nil
	}
	if err := changeUser(usr.Uid, "tcp://127.0.0.1:9001", operations); err != nil {
		t.Fatal(err)
	}
}

func TestChangeUserReturnsPrivilegeOperationErrors(t *testing.T) {
	usr := currentUser(t)
	tests := []struct {
		name        string
		fail        string
		wantCalls   []string
		wantInError string
	}{
		{name: "setgroups", fail: "setgroups", wantCalls: []string{"setgroups"}, wantInError: "setgroups"},
		{name: "setgid", fail: "setgid", wantCalls: []string{"setgroups", "setgid"}, wantInError: "setgid"},
		{name: "setuid", fail: "setuid", wantCalls: []string{"setgroups", "setgid", "setuid"}, wantInError: "setuid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentinel := errors.New("operation failed")
			var calls []string
			operation := func(name string) error {
				calls = append(calls, name)
				if name == tt.fail {
					return sentinel
				}
				return nil
			}
			operations := privilegeOperations{
				chown: func(string, int, int) error {
					t.Fatal("chown was called without a filesystem UNIX address")
					return nil
				},
				setgroups: func([]int) error { return operation("setgroups") },
				setgid:    func(int) error { return operation("setgid") },
				setuid:    func(int) error { return operation("setuid") },
			}

			err := changeUser(usr.Uid, "", operations)
			if err == nil || !strings.Contains(err.Error(), tt.wantInError) {
				t.Fatalf("changeUser error = %v, want error containing %q", err, tt.wantInError)
			}
			if strings.Join(calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Fatalf("operations = %v, want %v", calls, tt.wantCalls)
			}
		})
	}
}

func TestChownAdminSocketWithPlatformOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("UNIX sockets are unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}

	err = chownAdminSocket("unix://"+path, os.Getuid(), os.Getgid(), os.Chown)
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("current user cannot change socket ownership: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("connect to owned mode-0660 admin socket: %v", err)
	}
	_ = conn.Close()
}

func currentUser(t *testing.T) *user.User {
	t.Helper()
	usr, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return usr
}

func parseID(t *testing.T, kind, value string) int {
	t.Helper()
	id, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("parse current %s ID %q: %v", kind, value, err)
	}
	return id
}

func noOpPrivilegeOperations() privilegeOperations {
	return privilegeOperations{
		chown:     func(string, int, int) error { return nil },
		setgroups: func([]int) error { return nil },
		setgid:    func(int) error { return nil },
		setuid:    func(int) error { return nil },
	}
}
