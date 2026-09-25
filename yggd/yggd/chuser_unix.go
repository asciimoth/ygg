//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"fmt"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// privilegeOperations contains the operating-system calls used to change the
// daemon identity. Keeping these calls together makes their security-sensitive
// order explicit and lets tests verify the order without changing the test
// process identity.
type privilegeOperations struct {
	chown     func(string, int, int) error
	setgroups func([]int) error
	setgid    func(int) error
	setuid    func(int) error
}

var systemPrivilegeOperations = privilegeOperations{
	chown:     os.Chown,
	setgroups: unix.Setgroups,
	setgid:    unix.Setgid,
	setuid:    unix.Setuid,
}

// chuser changes the process identity to input. If adminAddress identifies a
// filesystem-backed UNIX socket, chuser first gives that socket to the target
// user and group so they can connect after privileges are dropped.
func chuser(input, adminAddress string) error {
	return changeUser(input, adminAddress, systemPrivilegeOperations)
}

// changeUser implements chuser with injected operating-system operations.
// Socket ownership must change before setgroups, setgid, and setuid because
// those calls can remove the permission needed to call chown.
func changeUser(input, adminAddress string, operations privilegeOperations) error {
	givenUser, givenGroup, _ := strings.Cut(input, ":")
	if givenUser == "" {
		return fmt.Errorf("user is empty")
	}
	if strings.Contains(input, ":") && givenGroup == "" {
		return fmt.Errorf("group is empty")
	}

	var (
		err      error
		usr      *user.User
		grp      *user.Group
		uid, gid int
	)

	if usr, err = user.LookupId(givenUser); err != nil {
		if usr, err = user.Lookup(givenUser); err != nil {
			return err
		}
	}
	if uid, err = strconv.Atoi(usr.Uid); err != nil {
		return err
	}

	if givenGroup != "" {
		if grp, err = user.LookupGroupId(givenGroup); err != nil {
			if grp, err = user.LookupGroup(givenGroup); err != nil {
				return err
			}
		}

		gid, _ = strconv.Atoi(grp.Gid)
	} else {
		gid, _ = strconv.Atoi(usr.Gid)
	}

	if err := chownAdminSocket(adminAddress, uid, gid, operations.chown); err != nil {
		return err
	}
	if err := operations.setgroups([]int{gid}); err != nil {
		return fmt.Errorf("setgroups: %d: %v", gid, err)
	}
	if err := operations.setgid(gid); err != nil {
		return fmt.Errorf("setgid: %d: %v", gid, err)
	}
	if err := operations.setuid(uid); err != nil {
		return fmt.Errorf("setuid: %d: %v", uid, err)
	}

	return nil
}

// filesystemAdminSocketPath returns the path only when address identifies a
// UNIX socket that has a filesystem entry. Empty, disabled, malformed, TCP,
// and abstract UNIX addresses do not identify a file whose ownership can be
// changed.
func filesystemAdminSocketPath(address string) (string, bool) {
	if address == "" || strings.EqualFold(address, "none") {
		return "", false
	}

	u, err := url.Parse(address)
	if err != nil || !strings.EqualFold(u.Scheme, "unix") {
		return "", false
	}
	if u.Path == "" || strings.HasPrefix(u.Path, "@") {
		return "", false
	}

	return u.Path, true
}

// chownAdminSocket changes the owner of a filesystem-backed UNIX admin socket.
// The injected chown operation makes classification and failures testable
// without requiring elevated permissions.
func chownAdminSocket(address string, uid, gid int, chown func(string, int, int) error) error {
	path, ok := filesystemAdminSocketPath(address)
	if !ok {
		return nil
	}
	if err := chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown admin socket %q to %d:%d: %w", path, uid, gid, err)
	}
	return nil
}
