package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const websocketSubprotocol = "ygg-ws"

func main() {
	listen := flag.String("listen", "127.0.0.1:8000", "HTTP listen address")
	dir := flag.String("dir", ".", "static web directory")
	relayPath := flag.String("relay-path", "/ygg-peer", "WebSocket relay path")
	flag.Parse()

	mux := http.NewServeMux()
	mux.Handle(*relayPath, relayHandler{})
	mux.Handle("/", http.FileServer(http.Dir(*dir)))

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("serving %s on http://%s/ with relay %s", *dir, *listen, *relayPath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type relayHandler struct{}

func (relayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		http.Error(w, "target is required", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(target)
	if err != nil {
		http.Error(w, fmt.Sprintf("parse target: %v", err), http.StatusBadRequest)
		return
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		http.Error(w, "target must use ws or wss", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	browserWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{websocketSubprotocol},
	})
	if err != nil {
		return
	}
	defer browserWS.CloseNow()

	if browserWS.Subprotocol() != websocketSubprotocol {
		_ = browserWS.Close(websocket.StatusPolicyViolation, "client must speak the ygg-ws subprotocol")
		return
	}

	peerWS, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		Subprotocols: []string{websocketSubprotocol},
	})
	if err != nil {
		_ = browserWS.Close(websocket.StatusBadGateway, err.Error())
		log.Printf("relay dial target=%s error=%v", target, err)
		return
	}
	defer peerWS.CloseNow()

	if peerWS.Subprotocol() != websocketSubprotocol {
		_ = browserWS.Close(websocket.StatusPolicyViolation, "target did not negotiate ygg-ws")
		_ = peerWS.Close(websocket.StatusPolicyViolation, "target did not negotiate ygg-ws")
		return
	}

	log.Printf("relay connected remote=%s target=%s", r.RemoteAddr, target)
	err = pipe(ctx, websocket.NetConn(ctx, browserWS, websocket.MessageBinary), websocket.NetConn(ctx, peerWS, websocket.MessageBinary))
	if err != nil && ctx.Err() == nil {
		log.Printf("relay closed target=%s error=%v", target, err)
	}
}

func pipe(ctx context.Context, a, b net.Conn) error {
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(a, b)
		_ = a.Close()
		_ = b.Close()
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(b, a)
		_ = a.Close()
		_ = b.Close()
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		_ = a.Close()
		_ = b.Close()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
