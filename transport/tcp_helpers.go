package transport

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/helpers"
	"github.com/asciimoth/gonnect/sockopt"
)

type lookupIPNetwork interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

func unwrapNetwork(network Network) any {
	if network == nil {
		return nil
	}
	if wrapped := gonnect.GetWrapped(network); wrapped != nil {
		return wrapped
	}
	return network
}

func getInterface(network Network, name string) (gonnect.NetworkInterface, error) {
	if name == "" {
		return nil, nil
	}
	if ifn, ok := unwrapNetwork(network).(gonnect.InterfaceNetwork); ok {
		ifaces, err := ifn.InterfacesByName(name)
		if err != nil {
			return nil, err
		}
		if len(ifaces) == 0 {
			return nil, fmt.Errorf("interface %q not found", name)
		}
		return ifaces[0], nil
	}
	return nil, ErrSourceInterfaceUnsupported
}

func resolveTCPNetworkAndRemote(
	ctx context.Context,
	network Network,
	u *url.URL,
) (string, *net.TCPAddr, error) {
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", nil, err
	}

	if ip := net.ParseIP(host); ip != nil {
		networkName := "tcp6"
		if ip.To4() != nil {
			networkName = "tcp4"
		}
		return networkName, &net.TCPAddr{IP: ip, Port: port}, nil
	}

	if resolver, ok := unwrapNetwork(network).(lookupIPNetwork); ok {
		ips, err := resolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return "", nil, err
		}
		for _, ip := range ips {
			if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			networkName := "tcp6"
			if ip.To4() != nil {
				networkName = "tcp4"
			}
			return networkName, &net.TCPAddr{IP: ip, Port: port}, nil
		}
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return "", nil, err
	}
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		networkName := "tcp6"
		if ip.To4() != nil {
			networkName = "tcp4"
		}
		return networkName, &net.TCPAddr{IP: ip, Port: port}, nil
	}
	return "", nil, fmt.Errorf("host %q has no suitable IPs", host)
}

func localAddrForInterface(
	iface gonnect.NetworkInterface,
	remote *net.TCPAddr,
	sintf string,
) (*net.TCPAddr, error) {
	if remote == nil {
		return nil, fmt.Errorf("remote address is required")
	}
	remoteIP := remote.IP
	if remoteIP == nil {
		return nil, fmt.Errorf("remote IP is required")
	}
	if remoteIP.IsLinkLocalUnicast() && remote.Zone == "" {
		remote.Zone = sintf
	}

	addrs, err := iface.Addrs()
	if err != nil {
		if remoteIP.IsLinkLocalUnicast() && remote.Zone != "" {
			return nil, nil
		}
		return nil, fmt.Errorf("interface %q addresses not available: %w", iface.Name(), err)
	}

	for addrIndex, addr := range addrs {
		src, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			continue
		}
		if !src.IsGlobalUnicast() && !src.IsLinkLocalUnicast() {
			continue
		}
		bothGlobal := src.IsGlobalUnicast() == remoteIP.IsGlobalUnicast()
		bothLinkLocal := src.IsLinkLocalUnicast() == remoteIP.IsLinkLocalUnicast()
		if !bothGlobal && !bothLinkLocal {
			continue
		}
		if (src.To4() != nil) != (remoteIP.To4() != nil) {
			continue
		}
		if bothGlobal || bothLinkLocal || addrIndex == len(addrs)-1 {
			return &net.TCPAddr{
				IP:   src,
				Port: 0,
				Zone: sintf,
			}, nil
		}
	}

	if remoteIP.IsLinkLocalUnicast() && remote.Zone != "" {
		return nil, nil
	}
	return nil, fmt.Errorf("no suitable source address found on interface %q", iface.Name())
}

func resolveListenAddr(
	u *url.URL,
	iface gonnect.NetworkInterface,
) string {
	if iface == nil {
		return u.Host
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return u.Host
	}
	ip := net.ParseIP(host)
	if ip != nil && !ip.IsUnspecified() {
		if ip.IsLinkLocalUnicast() {
			return net.JoinHostPort(host+"%"+iface.Name(), port)
		}
		return u.Host
	}
	return net.JoinHostPort(host, port)
}

func tcpListen(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (net.Listener, error) {
	if opts.SourceInterface == "" {
		return network.Listen(ctx, "tcp", u.Host)
	}

	iface, err := getInterface(network, opts.SourceInterface)
	if err != nil {
		return network.Listen(ctx, "tcp", u.Host)
	}

	addr := resolveListenAddr(u, iface)
	listener, err := network.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if sockopt.CheckSupport().BindToInterface {
		_ = sockopt.SetBindToInterface(listener, iface)
	}
	return listener, nil
}

func tcpDial(
	ctx context.Context,
	network Network,
	u *url.URL,
	opts Options,
) (net.Conn, error) {
	if opts.SourceInterface == "" {
		return network.Dial(ctx, "tcp", u.Host)
	}

	iface, err := getInterface(network, opts.SourceInterface)
	if err != nil {
		return network.Dial(ctx, "tcp", u.Host)
	}

	networkName, remote, err := resolveTCPNetworkAndRemote(ctx, network, u)
	if err != nil {
		return network.Dial(ctx, "tcp", u.Host)
	}

	dialTCP, ok := unwrapNetwork(network).(interface {
		DialTCP(
			ctx context.Context,
			network, laddr, raddr string,
		) (gonnect.TCPConn, error)
	})
	if !ok {
		return network.Dial(ctx, "tcp", u.Host)
	}

	local, err := localAddrForInterface(iface, remote, opts.SourceInterface)
	if err != nil {
		return network.Dial(ctx, "tcp", u.Host)
	}
	localAddr := ""
	if local != nil {
		localAddr = local.String()
	}
	remoteAddr := helpers.JointIPPort(remote.IP, remote.Port)
	return dialTCP.DialTCP(ctx, networkName, localAddr, remoteAddr)
}
