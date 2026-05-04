package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/ygg/autopeer"
)

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
}

func TestAutoPeerConfigRejectsInvalidDuration(t *testing.T) {
	cfg := GenerateConfig()
	cfg.AutoPeer.FetchInterval = "nope"
	if err := cfg.postprocessConfig(); err == nil {
		t.Fatal("expected invalid autopeer fetch interval to fail")
	}
}
