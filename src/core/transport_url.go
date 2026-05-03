package core

import (
	"net/url"
	"strings"
)

// normalizeTransportURL rewrites core-facing transport aliases to the transport
// scheme the manager should actually dispatch to.
func normalizeTransportURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	if u.Scheme != "socks" {
		return u
	}

	target := strings.TrimPrefix(u.Path, "/")
	if target == "" {
		return u
	}

	nu := *u
	nu.Scheme = "tcp"
	nu.Host = target
	nu.Path = ""
	nu.RawPath = ""
	return &nu
}
