package linktransport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/asciimoth/ygg/ygglib/transport"
)

type QUICTransport struct {
	tlsConfig  *tls.Config
	quicConfig *quic.Config
}

func NewQUICTransport(config *tls.Config) *QUICTransport {
	return &QUICTransport{
		tlsConfig: config,
		quicConfig: &quic.Config{
			MaxIdleTimeout:  time.Minute,
			KeepAlivePeriod: 20 * time.Second,
			TokenStore:      quic.NewLRUTokenStore(255, 255),
		},
	}
}

func (t *QUICTransport) Schemes() []string {
	return []string{"quic"}
}

func (t *QUICTransport) Dial(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	_ transport.Options,
) (transport.Conn, error) {
	remoteAddr, err := net.ResolveUDPAddr("udp", u.Host)
	if err != nil {
		return nil, err
	}
	localAddr := "0.0.0.0:0"
	if remoteAddr.IP != nil && remoteAddr.IP.To4() == nil {
		localAddr = "[::]:0"
	}
	packetConn, err := network.ListenPacket(ctx, "udp", localAddr)
	if err != nil {
		return nil, err
	}
	tlsConfig := t.cloneTLSConfig(u)
	qc, err := quic.Dial(ctx, packetConn, remoteAddr, tlsConfig, t.quicConfig)
	if err != nil {
		_ = packetConn.Close()
		return nil, err
	}
	qs, err := qc.OpenStreamSync(ctx)
	if err != nil {
		_ = qc.CloseWithError(1, fmt.Sprintf("stream error: %s", err))
		return nil, err
	}
	return &quicStreamConn{
		conn:   qc,
		stream: qs,
		local:  packetConn.LocalAddr(),
		remote: remoteAddr,
	}, nil
}

func (t *QUICTransport) Listen(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	_ transport.Options,
) (transport.Listener, error) {
	packetConn, err := network.ListenPacket(ctx, "udp", u.Host)
	if err != nil {
		return nil, err
	}
	qt := &quic.Transport{Conn: packetConn}
	ql, err := qt.Listen(t.cloneTLSConfig(u), t.quicConfig)
	if err != nil {
		_ = packetConn.Close()
		return nil, err
	}

	l := &quicListener{
		listener:   ql,
		transport:  qt,
		packetConn: packetConn,
		ch:         make(chan net.Conn),
		ctx:        ctx,
	}
	go l.acceptLoop()
	return l, nil
}

func (t *QUICTransport) cloneTLSConfig(u *url.URL) *tls.Config {
	cfg := &tls.Config{}
	if t.tlsConfig != nil {
		cfg = t.tlsConfig.Clone()
	}
	if sni := u.Query().Get("sni"); sni != "" && net.ParseIP(sni) == nil {
		cfg.ServerName = sni
	} else if cfg.ServerName == "" {
		host := u.Hostname()
		if host != "" && net.ParseIP(host) == nil {
			cfg.ServerName = host
		}
	}
	cfg.MinVersion = tls.VersionTLS12
	cfg.MaxVersion = tls.VersionTLS13
	return cfg
}

type quicStreamConn struct {
	conn   *quic.Conn
	stream *quic.Stream
	local  net.Addr
	remote net.Addr
}

func (c *quicStreamConn) Read(p []byte) (int, error) {
	return c.stream.Read(p)
}

func (c *quicStreamConn) Write(p []byte) (int, error) {
	return c.stream.Write(p)
}

func (c *quicStreamConn) Close() error {
	err := c.stream.Close()
	if closeErr := c.conn.CloseWithError(0, ""); err == nil {
		err = closeErr
	}
	return err
}

func (c *quicStreamConn) LocalAddr() net.Addr {
	if c.local != nil {
		return c.local
	}
	return c.conn.LocalAddr()
}

func (c *quicStreamConn) RemoteAddr() net.Addr {
	if c.remote != nil {
		return c.remote
	}
	return c.conn.RemoteAddr()
}

func (c *quicStreamConn) SetDeadline(t time.Time) error {
	return c.stream.SetDeadline(t)
}

func (c *quicStreamConn) SetReadDeadline(t time.Time) error {
	return c.stream.SetReadDeadline(t)
}

func (c *quicStreamConn) SetWriteDeadline(t time.Time) error {
	return c.stream.SetWriteDeadline(t)
}

type quicListener struct {
	listener   *quic.Listener
	transport  *quic.Transport
	packetConn net.PacketConn
	ch         chan net.Conn
	ctx        context.Context
}

func (l *quicListener) Accept() (net.Conn, error) {
	conn, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *quicListener) Close() error {
	err := l.listener.Close()
	if closeErr := l.transport.Close(); err == nil {
		err = closeErr
	}
	if closeErr := l.packetConn.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (l *quicListener) Addr() net.Addr {
	return l.listener.Addr()
}

func (l *quicListener) acceptLoop() {
	defer close(l.ch)
	for {
		qc, err := l.listener.Accept(l.ctx)
		switch {
		case err == nil:
		case l.ctx.Err() != nil:
			return
		case err == quic.ErrServerClosed:
			return
		default:
			continue
		}

		qs, err := qc.AcceptStream(l.ctx)
		if err != nil {
			_ = qc.CloseWithError(1, fmt.Sprintf("stream error: %s", err))
			continue
		}
		streamConn := &quicStreamConn{
			conn:   qc,
			stream: qs,
			local:  qc.LocalAddr(),
			remote: qc.RemoteAddr(),
		}
		select {
		case l.ch <- streamConn:
		case <-l.ctx.Done():
			_ = streamConn.Close()
			return
		}
	}
}

var _ transport.Transport = (*QUICTransport)(nil)
