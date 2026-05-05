//go:build linux || android

package tunnative

import (
	"fmt"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"github.com/vishvananda/netlink"
)

func create(log Logger, cfg Config) (gtun.Tun, error) {
	ifname := cfg.Name
	if ifname == "auto" {
		ifname = "\000"
	}
	device, err := tuntap.CreateTUN(ifname, int(cfg.MTU))
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN: %w", err)
	}
	if cfg.Address != "" {
		if err := configureAddress(log, device, cfg.Address, cfg.MTU); err != nil {
			_ = device.Close()
			return nil, err
		}
	}
	return device, nil
}

func configureAddress(log Logger, device gtun.Tun, addr string, mtu uint64) error {
	nladdr, err := netlink.ParseAddr(addr)
	if err != nil {
		return fmt.Errorf("couldn't parse address %q: %w", addr, err)
	}
	name, err := device.Name()
	if err != nil {
		return fmt.Errorf("failed to read link name: %w", err)
	}
	nlintf, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to find link by name: %w", err)
	}
	if err := netlink.AddrAdd(nlintf, nladdr); err != nil {
		return fmt.Errorf("failed to add address to link: %w", err)
	}
	effectiveMTU, err := device.MTU()
	if err != nil {
		effectiveMTU = int(mtu)
	}
	if err := netlink.LinkSetMTU(nlintf, effectiveMTU); err != nil {
		return fmt.Errorf("failed to set link MTU: %w", err)
	}
	if err := netlink.LinkSetUp(nlintf); err != nil {
		return fmt.Errorf("failed to bring link up: %w", err)
	}
	log.Infof("Interface name: %s", name)
	log.Infof("Interface IPv6: %s", addr)
	log.Infof("Interface MTU: %d", effectiveMTU)
	return nil
}
