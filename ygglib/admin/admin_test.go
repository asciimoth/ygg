package admin

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ygg/ygglib/autopeer"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/core"
	"github.com/asciimoth/ygg/ygglib/jumper"
	"github.com/asciimoth/ygg/ygglib/transport"
	"github.com/asciimoth/ygg/ygglib/transportcfg"
)

type testLogger struct{}

func (testLogger) Debug(...any)          {}
func (testLogger) Debugf(string, ...any) {}
func (testLogger) Info(...any)           {}
func (testLogger) Infof(string, ...any)  {}
func (testLogger) Warn(...any)           {}
func (testLogger) Warnf(string, ...any)  {}
func (testLogger) Err(...any)            {}
func (testLogger) Errf(string, ...any)   {}
func (testLogger) Fatal(...any)          {}
func (testLogger) Fatalf(string, ...any) {}

type stubJumperCore struct{}

func (stubJumperCore) AddPeer(*url.URL, string) error              { return nil }
func (stubJumperCore) RemovePeer(*url.URL, string) error           { return nil }
func (stubJumperCore) GetPeers() []core.PeerInfo                   { return nil }
func (stubJumperCore) GetNodeInfo(string) (json.RawMessage, error) { return nil, nil }

func TestAdminSocketConcurrentHandlerAccess(t *testing.T) {
	a := &AdminSocket{
		log:      testLogger{},
		handlers: make(map[string]handler),
		done:     make(chan struct{}),
	}
	if err := a.AddHandler("ping", "ping", nil, func(json.RawMessage) (interface{}, error) {
		return map[string]string{"ok": "true"}, nil
	}); err != nil {
		t.Fatalf("AddHandler(ping): %v", err)
	}

	server, client := net.Pipe()
	defer client.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		a.handleRequest(server)
	}()

	reqErr := make(chan error, 1)
	go func() {
		enc := json.NewEncoder(client)
		dec := json.NewDecoder(client)
		for i := 0; i < 200; i++ {
			req := AdminSocketRequest{
				Name:      "ping",
				KeepAlive: i < 199,
			}
			if err := enc.Encode(req); err != nil {
				reqErr <- err
				return
			}
			var resp AdminSocketResponse
			if err := dec.Decode(&resp); err != nil {
				reqErr <- err
				return
			}
			if resp.Status != "success" {
				reqErr <- fmt.Errorf("unexpected response status %q", resp.Status)
				return
			}
		}
		reqErr <- nil
	}()

	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("extra-%d", i)
		if err := a.AddHandler(name, "extra", nil, func(json.RawMessage) (interface{}, error) {
			return struct{}{}, nil
		}); err != nil {
			t.Fatalf("AddHandler(%s): %v", name, err)
		}
	}

	if err := <-reqErr; err != nil {
		t.Fatalf("request loop failed: %v", err)
	}
	<-serverDone
}

func TestSetupConfigHandlersExposesAllNodeConfigOptions(t *testing.T) {
	cfg := config.GenerateConfig()
	cfg.LocalDNSListen = ""
	cfg.TunSocksNoResolve = nil

	a := &AdminSocket{
		log:      testLogger{},
		handlers: make(map[string]handler),
		done:     make(chan struct{}),
	}
	a.SetupConfigHandlers(NewConfigController(cfg))

	h, ok := a.handlers["getconfig"]
	if !ok {
		t.Fatal("expected getConfig handler to be registered")
	}
	res, err := h.handler(nil)
	if err != nil {
		t.Fatalf("getConfig handler returned error: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal getConfig response: %v", err)
	}
	var resp GetConfigResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal getConfig response: %v", err)
	}

	cfgType := reflect.TypeOf(config.NodeConfig{})
	for i := 0; i < cfgType.NumField(); i++ {
		field := cfgType.Field(i)
		name := testJSONFieldName(field)
		if name == "-" {
			if _, ok := resp.Config[field.Name]; ok {
				t.Fatalf("field %s must not be exposed", field.Name)
			}
			continue
		}
		if _, ok := resp.Config[name]; !ok {
			t.Fatalf("missing config option %s", name)
		}
	}
	if _, ok := resp.Config["LocalDNSListen"]; !ok {
		t.Fatal("expected empty LocalDNSListen option to be present")
	}
	if _, ok := resp.Config["TunSocksNoResolve"]; !ok {
		t.Fatal("expected nil TunSocksNoResolve option to be present")
	}
}

func testJSONFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return field.Name
	}
	return name
}

