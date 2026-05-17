package transportcfg

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/ygg/ygglib/config"
	"github.com/asciimoth/ygg/ygglib/transport"
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
		network := gonnect.DetachNetwork(gonnect.NativeConfig{}.Build())
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

func (n *socksNetwork) ListenPacketConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.PacketConn, error) {
	return n.client.ListenPacketConfig(ctx, lc, network, address)
}

func (n *socksNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, laddr string) (gonnect.UDPConn, error) {
	return n.client.ListenUDPConfig(ctx, lc, network, laddr)
}

func (n *socksNetwork) ListenMulticastUDP(ctx context.Context, network, address string, opts gonnect.MulticastOptions) (gonnect.MulticastPacketConn, error) {
	return n.client.ListenMulticastUDP(ctx, network, address, opts)
}

func (n *socksNetwork) Interfaces() ([]gonnect.NetworkInterface, error) {
	return n.client.Interfaces()
}

func (n *socksNetwork) InterfaceAddrs() ([]net.Addr, error) {
	return n.client.InterfaceAddrs()
}

func (n *socksNetwork) InterfaceMulticastAddrs() ([]net.Addr, error) {
	return n.client.InterfaceMulticastAddrs()
}

func (n *socksNetwork) InterfacesByIndex(index int) ([]gonnect.NetworkInterface, error) {
	return n.client.InterfacesByIndex(index)
}

func (n *socksNetwork) InterfacesByName(name string) ([]gonnect.NetworkInterface, error) {
	return n.client.InterfacesByName(name)
}

func (n *socksNetwork) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return n.client.LookupIP(ctx, network, host)
}

func (n *socksNetwork) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return n.client.LookupIPAddr(ctx, host)
}

func (n *socksNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return n.client.LookupNetIP(ctx, network, host)
}

func (n *socksNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
	return n.client.LookupHost(ctx, host)
}

func (n *socksNetwork) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	return n.client.LookupAddr(ctx, addr)
}

func (n *socksNetwork) LookupCNAME(ctx context.Context, host string) (string, error) {
	return n.client.LookupCNAME(ctx, host)
}

func (n *socksNetwork) LookupPort(ctx context.Context, network, service string) (int, error) {
	return n.client.LookupPort(ctx, network, service)
}

func (n *socksNetwork) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	return n.client.LookupNS(ctx, name)
}

func (n *socksNetwork) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return n.client.LookupMX(ctx, name)
}

func (n *socksNetwork) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return n.client.LookupSRV(ctx, service, proto, name)
}

func (n *socksNetwork) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return n.client.LookupTXT(ctx, name)
}
