package transportcfg

import (
	"testing"

	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/transport"
)

func TestNetworkFromConfigSocksForcesProxyAll(t *testing.T) {
	network, err := NetworkFromConfig(config.NewSocksTransportNetworkConfig("socks5://proxy.internal:1080"))
	if err != nil {
		t.Fatalf("NetworkFromConfig(socks): %v", err)
	}

	socksNet, ok := network.(*socksNetwork)
	if !ok {
		t.Fatalf("unexpected network type %T", network)
	}
	if socksNet.client.DoFilter("tcp", "127.0.0.1:1234") {
		t.Fatal("expected localhost traffic to be proxied")
	}
	if got := ConfigFromNetwork(network); got.Name() != NetworkKindSocks || got.ProxyURL() != "socks5://proxy.internal:1080" {
		t.Fatalf("unexpected round-tripped config: %#v", got)
	}
}

func TestNetworkFromConfigNative(t *testing.T) {
	network, err := NetworkFromConfig(config.NewTransportNetworkConfig(transport.NetworkKindNative))
	if err != nil {
		t.Fatalf("NetworkFromConfig(native): %v", err)
	}
	if got := ConfigFromNetwork(network); got.Name() != transport.NetworkKindNative {
		t.Fatalf("unexpected round-tripped config: %#v", got)
	}
}