func TestSetupAutoPeerHandlers(t *testing.T) {
	logger := &autopeerTestLogger{}
	fetcher := autopeer.NewFetcher(logger, time.Hour)
	fetcher.SetSources([]string{autopeer.BuiltinSource})
	fetcher.SetDefaultNetwork(nil)

	manager := autopeer.NewManager(fetcher)
	manager.SetConfig(autopeer.ManagerConfig{
		CheckInterval:             time.Minute,
		MinimumConnected:          2,
		MinimumConnectedFromFetch: 1,
		Countries:                 []string{"georgia"},
		TransportSchemes:          []string{"tls"},
	})

	a := &AdminSocket{
		log:      testLogger{},
		handlers: make(map[string]handler),
		done:     make(chan struct{}),
	}
	controller := NewAutoPeerController(manager, true)
	a.SetupAutoPeerHandlers(controller)

	h, ok := a.handlers["getautopeer"]
	if !ok {
		t.Fatal("expected getAutoPeer handler to be registered")
	}
	res, err := h.handler(nil)
	if err != nil {
		t.Fatalf("getAutoPeer handler returned error: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal getAutoPeer response: %v", err)
	}

	var resp GetAutoPeerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal getAutoPeer response: %v", err)
	}
	if !resp.Enabled {
		t.Fatal("expected autopeer enabled flag in response")
	}
	if resp.Active {
		t.Fatal("expected autopeer manager to be inactive")
	}
	if resp.FetchInterval != time.Hour.String() {
		t.Fatalf("unexpected fetch interval %q", resp.FetchInterval)
	}
	if resp.CheckInterval != time.Minute.String() {
		t.Fatalf("unexpected check interval %q", resp.CheckInterval)
	}
	if len(resp.Sources) != 1 || resp.Sources[0] != autopeer.BuiltinSource {
		t.Fatalf("unexpected sources: %#v", resp.Sources)
	}
	if len(resp.Peers) == 0 {
		t.Fatal("expected builtin autopeer source to expose fetched peers")
	}
}

