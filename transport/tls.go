package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
)

type TLSTransport struct {
	Config *tls.Config
}

func NewTLSTransport(config *tls.Config) *TLSTransport {
	return &TLSTransport{Config: config}
}

func (t *TLSTransport) Schemes() []string {
	return []string{"tls"}
}

func (t *TLSTransport) Dial(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (Conn, error) {
	conn, err := tcpDial(ctx, network, u, opts)
	if err != nil {
		return nil, err
	}

	cfg := &tls.Config{}
	if t.Config != nil {
		cfg = t.Config.Clone()
	}
	if sni := u.Query().Get("sni"); sni != "" && net.ParseIP(sni) == nil {
		cfg.ServerName = sni
	} else if cfg.ServerName == "" {
		host := u.Hostname()
		if host != "" && net.ParseIP(host) == nil {
			cfg.ServerName = host
		}
	}

	client := tls.Client(conn, cfg)
	if err := client.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func (t *TLSTransport) Listen(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (Listener, error) {
	listener, err := tcpListen(ctx, network, u, opts)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{}
	if t.Config != nil {
		cfg = t.Config.Clone()
	}
	return tls.NewListener(listener, cfg), nil
}

var _ Transport = (*TLSTransport)(nil)
