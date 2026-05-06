//go:build !linux && !android && !darwin && !windows && !openbsd && !freebsd

package tunnative

import (
	"fmt"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
)

func create(log Logger, cfg Config) (gtun.Tun, error) {
	device, err := tuntap.CreateTUN(cfg.Name, int(cfg.MTU))
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN: %w", err)
	}
	if cfg.Address != "" {
		log.Warn("Warning: Platform not supported, you must set the address of", mustName(device), "to", cfg.Address)
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
