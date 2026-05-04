package transportcfg

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/native"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/transport"
)

const NetworkKindSocks = "socks"

type configuredNetwork interface {
	transport.Network
	TransportNetworkConfig() config.TransportNetworkConfig
}

type socksNetwork struct {
	client *socksgo.Client
	cfg    config.TransportNetworkConfig
}

func NetworkFromConfig(cfg config.TransportNetworkConfig) (transport.Network, error) {
	if cfg.IsNull() {
		return nil, nil
	}

	switch cfg.Name() {
	case transport.NetworkKindNative:
		network := &native.Network{}
		if err := network.Up(); err != nil {
			return nil, err
		}
		return network, nil
	case NetworkKindSocks:
		return newSocksNetwork(cfg)
	default:
		return nil, fmt.Errorf("%w: %q", transport.ErrUnsupportedNetwork, cfg.Name())
	}
}

func ConfigFromNetwork(network transport.Network) config.TransportNetworkConfig {
	if network == nil {
		return config.NullTransportNetworkConfig()
	}
	if configured, ok := network.(configuredNetwork); ok {
		return configured.TransportNetworkConfig()
	}
	if name, ok := transport.BuiltinNetworkName(network); ok {
		return config.NewTransportNetworkConfig(name)
	}
	return config.NewTransportNetworkConfig(strings.TrimPrefix(fmt.Sprintf("%T", network), "*"))
}

func newSocksNetwork(cfg config.TransportNetworkConfig) (transport.Network, error) {
	proxyURL := strings.TrimSpace(cfg.ProxyURL())
	if proxyURL == "" {
		return nil, fmt.Errorf("transport socks network requires proxy_url")
	}

	client, err := socksgo.ClientFromURL(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid socks proxy url %q: %w", proxyURL, err)
	}
	client.Filter = gonnect.FalseFilter

	return &socksNetwork{
		client: client,
		cfg:    config.NewSocksTransportNetworkConfig(proxyURL),
	}, nil
}

func (n *socksNetwork) TransportNetworkConfig() config.TransportNetworkConfig {
	return n.cfg
}

func (n *socksNetwork) IsNative() bool {
	return false
}

func (n *socksNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return n.client.Dial(ctx, network, address)
}

func (n *socksNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return n.client.Listen(ctx, network, address)
}

func (n *socksNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.client.PacketDial(ctx, network, address)
}

func (n *socksNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.client.ListenPacket(ctx, network, address)
}

func (n *socksNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	return n.client.DialTCP(ctx, network, laddr, raddr)
}

func (n *socksNetwork) ListenTCP(ctx context.Context, network, laddr string) (gonnect.TCPListener, error) {
	return n.client.ListenTCP(ctx, network, laddr)
}

func (n *socksNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	return n.client.DialUDP(ctx, network, laddr, raddr)
}

func (n *socksNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	return n.client.ListenUDP(ctx, network, laddr)
}
