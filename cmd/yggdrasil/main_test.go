package main

import (
	"testing"

	"github.com/asciimoth/ygg/internal/transportcfg"
	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/transport"
)

func TestNewTransportManagerAppliesDefaultAnonymousNetworkBlocks(t *testing.T) {
	cfg := config.GenerateConfig()

	manager, defaultNetwork, err := newTransportManager(cfg)
	if err != nil {
		t.Fatalf("newTransportManager: %v", err)
	}

	if defaultNetwork == nil {
		t.Fatal("expected default transport network to be configured")
	}
	if name, ok := transport.BuiltinNetworkName(defaultNetwork); !ok || name != transport.NetworkKindNative {
		t.Fatalf("unexpected default transport network: %q %v", name, ok)
	}

	mappings := manager.NetworkMappings()
	want := []string{"*.tor", "*.i2p", "*.loki"}
	if len(mappings) != len(want) {
		t.Fatalf("unexpected default transport mappings: %#v", mappings)
	}
	for _, pattern := range want {
		network, ok := mappings[pattern]
		if !ok {
			t.Fatalf("missing default transport mapping for %q: %#v", pattern, mappings)
		}
		if network != nil {
			t.Fatalf("expected default transport mapping %q to be nil, got %#v", pattern, network)
		}
	}
}

func TestNewTransportManagerSupportsSocksMappings(t *testing.T) {
	cfg := config.GenerateConfig()
	cfg.Transport.DefaultNetwork = config.NullTransportNetworkConfig()
	cfg.Transport.NetworkMappings["proxy.test"] = config.NewSocksTransportNetworkConfig("socks5://proxy.internal:1080")

	manager, defaultNetwork, err := newTransportManager(cfg)
	if err != nil {
		t.Fatalf("newTransportManager: %v", err)
	}
	if defaultNetwork != nil {
		t.Fatalf("expected nil default network, got %T", defaultNetwork)
	}

	mapped := manager.NetworkMappings()["proxy.test"]
	if mapped == nil {
		t.Fatal("expected socks mapping to be configured")
	}
	if got := transportcfg.ConfigFromNetwork(mapped); got.Name() != transportcfg.NetworkKindSocks || got.ProxyURL() != "socks5://proxy.internal:1080" {
		t.Fatalf("unexpected mapped network config: %#v", got)
	}
}
