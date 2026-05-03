package admin

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
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
