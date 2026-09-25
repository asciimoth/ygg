//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package main

import "errors"

// chuser reports that process identity changes are unavailable. adminAddress
// is accepted to keep the daemon call site the same on every platform.
func chuser(user, adminAddress string) error {
	return errors.New("setting uid/gid is not supported on this platform")
}
