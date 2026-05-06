//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/reject"
	"github.com/coder/websocket"

	"github.com/asciimoth/ygg/ygglib/autopeer"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/ipv6rwc"
	ygglogger "github.com/asciimoth/ygg/ygglib/logger"
	"github.com/asciimoth/ygg/ygglib/transport"
	yggtun "github.com/asciimoth/ygg/ygglib/tun"
)

const (
	websocketSubprotocol = "ygg-ws"
	requestTimeout       = 30 * time.Second
	defaultCountries     = "finland,germany,hungary,netherlands,russia,united-states"
	defaultSchemes       = "wss"
)

type startConfig struct {
	ManualPeers      string `json:"manualPeers"`
	Countries        string `json:"countries"`
	TransportSchemes string `json:"transportSchemes"`
	RelayURL         string `json:"relayUrl"`
}

type requestConfig struct {
	Method  string `json:"method"`
	URL     string `json:"url"`
	Headers string `json:"headers"`
	Body    string `json:"body"`
}

type appState struct {
	Address string      `json:"address"`
	Subnet  string      `json:"subnet"`
	Peers   []peerState `json:"peers"`
}

type peerState struct {
	URI     string `json:"uri"`
	Up      bool   `json:"up"`
	Inbound bool   `json:"inbound"`
	Error   string `json:"error,omitempty"`
}

type requestResult struct {
	Status     string            `json:"status"`
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

type browserNode struct {
	core     *core.Core
	rwc      *ipv6rwc.ReadWriteCloser
	adapter  *yggtun.TunAdapter
	vt       *vtun.VTun
	autopeer *autopeer.Manager
}

type browserWebSocketTransport struct {
	schemes []string
	relay   string
}

type jsLogger struct{}

var (
	mu   sync.Mutex
	node *browserNode
)

func main() {
	logLine("wasm", "initializing bindings")
	js.Global().Set("yggDemoStart", promiseFunc(func(args []js.Value) (any, error) {
		cfg := startConfig{
			Countries:        defaultCountries,
			TransportSchemes: defaultSchemes,
			RelayURL:         "/ygg-peer",
		}
		if len(args) > 0 && args[0].Type() == js.TypeString {
			if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
				return nil, err
			}
		}
		return startNode(cfg)
	}))
	js.Global().Set("yggDemoStop", promiseFunc(func(args []js.Value) (any, error) {
		return map[string]string{"message": "node stopped"}, stopNode()
	}))
	js.Global().Set("yggDemoState", promiseFunc(func(args []js.Value) (any, error) {
		return currentState()
	}))
	js.Global().Set("yggDemoRequest", promiseFunc(func(args []js.Value) (any, error) {
		var cfg requestConfig
		if len(args) == 0 || args[0].Type() != js.TypeString {
			return nil, errors.New("request config JSON is required")
		}
		if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
			return nil, err
		}
		return runHTTPRequest(cfg)
	}))
	logLine("wasm", "bindings ready")
	select {}
}

func startNode(cfg startConfig) (any, error) {
	mu.Lock()
	defer mu.Unlock()

	if node != nil {
		logLine("app", "restarting node")
		node.close()
		node = nil
	}

	n, err := newBrowserNode(cfg)
	if err != nil {
		return nil, err
	}
	node = n
	subnet := n.core.Subnet()
	logLine("app", fmt.Sprintf("node started address=%s subnet=%s", n.core.Address(), subnet.String()))
	return n.state(), nil
}

func stopNode() error {
	mu.Lock()
	defer mu.Unlock()
	if node == nil {
		return nil
	}
	err := node.close()
	node = nil
	logLine("app", "node stopped")
	return err
}

func currentState() (any, error) {
	mu.Lock()
	defer mu.Unlock()
	if node == nil {
		return nil, errors.New("node is not running")
	}
	return node.state(), nil
}

func runHTTPRequest(cfg requestConfig) (any, error) {
	mu.Lock()
	n := node
	mu.Unlock()
	if n == nil || n.vt == nil {
		return nil, errors.New("node is not running")
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("request URL is required")
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodGet
	}
	logLine("http", fmt.Sprintf("request start method=%s url=%s", method, cfg.URL))

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, strings.NewReader(cfg.Body))
	if err != nil {
		logLine("http", fmt.Sprintf("request failed method=%s url=%s error=%v", method, cfg.URL, err))
		return nil, err
	}
	if err := applyHeaders(req, cfg.Headers); err != nil {
		return nil, err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       n.vt.Dial,
		},
		Timeout: requestTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	logLine("http", fmt.Sprintf("request done method=%s url=%s status=%s bytes=%d", method, cfg.URL, resp.Status, len(body)))
	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		headers[key] = strings.Join(values, ", ")
	}
	return requestResult{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       string(body),
	}, nil
}

