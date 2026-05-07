//go:build windows

package tunnative

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/elevate"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func create(log Logger, cfg Config) (gtun.Tun, error) {
	if cfg.FD > 0 {
		return nil, fmt.Errorf("setup via FD not supported on this platform")
	}
	ifname := cfg.Name
	if ifname == "auto" {
		ifname = "Yggdrasil"
	}
	var device gtun.Tun
	err := elevate.DoAsSystem(func() error {
		var err error
		var guid windows.GUID
		if guid, err = windows.GUIDFromString("{8f59971a-7872-4aa6-b2eb-061fc4e9d0a7}"); err != nil {
			return err
		}
		tuntap.WintunStaticRequestedGUID = &guid
		device, err = tuntap.CreateTUN(ifname, int(cfg.MTU))
		if err != nil {
			log.Infof("Error creating TUN: '%s'", err)
			wintun.Uninstall()
			time.Sleep(3 * time.Second)
			log.Infof("Trying again")
			device, err = tuntap.CreateTUN(ifname, int(cfg.MTU))
			if err != nil {
				return err
			}
		}
		log.Infof("Waiting for TUN to come up")
		time.Sleep(time.Second)
		if cfg.Address != "" {
			log.Infof("Setting up address")
			if err = configureAddress(log, device, cfg.Address); err != nil {
				log.Err("Failed to set up TUN address:", err)
				return err
			}
		}
		if err = configureMTU(log, device, cfg.MTU); err != nil {
			log.Err("Failed to set up TUN MTU:", err)
			return err
		}
		log.Infof("TUN is set up successfully")
		return nil
	})
	if err != nil {
		return nil, err
	}
	return device, nil
}

func configureMTU(log Logger, device gtun.Tun, mtu uint64) error {
	name, err := device.Name()
	if err != nil || name == "" {
		return errors.New("can't configure MTU as TUN adapter is not present")
	}
	intf, ok := device.(*tuntap.NativeTun)
	if !ok {
		return errors.New("unable to get NativeTUN")
	}
	luid := winipcfg.LUID(intf.LUID())
	ipfamily, err := luid.IPInterface(windows.AF_INET6)
	if err != nil {
		return err
	}

	ipfamily.NLMTU = uint32(mtu)
	intf.ForceMTU(int(ipfamily.NLMTU))
	ipfamily.UseAutomaticMetric = false
	ipfamily.Metric = 0
	ipfamily.DadTransmits = 0
	ipfamily.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled

	return ipfamily.Set()
}

func configureAddress(log Logger, device gtun.Tun, addr string) error {
	name, err := device.Name()
	if err != nil || name == "" {
		return errors.New("can't configure IPv6 address as TUN adapter is not present")
	}
	intf, ok := device.(*tuntap.NativeTun)
	if !ok {
		return errors.New("unable to get NativeTUN")
	}
	ipnet, err := netip.ParsePrefix(addr)
	if err != nil {
		return err
	}
	luid := winipcfg.LUID(intf.LUID())
	addresses := []netip.Prefix{ipnet}
	err = luid.SetIPAddressesForFamily(windows.AF_INET6, addresses)
	if err == windows.ERROR_OBJECT_ALREADY_EXISTS {
		cleanupAddressesOnDisconnectedInterfaces(log, windows.AF_INET6, addresses)
		err = luid.SetIPAddressesForFamily(windows.AF_INET6, addresses)
	}
	return err
}

func cleanupAddressesOnDisconnectedInterfaces(log Logger, family winipcfg.AddressFamily, addresses []netip.Prefix) {
	if len(addresses) == 0 {
		return
	}
	addrHash := make(map[netip.Addr]bool, len(addresses))
	for i := range addresses {
		addrHash[addresses[i].Addr()] = true
	}
	interfaces, err := winipcfg.GetAdaptersAddresses(family, winipcfg.GAAFlagDefault)
	if err != nil {
		return
	}
	for _, iface := range interfaces {
		if iface.OperStatus == winipcfg.IfOperStatusUp {
			continue
		}
		for address := iface.FirstUnicastAddress; address != nil; address = address.Next {
			if ip, _ := netip.AddrFromSlice(address.Address.IP()); addrHash[ip] {
				prefix := netip.PrefixFrom(ip, int(address.OnLinkPrefixLength))
				log.Infof("Cleaning up stale address %s from interface '%s'", prefix.String(), iface.FriendlyName())
				iface.LUID.DeleteIPAddress(prefix)
			}
		}
	}
}
