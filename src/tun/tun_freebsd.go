//go:build freebsd

package tun

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	gtun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/tuntap"
	"golang.org/x/sys/unix"
)

const siocsIFAddrIN6 = (0x80000000) | ((288 & 0x1fff) << 16) | uint32(byte('i'))<<8 | 12

type sockaddrIn6FreeBSD struct {
	sin6Len      uint8
	sin6Family   uint8
	sin6Port     uint8
	sin6Flowinfo uint32
	sin6Addr     [8]uint16
	sin6ScopeID  uint32
}

type in6IfreqAddr struct {
	ifrName  [syscall.IFNAMSIZ]byte
	ifruAddr sockaddrIn6FreeBSD
}

func (tun *TunAdapter) createNativeTun(addr string, mtu uint64) (gtun.Tun, error) {
	device, err := tuntap.CreateTUN(string(tun.config.name), int(mtu))
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN: %w", err)
	}
	if addr != "" {
		if err := tun.configureAddress(device, addr, mtu); err != nil {
			_ = device.Close()
			return nil, err
		}
	}
	return device, nil
}

func (tun *TunAdapter) configureAddress(device gtun.Tun, addr string, mtu uint64) error {
	name, err := device.Name()
	if err != nil {
		return err
	}
	sfd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		tun.log.Printf("Create AF_INET socket failed: %v.", err)
		return err
	}
	defer unix.Close(sfd)

	tun.log.Infof("Interface name: %s", name)
	tun.log.Infof("Interface IPv6: %s", addr)
	tun.log.Infof("Interface MTU: %d", mtu)

	var ar in6IfreqAddr
	copy(ar.ifrName[:], name)
	ar.ifruAddr.sin6Len = uint8(unsafe.Sizeof(ar.ifruAddr))
	ar.ifruAddr.sin6Family = unix.AF_INET6
	parts := strings.Split(strings.Split(addr, "/")[0], ":")
	for i := 0; i < 8; i++ {
		part, _ := strconv.ParseUint(parts[i], 16, 16)
		b := make([]byte, 16)
		binary.LittleEndian.PutUint16(b, uint16(part))
		ar.ifruAddr.sin6Addr[i] = uint16(binary.BigEndian.Uint16(b))
	}

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(sfd), uintptr(siocsIFAddrIN6), uintptr(unsafe.Pointer(&ar))); errno != 0 {
		err = errno
		tun.log.Errorf("Error in SIOCSIFADDR_IN6: %v", errno)
		cmd := exec.Command("ifconfig", name, "inet6", addr)
		tun.log.Warnf("Using ifconfig as fallback: %v", strings.Join(cmd.Args, " "))
		output, cerr := cmd.CombinedOutput()
		if cerr != nil {
			tun.log.Errorf("SIOCSIFADDR_IN6 fallback failed: %v.", cerr)
			tun.log.Traceln(string(output))
		}
	}
	return nil
}