func newBrowserNode(start startConfig) (*browserNode, error) {
	cfg := config.GenerateConfig()
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}
	logLine("app", "generated ephemeral node certificate")

	manager, err := newTransportManager(start.RelayURL)
	if err != nil {
		return nil, err
	}

	log := jsLogger{}
	coreNode, err := core.New(cfg.Certificate, log, core.TransportManager{Manager: manager})
	if err != nil {
		return nil, fmt.Errorf("create core: %w", err)
	}
	logLine("core", fmt.Sprintf("created address=%s", coreNode.Address()))

	n := &browserNode{core: coreNode}
	defer func() {
		if err != nil {
			n.close()
		}
	}()

	rwc := ipv6rwc.NewReadWriteCloser(coreNode)
	adapter, err := yggtun.New(rwc, ygglogger.Discard(), yggtun.InterfaceMTU(1500))
	if err != nil {
		return nil, fmt.Errorf("create tun adapter: %w", err)
	}
	n.rwc = rwc
	n.adapter = adapter

	vt, err := buildVTun(coreNode.Address())
	if err != nil {
		return nil, err
	}
	n.vt = vt

	if err := adapter.Attach(vt, yggtun.AttachmentType("vtun")); err != nil {
		return nil, fmt.Errorf("attach vtun: %w", err)
	}
	logLine("vtun", "attached")

	if err := addManualPeers(coreNode, start.ManualPeers); err != nil {
		return nil, err
	}
	n.autopeer = startAutoPeer(log, coreNode, start)
	return n, nil
}

func newTransportManager(relay string) (*transport.Manager, error) {
	manager := transport.NewManager(&reject.Network{})
	for _, t := range []transport.Transport{
		&browserWebSocketTransport{schemes: []string{"ws"}, relay: relay},
		&browserWebSocketTransport{schemes: []string{"wss"}, relay: relay},
	} {
		if err := manager.RegisterTransport(t); err != nil {
			return nil, fmt.Errorf("register websocket transport: %w", err)
		}
	}
	return manager, nil
}

func buildVTun(localIP net.IP) (*vtun.VTun, error) {
	addr, ok := netip.AddrFromSlice(localIP)
	if !ok {
		return nil, fmt.Errorf("invalid yggdrasil address %q", localIP.String())
	}
	vt, err := (&vtun.Opts{
		Name:           "ygg-web",
		LocalAddrs:     []netip.Addr{addr},
		NoLoopbackAddr: true,
		NetStackOpts:   &helpers.Opts{MTU: 1500},
	}).Build()
	if err != nil {
		return nil, fmt.Errorf("build vtun: %w", err)
	}
	return vt, nil
}

func addManualPeers(coreNode *core.Core, raw string) error {
	for _, line := range splitList(raw) {
		u, err := url.Parse(line)
		if err != nil {
			return fmt.Errorf("parse peer %q: %w", line, err)
		}
		if err := coreNode.AddPeer(u, ""); err != nil {
			return fmt.Errorf("add peer %q: %w", line, err)
		}
		logLine("peer", "manual peer added "+line)
	}
	return nil
}

func startAutoPeer(log autopeer.Logger, coreNode *core.Core, cfg startConfig) *autopeer.Manager {
	countries := splitList(cfg.Countries)
	schemes := splitList(cfg.TransportSchemes)
	if _, ok := makeSet(schemes)["ws"]; ok {
		logLine("autopeer", "warning: ws:// peers may be blocked by browser HTTPS-Only Mode or mixed-content policy; prefer wss")
	}
	fetcher := autopeer.NewFetcher(log, time.Hour)
	fetcher.SetSources([]string{autopeer.BuiltinSource})
	manager := autopeer.NewManager(fetcher)
	manager.SetOnChange(func(peers []autopeer.Peer) {
		logLine(
			"autopeer",
			fmt.Sprintf(
				"source=%s peers=%d eligible_websocket_endpoints=%d countries=%s schemes=%s",
				autopeer.BuiltinSource,
				len(peers),
				countEligibleAutoPeerEndpoints(peers, countries, schemes),
				strings.Join(countries, ","),
				strings.Join(schemes, ","),
			),
		)
	})
	manager.SetPeerManager(coreNode)
	manager.SetConfig(autopeer.ManagerConfig{
		CheckInterval:             10 * time.Second,
		MinimumConnected:          1,
		MinimumConnectedFromFetch: 1,
		Countries:                 countries,
		TransportSchemes:          schemes,
	})
	manager.Start()
	logLine("autopeer", fmt.Sprintf("started source=%s countries=%s schemes=%s", autopeer.BuiltinSource, strings.Join(countries, ","), strings.Join(schemes, ",")))
	return manager
}

func (n *browserNode) close() error {
	var errs []error
	if n.autopeer != nil {
		errs = append(errs, n.autopeer.Close())
	}
	if n.adapter != nil {
		errs = append(errs, n.adapter.Stop())
	}
	if n.vt != nil {
		errs = append(errs, n.vt.Close())
	}
	if n.rwc != nil {
		errs = append(errs, n.rwc.Close())
	}
	if n.core != nil {
		n.core.Stop()
	}
	return errors.Join(errs...)
}

func (n *browserNode) state() appState {
	peers := n.core.GetPeers()
	out := make([]peerState, 0, len(peers))
	for _, peer := range peers {
		ps := peerState{URI: peer.URI, Up: peer.Up, Inbound: peer.Inbound}
		if peer.LastError != nil {
			ps.Error = peer.LastError.Error()
		}
		out = append(out, ps)
	}
	subnet := n.core.Subnet()
	return appState{
		Address: n.core.Address().String(),
		Subnet:  subnet.String(),
		Peers:   out,
	}
}

