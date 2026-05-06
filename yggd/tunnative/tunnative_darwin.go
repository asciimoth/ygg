//go:build darwin || ios

package tunnative

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"golang.org/x/sys/unix"
)

func create(log Logger, cfg Config) (gtun.Tun, error) {
	ifname := cfg.Name
	if ifname == "auto" {
		ifname = "utun"
	}
	var (
		device gtun.Tun
		err    error
	)
	if cfg.FD > 0 {
		dfd, derr := unix.Dup(int(cfg.FD))
		if derr != nil {
			return nil, fmt.Errorf("failed to duplicate FD: %w", derr)
		}
		if err = unix.SetNonblock(dfd, true); err != nil {
			unix.Close(dfd)
			return nil, fmt.Errorf("failed to set FD as non-blocking: %w", err)
		}
		device, err = tuntap.CreateTUNFromFile(os.NewFile(uintptr(dfd), "/dev/tun"), int(cfg.MTU))
	} else {
		device, err = tuntap.CreateTUN(ifname, int(cfg.MTU))
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN: %w", err)
	}
	if cfg.Address != "" {
		if err := configureAddress(log, device, cfg.Address); err != nil {
			_ = device.Close()
			return nil, err
		}
	}
	return device, nil
}

const (
	darwinSIOCAIFADDRIN6      = 2155899162
	darwinIN6IFFNODAD         = 0x0020
	darwinIN6IFFSECURED       = 0x0400
	darwinND6InfiniteLifetime = 0xFFFFFFFF
)

type in6Addrlifetime struct {
	ia6tExpire    float64
	ia6tPreferred float64
	ia6tVltime    uint32
	ia6tPltime    uint32
}

type sockaddrIn6 struct {
	sin6Len      uint8
	sin6Family   uint8
	sin6Port     uint8
	sin6Flowinfo uint32
	sin6Addr     [8]uint16
	sin6ScopeID  uint32
}

type in6Aliasreq struct {
	ifraName       [16]byte
	ifraAddr       sockaddrIn6
	ifraDstaddr    sockaddrIn6
	ifraPrefixmask sockaddrIn6
	ifraFlags      uint32
	ifraLifetime   in6Addrlifetime
}

type ifreqDarwin struct {
	ifrName [16]byte
	ifruMTU uint32
}

func configureAddress(log Logger, device gtun.Tun, addr string) error {
	name, err := device.Name()
	if err != nil {
		return fmt.Errorf("failed to read TUN name: %w", err)
	}
	mtu, err := device.MTU()
	if err != nil {
		return fmt.Errorf("failed to read TUN mtu: %w", err)
	}
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM, 0)
	if err != nil {
		log.Errf("Create AF_SYSTEM socket failed: %v.", err)
		return fmt.Errorf("failed to open AF_SYSTEM: %w", err)
	}
	defer unix.Close(fd)

	var ar in6Aliasreq
	copy(ar.ifraName[:], name)

	ar.ifraPrefixmask.sin6Len = uint8(unsafe.Sizeof(ar.ifraPrefixmask))
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b, uint16(0xFE00))
	ar.ifraPrefixmask.sin6Addr[0] = binary.BigEndian.Uint16(b)

	ar.ifraAddr.sin6Len = uint8(unsafe.Sizeof(ar.ifraAddr))
	ar.ifraAddr.sin6Family = unix.AF_INET6
	parts := strings.Split(strings.Split(addr, "/")[0], ":")
	for i := 0; i < 8; i++ {
		part, _ := strconv.ParseUint(parts[i], 16, 16)
		b := make([]byte, 16)
		binary.LittleEndian.PutUint16(b, uint16(part))
		ar.ifraAddr.sin6Addr[i] = binary.BigEndian.Uint16(b)
	}

	ar.ifraFlags |= darwinIN6IFFNODAD
	ar.ifraFlags |= darwinIN6IFFSECURED
	ar.ifraLifetime.ia6tVltime = darwinND6InfiniteLifetime
	ar.ifraLifetime.ia6tPltime = darwinND6InfiniteLifetime

	var ir ifreqDarwin
	copy(ir.ifrName[:], name)
	ir.ifruMTU = uint32(mtu)

	log.Infof("Interface name: %s", name)
	log.Infof("Interface IPv6: %s", addr)
	log.Infof("Interface MTU: %d", ir.ifruMTU)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(darwinSIOCAIFADDRIN6), uintptr(unsafe.Pointer(&ar))); errno != 0 {
		log.Errf("Error in darwin_SIOCAIFADDR_IN6: %v", errno)
		return fmt.Errorf("failed to call SIOCAIFADDR_IN6: %w", errno)
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.SIOCSIFMTU), uintptr(unsafe.Pointer(&ir))); errno != 0 {
		log.Errf("Error in SIOCSIFMTU: %v", errno)
		return fmt.Errorf("failed to call SIOCSIFMTU: %w", errno)
	}
	return nil
}
