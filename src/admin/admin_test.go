package admin

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/asciimoth/ygg/autopeer"
)

type testLogger struct{}

func (testLogger) Printf(string, ...interface{}) {}
func (testLogger) Println(...interface{})        {}
func (testLogger) Infof(string, ...interface{})  {}
func (testLogger) Infoln(...interface{})         {}
func (testLogger) Warnf(string, ...interface{})  {}
func (testLogger) Warnln(...interface{})         {}
func (testLogger) Errorf(string, ...interface{}) {}
func (testLogger) Errorln(...interface{})        {}
func (testLogger) Debugf(string, ...interface{}) {}
func (testLogger) Debugln(...interface{})        {}
func (testLogger) Traceln(...interface{})        {}

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

type autopeerTestLogger struct{}

func (*autopeerTestLogger) Printf(string, ...interface{}) {}