func TestAutoPeerControllerApply(t *testing.T) {
	logger := &autopeerTestLogger{}
	fetcher := autopeer.NewFetcher(logger, time.Hour)
	manager := autopeer.NewManager(fetcher)
	controller := NewAutoPeerController(manager, false)

	err := controller.Apply(&SetAutoPeerRequest{
		Enabled:                   "true",
		Sources:                   "BUILTIN",
		FetchInterval:             "30m",
		CheckInterval:             "5s",
		MinimumConnected:          "2",
		MinimumConnectedFromFetch: "1",
		Countries:                 "georgia, france",
		TransportSchemes:          "tls,tcp",
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if !controller.Enabled() {
		t.Fatal("expected controller to be enabled")
	}
	if !manager.IsStarted() {
		t.Fatal("expected manager to be started")
	}

	snapshot := controller.Snapshot()
	if snapshot == nil {
		t.Fatal("expected autopeer snapshot")
	}
	if snapshot.FetchInterval != "30m0s" {
		t.Fatalf("unexpected fetch interval %q", snapshot.FetchInterval)
	}
	if snapshot.CheckInterval != "5s" {
		t.Fatalf("unexpected check interval %q", snapshot.CheckInterval)
	}
	if snapshot.MinimumConnected != 2 || snapshot.MinimumConnectedFromFetch != 1 {
		t.Fatalf("unexpected thresholds: %#v", snapshot)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0] != autopeer.BuiltinSource {
		t.Fatalf("unexpected sources: %#v", snapshot.Sources)
	}
	if len(snapshot.Countries) != 2 || snapshot.Countries[0] != "georgia" || snapshot.Countries[1] != "france" {
		t.Fatalf("unexpected countries: %#v", snapshot.Countries)
	}
	if len(snapshot.TransportSchemes) != 2 || snapshot.TransportSchemes[0] != "tls" || snapshot.TransportSchemes[1] != "tcp" {
		t.Fatalf("unexpected transport schemes: %#v", snapshot.TransportSchemes)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("manager close failed: %v", err)
	}
}

type stubNodeInfoUpdater struct {
	info    map[string]interface{}
	privacy bool
}

func (s *stubNodeInfoUpdater) SetNodeInfo(info map[string]interface{}, privacy bool) error {
	s.info = info
	s.privacy = privacy
	return nil
}

func TestSetupJumperHandlers(t *testing.T) {
	manager := jumper.NewManager(&stubJumperCore{}, testLogger{}, jumper.Config{
		CheckInterval: time.Minute,
		LinkTimeout:   2 * time.Minute,
	})
	updater := &stubNodeInfoUpdater{}
	controller := NewJumperController(manager, updater, map[string]interface{}{"name": "node"}, true, false, nil)

	a := &AdminSocket{
		log:      testLogger{},
		handlers: make(map[string]handler),
		done:     make(chan struct{}),
	}
	a.SetupJumperHandlers(controller)

	h, ok := a.handlers["getjumper"]
	if !ok {
		t.Fatal("expected getJumper handler to be registered")
	}
	res, err := h.handler(nil)
	if err != nil {
		t.Fatalf("getJumper handler returned error: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal getJumper response: %v", err)
	}
	var resp GetJumperResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal getJumper response: %v", err)
	}
	if resp.Enabled || resp.Active {
		t.Fatalf("expected disabled inactive jumper, got %#v", resp)
	}
	if resp.CheckInterval != "1m0s" || resp.LinkTimeout != "2m0s" {
		t.Fatalf("unexpected intervals: %#v", resp)
	}
}

func TestJumperControllerApply(t *testing.T) {
	manager := jumper.NewManager(&stubJumperCore{}, testLogger{}, jumper.Config{})
	updater := &stubNodeInfoUpdater{}
	controller := NewJumperController(
		manager,
		updater,
		map[string]interface{}{"name": "node", jumper.NodeInfoKey: "old"},
		true,
		false,
		nil,
	)

	if err := controller.Apply(&SetJumperRequest{
		Enabled:       "true",
		Addresses:     "tls://node.example:1234, tcp://node.example:5678",
		CheckInterval: "5s",
		LinkTimeout:   "10s",
	}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	defer manager.Close()

	snapshot := controller.Snapshot()
	if snapshot == nil {
		t.Fatal("expected jumper snapshot")
	}
	if !snapshot.Enabled || !snapshot.Active {
		t.Fatalf("expected enabled active jumper, got %#v", snapshot)
	}
	if snapshot.CheckInterval != "5s" || snapshot.LinkTimeout != "10s" {
		t.Fatalf("unexpected intervals: %#v", snapshot)
	}
	if len(snapshot.Addresses) != 2 || snapshot.Addresses[0] != "tls://node.example:1234" {
		t.Fatalf("unexpected addresses: %#v", snapshot.Addresses)
	}
	if !updater.privacy {
		t.Fatal("expected nodeinfo privacy flag to be preserved")
	}
	if updater.info["name"] != "node" {
		t.Fatalf("expected base nodeinfo to be preserved: %#v", updater.info)
	}
	if updater.info[jumper.NodeInfoKey] == "old" {
		t.Fatalf("expected stale jumper nodeinfo to be replaced: %#v", updater.info)
	}

	if err := controller.Apply(&SetJumperRequest{Enabled: "false"}); err != nil {
		t.Fatalf("disable returned error: %v", err)
	}
	if manager.IsStarted() {
		t.Fatal("expected jumper manager to stop")
	}
	if _, ok := updater.info[jumper.NodeInfoKey]; ok {
		t.Fatalf("expected jumper nodeinfo to be removed when disabled: %#v", updater.info)
	}
}

func TestTransportHandlers(t *testing.T) {
	a := &AdminSocket{
		core:     newAdminTestCore(t),
		log:      testLogger{},
		handlers: make(map[string]handler),
		done:     make(chan struct{}),
	}
	a.SetupCoreHandlers()

	getResp, err := a.getTransportHandler()
	if err != nil {
		t.Fatalf("getTransportHandler: %v", err)
	}
	if getResp.DefaultNetwork == nil || *getResp.DefaultNetwork != transport.NetworkKindNative {
		t.Fatalf("unexpected initial default network: %#v", getResp.DefaultNetwork)
	}
	if got := getResp.DefaultNetworkConfig; got != transport.NetworkKindNative {
		t.Fatalf("unexpected initial default network config: %#v", got)
	}
	assertInitialTransportMappings(t, getResp.NetworkMappings)

	if err := a.setTransportHandler(&SetTransportRequest{
		DefaultNetworkConfig: `{"type":"socks","proxy_url":"socks5://proxy.internal:1080"}`,
		NetworkMappings:      `{"*.example":"native","disabled.example":null,"hidden.example":{"type":"socks","proxy_url":"socks5://tor:9050"}}`,
		UnsetNetworkMappings: "unused.example",
	}); err != nil {
		t.Fatalf("setTransportHandler(nil+map): %v", err)
	}

	getResp, err = a.getTransportHandler()
	if err != nil {
		t.Fatalf("getTransportHandler after set: %v", err)
	}
	if getResp.DefaultNetwork == nil || *getResp.DefaultNetwork != transportcfg.NetworkKindSocks {
		t.Fatalf("unexpected socks default network name: %#v", getResp.DefaultNetwork)
	}
	if got, ok := getResp.DefaultNetworkConfig.(map[string]any); !ok || got["type"] != "socks" || got["proxy_url"] != "socks5://proxy.internal:1080" {
		t.Fatalf("unexpected default network config: %#v", getResp.DefaultNetworkConfig)
	}
	if got := getResp.NetworkMappings["*.example"]; got == nil || *got != transport.NetworkKindNative {
		t.Fatalf("unexpected mapped network: %#v", got)
	}
	if got := getResp.NetworkMappings["disabled.example"]; got != nil {
		t.Fatalf("expected nil mapped network, got %#v", got)
	}
	if got := getResp.NetworkMappings["hidden.example"]; got == nil || *got != transportcfg.NetworkKindSocks {
		t.Fatalf("unexpected socks mapped network: %#v", got)
	}
	if got, ok := getResp.NetworkMappingConfigs["hidden.example"].(map[string]any); !ok || got["type"] != "socks" || got["proxy_url"] != "socks5://tor:9050" {
		t.Fatalf("unexpected socks mapped config: %#v", getResp.NetworkMappingConfigs["hidden.example"])
	}

	if err := a.setTransportHandler(&SetTransportRequest{
		DefaultNetwork:       "unset",
		UnsetNetworkMappings: "*.example,disabled.example,hidden.example",
	}); err != nil {
		t.Fatalf("setTransportHandler(unset): %v", err)
	}

	getResp, err = a.getTransportHandler()
	if err != nil {
		t.Fatalf("getTransportHandler after unset: %v", err)
	}
	if getResp.DefaultNetwork == nil || *getResp.DefaultNetwork != transport.NetworkKindNative {
		t.Fatalf("unexpected restored default network: %#v", getResp.DefaultNetwork)
	}
	assertInitialTransportMappings(t, getResp.NetworkMappings)
}

func assertInitialTransportMappings(t *testing.T, mappings map[string]*string) {
	t.Helper()

	want := map[string]struct{}{
		"*.onion": {},
		"*.i2p":   {},
		"*.loki":  {},
	}
	if len(mappings) != len(want) {
		t.Fatalf("unexpected initial transport mappings: %#v", mappings)
	}
	for pattern := range want {
		value, ok := mappings[pattern]
		if !ok {
			t.Fatalf("missing initial transport mapping for %q: %#v", pattern, mappings)
		}
		if value != nil {
			t.Fatalf("expected initial transport mapping %q to be nil, got %#v", pattern, value)
		}
	}
}

type autopeerTestLogger struct{}

func (*autopeerTestLogger) Debug(...any)          {}
func (*autopeerTestLogger) Debugf(string, ...any) {}
func (*autopeerTestLogger) Info(...any)           {}
func (*autopeerTestLogger) Infof(string, ...any)  {}
func (*autopeerTestLogger) Warn(...any)           {}
func (*autopeerTestLogger) Warnf(string, ...any)  {}
func (*autopeerTestLogger) Err(...any)            {}
func (*autopeerTestLogger) Errf(string, ...any)   {}
func (*autopeerTestLogger) Fatal(...any)          {}
func (*autopeerTestLogger) Fatalf(string, ...any) {}

func newAdminTestCore(t *testing.T) *core.Core {
	t.Helper()

	cfg := config.GenerateConfig()
	manager := newAdminTestTransportManager(t, cfg)
	node, err := core.New(cfg.Certificate, testLogger{}, core.TransportManager{Manager: manager})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(node.Stop)
	return node
}

func newAdminTestTransportManager(t *testing.T, cfg *config.NodeConfig) *transport.Manager {
	t.Helper()

	network, err := transport.NewBuiltinNetwork(transport.NetworkKindNative)
	if err != nil {
		t.Fatalf("NewBuiltinNetwork: %v", err)
	}
	manager := transport.NewManager(network)
	if err := manager.RegisterTransport(transport.NewTCPTransport()); err != nil {
		t.Fatalf("RegisterTransport(tcp): %v", err)
	}
	tlsConfig, err := core.GenerateTLSConfig(cfg.Certificate)
	if err != nil {
		t.Fatalf("GenerateTLSConfig: %v", err)
	}
	if err := manager.RegisterTransport(transport.NewTLSTransport(tlsConfig.Clone())); err != nil {
		t.Fatalf("RegisterTransport(tls): %v", err)
	}
	for pattern, networkCfg := range cfg.Transport.NetworkMappings {
		var mapped transport.Network
		if !networkCfg.IsNull() {
			mapped, err = transportcfg.NetworkFromConfig(networkCfg)
			if err != nil {
				t.Fatalf("NetworkFromConfig(%s): %v", networkCfg.Name(), err)
			}
		}
		if err := manager.MapNetwork(pattern, mapped); err != nil {
			t.Fatalf("MapNetwork(%s): %v", pattern, err)
		}
	}
	return manager
}
