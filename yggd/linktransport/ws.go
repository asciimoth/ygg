package linktransport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/asciimoth/ygg/ygglib/transport"
)

const websocketSubprotocol = "ygg-ws"

type WebSocketTransport struct{}

func NewWebSocketTransport() *WebSocketTransport {
	return &WebSocketTransport{}
}

func (t *WebSocketTransport) Schemes() []string {
	return []string{"ws"}
}

func (t *WebSocketTransport) Dial(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Conn, error) {
	return websocketDial(ctx, network, u, opts, nil)
}

func (t *WebSocketTransport) Listen(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Listener, error) {
	return websocketListen(ctx, network, u, opts)
}

type SecureWebSocketTransport struct {
	Config *tls.Config
}

func NewSecureWebSocketTransport(config *tls.Config) *SecureWebSocketTransport {
	return &SecureWebSocketTransport{Config: config}
}

func (t *SecureWebSocketTransport) Schemes() []string {
	return []string{"wss"}
}

func (t *SecureWebSocketTransport) Dial(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	opts transport.Options,
) (transport.Conn, error) {
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
	cfg.MinVersion = tls.VersionTLS12
	cfg.MaxVersion = tls.VersionTLS13
	return websocketDial(ctx, network, u, opts, cfg)
}

func (t *SecureWebSocketTransport) Listen(
	context.Context,
	transport.Network,
	*url.URL,
	transport.Options,
) (transport.Listener, error) {
	return nil, fmt.Errorf("wss listener not supported, use ws listener behind a reverse proxy instead")
}

type websocketListener struct {
	ch         chan net.Conn
	ctx        context.Context
	httpServer *http.Server
	listener   net.Listener
}

func (l *websocketListener) Accept() (net.Conn, error) {
	conn, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *websocketListener) Addr() net.Addr {
	return l.listener.Addr()
}

func (l *websocketListener) Close() error {
	err := l.httpServer.Shutdown(l.ctx)
	if closeErr := l.listener.Close(); err == nil {
		err = closeErr
	}
	return err
}

type websocketServer struct {
	ch  chan net.Conn
	ctx context.Context
}

func (s *websocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{websocketSubprotocol},
	})
	if err != nil {
		return
	}
	if c.Subprotocol() != websocketSubprotocol {
		_ = c.Close(websocket.StatusPolicyViolation, "client must speak the ygg-ws subprotocol")
		return
	}

	select {
	case s.ch <- websocket.NetConn(s.ctx, c, websocket.MessageBinary):
	case <-s.ctx.Done():
		_ = c.Close(websocket.StatusGoingAway, "listener closing")
	}
}

func websocketDial(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	_ transport.Options,
	tlsConfig *tls.Config,
) (net.Conn, error) {
	httpTransport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: tlsConfig,
		DialContext: func(ctx context.Context, networkName, address string) (net.Conn, error) {
			return network.Dial(ctx, networkName, address)
		},
	}
	wsconn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: httpTransport},
		Subprotocols: []string{
			websocketSubprotocol,
		},
	})
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(ctx, wsconn, websocket.MessageBinary), nil
}

func websocketListen(
	ctx context.Context,
	network transport.Network,
	u *url.URL,
	_ transport.Options,
) (net.Listener, error) {
	nl, err := network.Listen(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}

	ch := make(chan net.Conn)
	httpServer := &http.Server{
		Handler: &websocketServer{
			ch:  ch,
			ctx: ctx,
		},
		BaseContext:  func(net.Listener) context.Context { return ctx },
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	l := &websocketListener{
		ch:         ch,
		ctx:        ctx,
		httpServer: httpServer,
		listener:   nl,
	}
	go func() {
		_ = httpServer.Serve(nl)
		close(ch)
	}()
	return l, nil
}

var _ transport.Transport = (*WebSocketTransport)(nil)
var _ transport.Transport = (*SecureWebSocketTransport)(nil)
