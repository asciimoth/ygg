//go:build windows

package tun

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"time"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"github.com/asciimoth/ygg/src/config"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/elevate"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func (tun *TunAdapter) createNativeTun(addr string, mtu uint64) (gtun.Tun, error) {
	if tun.config.fd > 0 {
		return nil, fmt.Errorf("setup via FD not supported on this platform")
	}
	ifname := string(tun.config.name)
	if ifname == "auto" {
		ifname = config.GetDefaults().DefaultIfName
	}
	var device gtun.Tun
	err := elevate.DoAsSystem(func() error {
		var err error
		var guid windows.GUID
		if guid, err = windows.GUIDFromString("{8f59971a-7872-4aa6-b2eb-061fc4e9d0a7}"); err != nil {
			return err
		}
		tuntap.WintunStaticRequestedGUID = &guid
		device, err = tuntap.CreateTUN(ifname, int(mtu))
		if err != nil {
			tun.log.Printf("Error creating TUN: '%s'", err)
			wintun.Uninstall()
			time.Sleep(3 * time.Second)
			tun.log.Printf("Trying again")
			device, err = tuntap.CreateTUN(ifname, int(mtu))
			if err != nil {
				return err
			}
		}
		tun.log.Printf("Waiting for TUN to come up")
		time.Sleep(time.Second)
		if addr != "" {
			tun.log.Printf("Setting up address")
			if err = tun.configureAddress(device, addr); err != nil {
				tun.log.Errorln("Failed to set up TUN address:", err)
				return err
			}
		}
		if err = tun.configureMTU(device, getSupportedMTU(mtu)); err != nil {
			tun.log.Errorln("Failed to set up TUN MTU:", err)
			return err
		}
		tun.log.Printf("TUN is set up successfully")
		return nil
	})
	if err != nil {
		return nil, err
	}
	return device, nil
}

func (tun *TunAdapter) configureMTU(device gtun.Tun, mtu uint64) error {
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

func (tun *TunAdapter) configureAddress(device gtun.Tun, addr string) error {
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
		cleanupAddressesOnDisconnectedInterfaces(windows.AF_INET6, addresses)
		err = luid.SetIPAddressesForFamily(windows.AF_INET6, addresses)
	}
	return err
}

func cleanupAddressesOnDisconnectedInterfaces(family winipcfg.AddressFamily, addresses []netip.Prefix) {
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
				log.Printf("Cleaning up stale address %s from interface '%s'", prefix.String(), iface.FriendlyName())
				iface.LUID.DeleteIPAddress(prefix)
			}
		}
	}
}
