//go:build openbsd

package tun

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"golang.org/x/sys/unix"
)

const (
	siocAIFAddrIN6      = 0x8080691a
	nd6InfiniteLifetime = 0xffffffff
)

type in6AddrlifetimeOpenBSD struct {
	ia6tExpire    int64
	ia6tPreferred int64
	ia6tVltime    uint32
	ia6tPltime    uint32
}

type in6Addr [16]uint8

type sockaddrIn6OpenBSD struct {
	sin6Len      uint8
	sin6Family   uint8
	sin6Port     uint16
	sin6Flowinfo uint32
	sin6Addr     in6Addr
	sin6ScopeID  uint32
}

func (sa6 *sockaddrIn6OpenBSD) setSockaddr(addr []byte) {
	sa6.sin6Len = uint8(unsafe.Sizeof(*sa6))
	sa6.sin6Family = unix.AF_INET6
	for i := range sa6.sin6Addr {
		sa6.sin6Addr[i] = addr[i]
	}
}

type in6AliasreqOpenBSD struct {
	ifraName       [syscall.IFNAMSIZ]byte
	ifraAddr       sockaddrIn6OpenBSD
	ifraDstaddr    sockaddrIn6OpenBSD
	ifraPrefixmask sockaddrIn6OpenBSD
	ifraFlags      int32
	ifraLifetime   in6AddrlifetimeOpenBSD
}

func (tun *TunAdapter) createNativeTun(addr string, mtu uint64) (gtun.Tun, error) {
	device, err := tuntap.CreateTUN(string(tun.config.name), int(mtu))
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN: %w", err)
	}
	if addr != "" {
		if err := tun.configureAddress(device, addr); err != nil {
			_ = device.Close()
			return nil, err
		}
	}
	return device, nil
}

func (tun *TunAdapter) configureAddress(device gtun.Tun, addr string) error {
	name, err := device.Name()
	if err != nil {
		return err
	}
	mtu, err := device.MTU()
	if err != nil {
		return err
	}
	ip, prefix, err := net.ParseCIDR(addr)
	if err != nil {
		tun.log.Errorf("Error in ParseCIDR: %v", err)
		return err
	}
	sfd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM, 0)
	if err != nil {
		tun.log.Printf("Create AF_INET6 socket failed: %v", err)
		return err
	}
	defer unix.Close(sfd)

	tun.log.Infof("Interface name: %s", name)
	tun.log.Infof("Interface IPv6: %s", addr)
	tun.log.Infof("Interface MTU: %d", mtu)

	var ar in6AliasreqOpenBSD
	copy(ar.ifraName[:], name)
	ar.ifraAddr.setSockaddr(ip)
	prefixmask := net.CIDRMask(prefix.Mask.Size())
	ar.ifraPrefixmask.setSockaddr(prefixmask)
	ar.ifraLifetime.ia6tVltime = nd6InfiniteLifetime
	ar.ifraLifetime.ia6tPltime = nd6InfiniteLifetime

	if err = unix.IoctlSetInt(sfd, siocAIFAddrIN6, int(uintptr(unsafe.Pointer(&ar)))); err != nil {
		tun.log.Errorf("Error in SIOCAIFADDR_IN6: %v", err)
		return err
	}
	return nil
}
