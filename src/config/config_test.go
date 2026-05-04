package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/ygg/autopeer"
)

func assertDefaultTransportMappings(t *testing.T, mappings map[string]TransportNetworkConfig) {
	t.Helper()

	want := map[string]struct{}{
		"*.onion": {},
		"*.i2p":   {},
		"*.loki":  {},
	}
	if len(mappings) != len(want) {
		t.Fatalf("unexpected transport mappings: %#v", mappings)
	}
	for pattern := range want {
		mapping, ok := mappings[pattern]
		if !ok {
			t.Fatalf("missing default transport mapping for %q: %#v", pattern, mappings)
		}
		if !mapping.IsNull() {
			t.Fatalf("expected default transport mapping %q to be null, got %#v", pattern, mapping)
		}
	}
}

func TestConfig_Keys(t *testing.T) {
	/*
		var nodeConfig NodeConfig
		nodeConfig.NewKeys()

		publicKey1, err := hex.DecodeString(nodeConfig.PublicKey)

		if err != nil {
			t.Fatal("can not decode generated public key")
		}

		if len(publicKey1) == 0 {
			t.Fatal("empty public key generated")
		}

		privateKey1, err := hex.DecodeString(nodeConfig.PrivateKey)

		if err != nil {
			t.Fatal("can not decode generated private key")
		}

		if len(privateKey1) == 0 {
			t.Fatal("empty private key generated")
		}

		nodeConfig.NewKeys()

		publicKey2, err := hex.DecodeString(nodeConfig.PublicKey)

		if err != nil {
			t.Fatal("can not decode generated public key")
		}

		if bytes.Equal(publicKey2, publicKey1) {
			t.Fatal("same public key generated")
		}

		privateKey2, err := hex.DecodeString(nodeConfig.PrivateKey)

		if err != nil {
			t.Fatal("can not decode generated private key")
		}

		if bytes.Equal(privateKey2, privateKey1) {
			t.Fatal("same private key generated")
		}
	*/
}

func TestGenerateConfigAutoPeerDefaults(t *testing.T) {
	cfg := GenerateConfig()

	if cfg.AutoPeer.Enabled {
		t.Fatal("autopeer should be disabled by default")
	}
	if len(cfg.AutoPeer.Sources) != 1 || cfg.AutoPeer.Sources[0] != autopeer.BuiltinSource {
		t.Fatalf("unexpected autopeer sources: %#v", cfg.AutoPeer.Sources)
	}
	if cfg.AutoPeer.FetchInterval != "1h" {
		t.Fatalf("unexpected fetch interval %q", cfg.AutoPeer.FetchInterval)
	}
	if cfg.AutoPeer.CheckInterval != "1m" {
		t.Fatalf("unexpected check interval %q", cfg.AutoPeer.CheckInterval)
	}
}

func TestExampleConfigIncludesAutoPeer(t *testing.T) {
	examplePath := filepath.Join("..", "..", "example.conf")
	f, err := os.Open(examplePath)
	if err != nil {
		t.Fatalf("open example.conf: %v", err)
	}
	defer f.Close()

	var cfg NodeConfig
	if _, err := cfg.ReadFrom(f); err != nil {
		t.Fatalf("parse example.conf: %v", err)
	}

	if cfg.AutoPeer.Enabled {
		t.Fatal("expected example autopeer to be disabled")
	}
	if len(cfg.AutoPeer.Sources) != 1 || cfg.AutoPeer.Sources[0] != autopeer.BuiltinSource {
		t.Fatalf("unexpected example autopeer sources: %#v", cfg.AutoPeer.Sources)
	}
	if cfg.AutoPeer.FetchInterval != "1h" || cfg.AutoPeer.CheckInterval != "1m" {
		t.Fatalf("unexpected example autopeer intervals: fetch=%q check=%q", cfg.AutoPeer.FetchInterval, cfg.AutoPeer.CheckInterval)
	}
	if cfg.Transport.DefaultNetwork.Name() != "native" || cfg.Transport.DefaultNetwork.IsNull() {
		t.Fatalf("unexpected example default transport network: %#v", cfg.Transport.DefaultNetwork)
	}
	assertDefaultTransportMappings(t, cfg.Transport.NetworkMappings)
}

func TestAutoPeerConfigRejectsInvalidDuration(t *testing.T) {
	cfg := GenerateConfig()
	cfg.AutoPeer.FetchInterval = "nope"
	if err := cfg.postprocessConfig(); err == nil {
		t.Fatal("expected invalid autopeer fetch interval to fail")
	}
}

func TestGenerateConfigTransportDefaults(t *testing.T) {
	cfg := GenerateConfig()

	if !cfg.Transport.DefaultNetwork.IsSet() {
		t.Fatal("expected default transport network to be set")
	}
	if cfg.Transport.DefaultNetwork.IsNull() {
		t.Fatal("expected default transport network to be non-null")
	}
	if cfg.Transport.DefaultNetwork.Name() != "native" {
		t.Fatalf("unexpected default transport network %q", cfg.Transport.DefaultNetwork.Name())
	}
	assertDefaultTransportMappings(t, cfg.Transport.NetworkMappings)
}

func TestTransportConfigPreservesExplicitNullAndMappings(t *testing.T) {
	const raw = `{
		Transport: {
			DefaultNetwork: null
			NetworkMappings: {
				"*.example": native
				"disabled.example": null
			}
		}
	}`

	cfg := GenerateConfig()
	if err := cfg.UnmarshalHJSON([]byte(raw)); err != nil {
		t.Fatalf("unmarshal transport config: %v", err)
	}

	if !cfg.Transport.DefaultNetwork.IsNull() {
		t.Fatal("expected explicit null default transport network")
	}
	if got := cfg.Transport.NetworkMappings["*.example"].Name(); got != "native" {
		t.Fatalf("unexpected mapped network %q", got)
	}
	if !cfg.Transport.NetworkMappings["disabled.example"].IsNull() {
		t.Fatal("expected explicit null mapped network")
	}
}

func TestTransportConfigSupportsSocksObjects(t *testing.T) {
	const raw = `{
		Transport: {
			DefaultNetwork: {
				Type: socks
				ProxyURL: "socks5://proxy.internal:1080"
			}
			NetworkMappings: {
				"*.onion": {
					Type: socks
					ProxyURL: "socks5://tor-proxy:9050"
				}
			}
		}
	}`

	cfg := GenerateConfig()
	if err := cfg.UnmarshalHJSON([]byte(raw)); err != nil {
		t.Fatalf("unmarshal transport config: %v", err)
	}

	if got := cfg.Transport.DefaultNetwork.Name(); got != "socks" {
		t.Fatalf("unexpected default transport network %q", got)
	}
	if got := cfg.Transport.DefaultNetwork.ProxyURL(); got != "socks5://proxy.internal:1080" {
		t.Fatalf("unexpected default proxy url %q", got)
	}
	if got := cfg.Transport.NetworkMappings["*.onion"].Name(); got != "socks" {
		t.Fatalf("unexpected mapped network %q", got)
	}
	if got := cfg.Transport.NetworkMappings["*.onion"].ProxyURL(); got != "socks5://tor-proxy:9050" {
		t.Fatalf("unexpected mapped proxy url %q", got)
	}
}
