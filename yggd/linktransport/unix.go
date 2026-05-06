package linktransport

import (
	"context"
	"net"
	"net/url"
	"time"

	"github.com/asciimoth/ygg/ygglib/transport"
)

type UNIXTransport struct {
	dialer *net.Dialer
	listen *net.ListenConfig
}

func NewUNIXTransport() *UNIXTransport {
	return &UNIXTransport{
		dialer: &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: -1,
		},
		listen: &net.ListenConfig{
			KeepAlive: -1,
		},
	}
}

func (t *UNIXTransport) Schemes() []string {
	return []string{"unix"}
}

func (t *UNIXTransport) Dial(
	ctx context.Context,
	_ transport.Network,
	u *url.URL,
	_ transport.Options,
) (transport.Conn, error) {
	addr, err := net.ResolveUnixAddr("unix", u.Path)
	if err != nil {
		return nil, err
	}
	return t.dialer.DialContext(ctx, "unix", addr.String())
}

func (t *UNIXTransport) Listen(
	ctx context.Context,
	_ transport.Network,
	u *url.URL,
	_ transport.Options,
) (transport.Listener, error) {
	return t.listen.Listen(ctx, "unix", u.Path)
}

var _ transport.Transport = (*UNIXTransport)(nil)