func (t *browserWebSocketTransport) Schemes() []string {
	return append([]string(nil), t.schemes...)
}

func (t *browserWebSocketTransport) Dial(
	ctx context.Context,
	_ transport.Network,
	u *url.URL,
	_ transport.Options,
) (transport.Conn, error) {
	target := u.String()
	if strings.TrimSpace(t.relay) != "" {
		relayURL, err := browserRelayURL(t.relay, target)
		if err != nil {
			return nil, err
		}
		logLine("transport", fmt.Sprintf("dial relay=%s target=%s", relayURL, target))
		target = relayURL
	} else {
		logLine("transport", "dial direct "+target)
	}

	wsconn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		Subprotocols: []string{websocketSubprotocol},
	})
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(ctx, wsconn, websocket.MessageBinary), nil
}

func browserRelayURL(relay, target string) (string, error) {
	relay = strings.TrimSpace(relay)
	if relay == "" {
		return target, nil
	}
	if strings.HasPrefix(relay, "/") {
		location := js.Global().Get("location")
		protocol := "ws:"
		if location.Get("protocol").String() == "https:" {
			protocol = "wss:"
		}
		relay = protocol + "//" + location.Get("host").String() + relay
	}
	u, err := url.Parse(relay)
	if err != nil {
		return "", fmt.Errorf("parse relay url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported relay scheme %q", u.Scheme)
	}
	q := u.Query()
	q.Set("target", target)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (t *browserWebSocketTransport) Listen(
	context.Context,
	transport.Network,
	*url.URL,
	transport.Options,
) (transport.Listener, error) {
	return nil, errors.New("browser websocket listen is not supported")
}

func applyHeaders(req *http.Request, raw string) error {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("invalid header line %q", line)
		}
		req.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	return nil
}

func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func countEligibleAutoPeerEndpoints(peers []autopeer.Peer, countries, schemes []string) int {
	countrySet := makeSet(countries)
	schemeSet := makeSet(schemes)
	count := 0
	for _, peer := range peers {
		if len(countrySet) > 0 {
			if _, ok := countrySet[strings.ToLower(strings.TrimSpace(peer.Country))]; !ok {
				continue
			}
		}
		for _, endpoint := range peer.Endpoints {
			scheme := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
			if scheme == "" {
				u, err := url.Parse(endpoint.URL)
				if err == nil {
					scheme = strings.ToLower(u.Scheme)
				}
			}
			if len(schemeSet) > 0 {
				if _, ok := schemeSet[scheme]; !ok {
					continue
				}
			}
			if strings.TrimSpace(endpoint.URL) != "" {
				count++
			}
		}
	}
	return count
}

func makeSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func promiseFunc(fn func([]js.Value) (any, error)) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) any {
		handler := js.FuncOf(func(this js.Value, promiseArgs []js.Value) any {
			resolve := promiseArgs[0]
			reject := promiseArgs[1]
			go func() {
				value, err := fn(args)
				if err != nil {
					reject.Invoke(jsError(err))
					return
				}
				raw, err := json.Marshal(value)
				if err != nil {
					reject.Invoke(jsError(err))
					return
				}
				resolve.Invoke(string(raw))
			}()
			return nil
		})
		return js.Global().Get("Promise").New(handler)
	})
}

func jsError(err error) js.Value {
	return js.Global().Get("Error").New(err.Error())
}

func (jsLogger) Debug(args ...any)                 { console("debug", args...) }
func (jsLogger) Debugf(format string, args ...any) { consolef("debug", format, args...) }
func (jsLogger) Info(args ...any)                  { console("info", args...) }
func (jsLogger) Infof(format string, args ...any)  { consolef("info", format, args...) }
func (jsLogger) Warn(args ...any)                  { console("warn", args...) }
func (jsLogger) Warnf(format string, args ...any)  { consolef("warn", format, args...) }
func (jsLogger) Err(args ...any)                   { console("error", args...) }
func (jsLogger) Errf(format string, args ...any)   { consolef("error", format, args...) }
func (jsLogger) Fatal(args ...any)                 { console("error", args...) }
func (jsLogger) Fatalf(format string, args ...any) { consolef("error", format, args...) }

func console(method string, args ...any) {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}
	logLine(method, strings.Join(parts, " "))
}

func consolef(method, format string, args ...any) {
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(&buf, format, args...)
	logLine(method, buf.String())
}

func logLine(kind, message string) {
	line := strings.TrimSpace(kind) + " " + strings.TrimSpace(message)
	appendFn := js.Global().Get("yggDemoAppendLog")
	if appendFn.Type() == js.TypeFunction {
		appendFn.Invoke(time.Now().Format("15:04:05.000") + " " + line)
	}

	method := "log"
	switch kind {
	case "debug", "info", "warn", "error":
		method = kind
	}
	js.Global().Get("console").Call(method, line)
}

var _ transport.Transport = (*browserWebSocketTransport)(nil)
