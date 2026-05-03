//go:build !linux && !android && !darwin && !windows && !openbsd && !freebsd

package tun

import (
	"fmt"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
)

func (tun *TunAdapter) createNativeTun(addr string, mtu uint64) (gtun.Tun, error) {
	device, err := tuntap.CreateTUN(string(tun.config.name), int(mtu))
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN: %w", err)
	}
	if addr != "" {
		tun.log.Warnln("Warning: Platform not supported, you must set the address of", mustName(device), "to", addr)
	}
	return device, nil
}

func mustName(device gtun.Tun) string {
	name, err := device.Name()
	if err != nil {
		return ""
	}
	return name
}
