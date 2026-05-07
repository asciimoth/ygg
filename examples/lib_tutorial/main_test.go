package main

import (
	"strings"
	"testing"
)

func TestLibTutorialExample(t *testing.T) {
	demo, err := newDemo()
	if err != nil {
		t.Fatalf("new demo: %v", err)
	}
	defer func() {
		if err := demo.Close(); err != nil {
			t.Fatalf("close demo: %v", err)
		}
	}()

	tcpBody, err := runTCPDemo(demo.client.vt, demo.server.vt, demo.server.core.Address())
	if err != nil {
		t.Fatalf("run tcp demo: %v", err)
	}
	if tcpBody != "tcp:ping" {
		t.Fatalf("tcp body = %q, want %q", tcpBody, "tcp:ping")
	}

	udpBody, err := runUDPDemo(demo.client.vt, demo.server.vt, demo.server.core.Address())
	if err != nil {
		t.Fatalf("run udp demo: %v", err)
	}
	if udpBody != "udp:ping" {
		t.Fatalf("udp body = %q, want %q", udpBody, "udp:ping")
	}

	httpBody, err := runHTTPDemo(demo.client.vt, demo.server.vt, demo.server.core.Address())
	if err != nil {
		t.Fatalf("run http demo: %v", err)
	}
	if httpBody != "http:pong" {
		t.Fatalf("http body = %q, want %q", httpBody, "http:pong")
	}

	if demo.transport.Dials() == 0 {
		t.Fatal("custom transport was not used for dialing")
	}
	if demo.transport.Listens() == 0 {
		t.Fatal("custom transport was not used for listening")
	}
}

func TestAutopeeringHelpers(t *testing.T) {
	demo, err := newDemo()
	if err != nil {
		t.Fatalf("new demo: %v", err)
	}
	defer func() {
		if err := demo.Close(); err != nil {
			t.Fatalf("close demo: %v", err)
		}
	}()

	publicPeers := configurePublicAutopeering(demo.client.core, demo.network)
	if publicPeers == nil {
		t.Fatal("configure public autopeering returned nil")
	}
	if err := publicPeers.Close(); err != nil {
		t.Fatalf("close public autopeering: %v", err)
	}

	_, err = startLinkLocalAutopeering(demo.client.core, demo.network, ".*")
	if err == nil {
		t.Fatal("link-local autopeering should reject non-native networks")
	}
	if !strings.Contains(err.Error(), "native") {
		t.Fatalf("link-local autopeering error = %q, want native-network error", err)
	}
}
