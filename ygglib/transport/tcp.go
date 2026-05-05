package transport

import (
	"context"
	"net/url"
)

type TCPTransport struct{}

func NewTCPTransport() *TCPTransport {
	return &TCPTransport{}
}

func (t *TCPTransport) Schemes() []string {
	return []string{"tcp"}
}

func (t *TCPTransport) Dial(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (Conn, error) {
	return tcpDial(ctx, network, u, opts)
}

func (t *TCPTransport) Listen(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (Listener, error) {
	return tcpListen(ctx, network, u, opts)
}

var _ Transport = (*TCPTransport)(nil)
