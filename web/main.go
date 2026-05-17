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

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect-netstack/helpers"
	"github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/irca"
	"github.com/asciimoth/mnlib"
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
	defaultDNSFallback   = "[300:6223::53]:53"
	defaultIRCServer     = "[324:71e:281a:9ed3::41]:6667"
)

type startConfig struct {
	ManualPeers      string `json:"manualPeers"`
	Countries        string `json:"countries"`
	TransportSchemes string `json:"transportSchemes"`
	DNSFallback      string `json:"dnsFallback"`
}

type requestConfig struct {
	Method  string `json:"method"`
	URL     string `json:"url"`
	Headers string `json:"headers"`
	Body    string `json:"body"`
}

type ircConnectConfig struct {
	Server           string `json:"server"`
	Nick             string `json:"nick"`
	Username         string `json:"username"`
	Realname         string `json:"realname"`
	Channel          string `json:"channel"`
	NickServMode     string `json:"nickServMode"`
	NickServPassword string `json:"nickServPassword"`
	NickServEmail    string `json:"nickServEmail"`
}

type ircSendConfig struct {
	Target string `json:"target"`
	Text   string `json:"text"`
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
	irc      *ircSession
}

type browserWebSocketTransport struct {
	schemes []string
}

type ircSession struct {
	client *irca.Client
	server string
	nick   string
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
			DNSFallback:      defaultDNSFallback,
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
	js.Global().Set("yggDemoIRCConnect", promiseFunc(func(args []js.Value) (any, error) {
		cfg := ircConnectConfig{Server: defaultIRCServer}
		if len(args) > 0 && args[0].Type() == js.TypeString {
			if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
				return nil, err
			}
		}
		return connectIRC(cfg)
	}))
	js.Global().Set("yggDemoIRCDisconnect", promiseFunc(func(args []js.Value) (any, error) {
		return map[string]string{"message": "irc disconnected"}, disconnectIRC()
	}))
	js.Global().Set("yggDemoIRCSend", promiseFunc(func(args []js.Value) (any, error) {
		var cfg ircSendConfig
		if len(args) == 0 || args[0].Type() != js.TypeString {
			return nil, errors.New("IRC message JSON is required")
		}
		if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
			return nil, err
		}
		return sendIRC(cfg)
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

func connectIRC(cfg ircConnectConfig) (any, error) {
	mu.Lock()
	n := node
	if n == nil || n.vt == nil {
		mu.Unlock()
		return nil, errors.New("node is not running")
	}
	old := n.irc
	n.irc = nil
	mu.Unlock()
	if old != nil {
		_ = old.close()
	}

	cfg = normalizeIRCConnectConfig(cfg)
	logLine("irc", fmt.Sprintf("connecting server=%s nick=%s", cfg.Server, cfg.Nick))
	appendIRC("status", fmt.Sprintf("Connecting to %s as %s", cfg.Server, cfg.Nick))

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	conn, err := n.vt.Dial(ctx, "tcp", cfg.Server)
	if err != nil {
		appendIRC("error", err.Error())
		return nil, err
	}

	session := &ircSession{
		client: irca.NewClient(conn),
		server: cfg.Server,
		nick:   cfg.Nick,
	}
	if err := session.client.Send(irca.Message{Command: "NICK", Params: []string{cfg.Nick}}); err != nil {
		_ = session.close()
		return nil, err
	}
	appendIRC("out", "/nick "+cfg.Nick)
	if err := session.client.Send(irca.Message{Command: "USER", Params: []string{cfg.Username, "0", "*", cfg.Realname}}); err != nil {
		_ = session.close()
		return nil, err
	}
	appendIRC("out", "/user "+cfg.Username)
	if err := sendNickServAuth(session, cfg); err != nil {
		_ = session.close()
		return nil, err
	}
	if cfg.Channel != "" {
		if err := session.client.Send(irca.Message{Command: "JOIN", Params: []string{cfg.Channel}}); err != nil {
			_ = session.close()
			return nil, err
		}
		appendIRC("out", "/join "+cfg.Channel)
	}

	mu.Lock()
	if node != n {
		mu.Unlock()
		_ = session.close()
		return nil, errors.New("node stopped while connecting IRC")
	}
	n.irc = session
	mu.Unlock()

	go session.recvLoop()
	appendIRC("status", fmt.Sprintf("Connected to %s", cfg.Server))
	logLine("irc", fmt.Sprintf("connected server=%s nick=%s", cfg.Server, cfg.Nick))
	return map[string]string{"server": cfg.Server, "nick": cfg.Nick}, nil
}

func disconnectIRC() error {
	mu.Lock()
	n := node
	if n == nil || n.irc == nil {
		mu.Unlock()
		return nil
	}
	session := n.irc
	n.irc = nil
	mu.Unlock()
	appendIRC("status", "Disconnected")
	return session.close()
}

func sendIRC(cfg ircSendConfig) (any, error) {
	mu.Lock()
	n := node
	if n == nil || n.irc == nil {
		mu.Unlock()
		return nil, errors.New("IRC is not connected")
	}
	session := n.irc
	mu.Unlock()

	msg, echo, err := buildIRCMessage(cfg)
	if err != nil {
		return nil, err
	}
	if err := session.client.Send(msg); err != nil {
		appendIRC("error", err.Error())
		return nil, err
	}
	appendIRC("out", echo)
	return map[string]string{"message": "sent"}, nil
}

func newBrowserNode(start startConfig) (*browserNode, error) {
	cfg := config.GenerateConfig()
	if err := cfg.GenerateSelfSignedCertificate(); err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}
	logLine("app", "generated ephemeral node certificate")

	manager, err := newTransportManager()
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

	vt, err := buildVTun(coreNode.Address(), start.DNSFallback)
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

func newTransportManager() (*transport.Manager, error) {
	manager := transport.NewManager(&gonnect.RejectNetwork{})
	for _, t := range []transport.Transport{
		&browserWebSocketTransport{schemes: []string{"ws"}},
		&browserWebSocketTransport{schemes: []string{"wss"}},
	} {
		if err := manager.RegisterTransport(t); err != nil {
			return nil, fmt.Errorf("register websocket transport: %w", err)
		}
	}
	return manager, nil
}

func buildVTun(localIP net.IP, dnsFallback string) (*vtun.VTun, error) {
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
	configureVTunResolver(vt, dnsFallback)
	return vt, nil
}

func configureVTunResolver(vt *vtun.VTun, dnsFallback string) {
	dnsFallback = strings.TrimSpace(dnsFallback)
	if dnsFallback == "" {
		dnsFallback = defaultDNSFallback
	}
	resolver := mnlib.NewResolver(vt)
	fallback := gonnect.ResolverCfg{
		Dial: vt.Dial,
		Server: &gonnect.DnsServer{
			Addr: dnsFallback,
		},
	}.Build()
	resolver.Fallback = &fallback
	vt.SetLookup(resolver.LookupIP)
	logLine("dns", fmt.Sprintf("configured mnlib resolver fallback_server=%q", dnsFallback))
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
	if n.irc != nil {
		errs = append(errs, n.irc.close())
	}
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

func (s *ircSession) close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *ircSession) recvLoop() {
	for {
		msg, err := s.client.Recv()
		if err != nil {
			appendIRC("status", fmt.Sprintf("Connection closed: %v", err))
			logLine("irc", fmt.Sprintf("receive stopped server=%s error=%v", s.server, err))
			return
		}
		appendIRC("in", formatIRCMessage(msg))
	}
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
	logLine("transport", "dial direct "+target)

	wsconn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		Subprotocols: []string{websocketSubprotocol},
	})
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(ctx, wsconn, websocket.MessageBinary), nil
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

func normalizeIRCConnectConfig(cfg ircConnectConfig) ircConnectConfig {
	cfg.Server = strings.TrimSpace(cfg.Server)
	if cfg.Server == "" {
		cfg.Server = defaultIRCServer
	}
	cfg.Nick = strings.TrimSpace(cfg.Nick)
	if cfg.Nick == "" {
		cfg.Nick = fmt.Sprintf("yggweb%d", time.Now().Unix()%10000)
	}
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.Username == "" {
		cfg.Username = "yggweb"
	}
	cfg.Realname = strings.TrimSpace(cfg.Realname)
	if cfg.Realname == "" {
		cfg.Realname = "Yggdrasil Web IRC"
	}
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	cfg.NickServMode = strings.ToLower(strings.TrimSpace(cfg.NickServMode))
	cfg.NickServPassword = strings.TrimSpace(cfg.NickServPassword)
	cfg.NickServEmail = strings.TrimSpace(cfg.NickServEmail)
	return cfg
}

func sendNickServAuth(session *ircSession, cfg ircConnectConfig) error {
	switch cfg.NickServMode {
	case "", "none":
		return nil
	case "login":
		if cfg.NickServPassword == "" {
			return errors.New("NickServ login requires a password")
		}
		msg := irca.Message{Command: "PRIVMSG", Params: []string{"NickServ", "IDENTIFY " + cfg.NickServPassword}}
		if err := session.client.Send(msg); err != nil {
			return err
		}
		appendIRC("out", "NickServ IDENTIFY ********")
		return nil
	case "register":
		if cfg.NickServPassword == "" {
			return errors.New("NickServ registration requires a password")
		}
		params := "REGISTER " + cfg.NickServPassword
		echo := "NickServ REGISTER ********"
		if cfg.NickServEmail != "" {
			params += " " + cfg.NickServEmail
			echo += " " + cfg.NickServEmail
		}
		msg := irca.Message{Command: "PRIVMSG", Params: []string{"NickServ", params}}
		if err := session.client.Send(msg); err != nil {
			return err
		}
		appendIRC("out", echo)
		return nil
	default:
		return fmt.Errorf("unknown NickServ mode %q", cfg.NickServMode)
	}
}

func buildIRCMessage(cfg ircSendConfig) (irca.Message, string, error) {
	text := strings.TrimSpace(cfg.Text)
	if text == "" {
		return irca.Message{}, "", errors.New("message is required")
	}
	if strings.HasPrefix(text, "/") {
		msg, echo, err := parseIRCCommand(text[1:])
		return msg, echo, err
	}
	target := strings.TrimSpace(cfg.Target)
	if target == "" {
		return irca.Message{}, "", errors.New("target is required for regular chat messages")
	}
	return irca.Message{Command: "PRIVMSG", Params: []string{target, text}}, fmt.Sprintf("<me:%s> %s", target, text), nil
}

func parseIRCCommand(raw string) (irca.Message, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return irca.Message{}, "", errors.New("IRC command is required")
	}
	command, rest, _ := strings.Cut(raw, " ")
	command = strings.ToUpper(strings.TrimSpace(command))
	rest = strings.TrimSpace(rest)
	switch command {
	case "JOIN":
		if rest == "" {
			return irca.Message{}, "", errors.New("/join requires a channel")
		}
		return irca.Message{Command: "JOIN", Params: []string{rest}}, "/join " + rest, nil
	case "PART":
		if rest == "" {
			return irca.Message{}, "", errors.New("/part requires a channel")
		}
		return irca.Message{Command: "PART", Params: []string{rest}}, "/part " + rest, nil
	case "NICK":
		if rest == "" {
			return irca.Message{}, "", errors.New("/nick requires a nickname")
		}
		return irca.Message{Command: "NICK", Params: []string{rest}}, "/nick " + rest, nil
	case "MSG":
		target, text, ok := strings.Cut(rest, " ")
		if !ok || strings.TrimSpace(target) == "" || strings.TrimSpace(text) == "" {
			return irca.Message{}, "", errors.New("/msg requires a target and message")
		}
		text = strings.TrimSpace(text)
		return irca.Message{Command: "PRIVMSG", Params: []string{target, text}}, fmt.Sprintf("<me:%s> %s", target, text), nil
	case "ME":
		target, text, ok := strings.Cut(rest, " ")
		if !ok || strings.TrimSpace(target) == "" || strings.TrimSpace(text) == "" {
			return irca.Message{}, "", errors.New("/me requires a target and action")
		}
		text = strings.TrimSpace(text)
		return irca.Message{Command: "PRIVMSG", Params: []string{target, "\x01ACTION " + text + "\x01"}}, fmt.Sprintf("* me:%s %s", target, text), nil
	case "QUIT":
		if rest == "" {
			rest = "leaving"
		}
		return irca.Message{Command: "QUIT", Params: []string{rest}}, "/quit " + rest, nil
	default:
		if rest == "" {
			return irca.Message{Command: command}, "/" + command, nil
		}
		msg, err := irca.ParseMessage(command + " " + rest)
		if err != nil {
			return irca.Message{}, "", err
		}
		return msg, "/" + command + " " + rest, nil
	}
}

func formatIRCMessage(msg irca.Message) string {
	var b strings.Builder
	if msg.Prefix != "" {
		b.WriteByte('[')
		b.WriteString(msg.Prefix)
		b.WriteString("] ")
	}
	b.WriteString(msg.Command)
	if len(msg.Params) > 0 {
		b.WriteByte(' ')
		b.WriteString(strings.Join(msg.Params, " "))
	}
	return b.String()
}

func appendIRC(kind, message string) {
	appendFn := js.Global().Get("yggDemoAppendIRC")
	if appendFn.Type() == js.TypeFunction {
		appendFn.Invoke(kind, time.Now().Format("15:04:05"), message)
		return
	}
	logLine("irc", message)
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
