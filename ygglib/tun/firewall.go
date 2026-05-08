package tun

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"sync"
	"time"
)

const (
	ipProtoHopByHop     = 0
	ipProtoTCP          = 6
	ipProtoUDP          = 17
	ipProtoRouting      = 43
	ipProtoFragment     = 44
	ipProtoESP          = 50
	ipProtoAH           = 51
	ipProtoNoNextHeader = 59
	ipProtoICMPv6       = 58
	ipProtoDstOpts      = 60

	udpFlowTTL = 2 * time.Minute
	tcpFlowTTL = 10 * time.Minute
)

type FirewallConfig struct {
	Enabled         bool
	AllowedTCPPorts []uint16
	AllowedUDPPorts []uint16
}

type FirewallStatus struct {
	Enabled         bool     `json:"enabled"`
	AllowedTCPPorts []uint16 `json:"allowed_tcp_ports"`
	AllowedUDPPorts []uint16 `json:"allowed_udp_ports"`
	TrackedFlows    int      `json:"tracked_flows"`
}

type Firewall struct {
	mu              sync.RWMutex
	enabled         bool
	allowedTCPPorts map[uint16]struct{}
	allowedUDPPorts map[uint16]struct{}
	flows           map[flowKey]time.Time
}

type flowKey struct {
	proto   uint8
	src     netip.Addr
	dst     netip.Addr
	srcPort uint16
	dstPort uint16
}

type parsedPacket struct {
	src       netip.Addr
	dst       netip.Addr
	proto     uint8
	srcPort   uint16
	dstPort   uint16
	tcpFlags  uint8
	hasPorts  bool
	allowICMP bool
}

func NewFirewall(cfg FirewallConfig) *Firewall {
	fw := &Firewall{
		allowedTCPPorts: make(map[uint16]struct{}),
		allowedUDPPorts: make(map[uint16]struct{}),
		flows:           make(map[flowKey]time.Time),
	}
	fw.SetConfig(cfg)
	return fw
}

func (fw *Firewall) SetConfig(cfg FirewallConfig) {
	if fw == nil {
		return
	}
	tcpPorts := portsToSet(cfg.AllowedTCPPorts)
	udpPorts := portsToSet(cfg.AllowedUDPPorts)

	fw.mu.Lock()
	fw.enabled = cfg.Enabled
	fw.allowedTCPPorts = tcpPorts
	fw.allowedUDPPorts = udpPorts
	clear(fw.flows)
	fw.mu.Unlock()
}

func (fw *Firewall) Config() FirewallConfig {
	if fw == nil {
		return FirewallConfig{}
	}
	fw.mu.RLock()
	defer fw.mu.RUnlock()
	return FirewallConfig{
		Enabled:         fw.enabled,
		AllowedTCPPorts: sortedPorts(fw.allowedTCPPorts),
		AllowedUDPPorts: sortedPorts(fw.allowedUDPPorts),
	}
}

func (fw *Firewall) Status() FirewallStatus {
	if fw == nil {
		return FirewallStatus{}
	}
	fw.mu.RLock()
	defer fw.mu.RUnlock()
	return FirewallStatus{
		Enabled:         fw.enabled,
		AllowedTCPPorts: sortedPorts(fw.allowedTCPPorts),
		AllowedUDPPorts: sortedPorts(fw.allowedUDPPorts),
		TrackedFlows:    len(fw.flows),
	}
}

func (fw *Firewall) AllowOutgoing(packet []byte, now time.Time) bool {
	if fw == nil {
		return true
	}
	if !fw.isEnabled() {
		return true
	}
	parsed, ok := parseIPv6Packet(packet)
	if !ok {
		return false
	}
	if parsed.allowICMP {
		return true
	}
	if !parsed.hasPorts || (parsed.proto != ipProtoTCP && parsed.proto != ipProtoUDP) {
		return false
	}

	fw.mu.Lock()
	defer fw.mu.Unlock()
	if !fw.enabled {
		return true
	}
	fw.pruneLocked(now)
	fw.flows[reverseFlow(parsed)] = now.Add(flowTTL(parsed))
	return true
}

func (fw *Firewall) AllowIncoming(packet []byte, now time.Time) bool {
	if fw == nil {
		return true
	}
	if !fw.isEnabled() {
		return true
	}
	parsed, ok := parseIPv6Packet(packet)
	if !ok {
		return false
	}
	if parsed.allowICMP {
		return true
	}
	if !parsed.hasPorts || (parsed.proto != ipProtoTCP && parsed.proto != ipProtoUDP) {
		return false
	}

	fw.mu.Lock()
	defer fw.mu.Unlock()
	if !fw.enabled {
		return true
	}
	fw.pruneLocked(now)
	key := packetFlow(parsed)
	if expires, ok := fw.flows[key]; ok && now.Before(expires) {
		if parsed.proto == ipProtoTCP && parsed.tcpFlags&0x05 != 0 {
			delete(fw.flows, key)
		} else {
			fw.flows[key] = now.Add(flowTTL(parsed))
		}
		return true
	}
	switch parsed.proto {
	case ipProtoTCP:
		_, ok := fw.allowedTCPPorts[parsed.dstPort]
		return ok
	case ipProtoUDP:
		_, ok := fw.allowedUDPPorts[parsed.dstPort]
		return ok
	default:
		return false
	}
}

func (fw *Firewall) isEnabled() bool {
	fw.mu.RLock()
	defer fw.mu.RUnlock()
	return fw.enabled
}

func (fw *Firewall) pruneLocked(now time.Time) {
	for key, expires := range fw.flows {
		if !now.Before(expires) {
			delete(fw.flows, key)
		}
	}
}

func parseIPv6Packet(packet []byte) (parsedPacket, bool) {
	if len(packet) < 40 || packet[0]&0xf0 != 0x60 {
		return parsedPacket{}, false
	}
	payloadLen := int(binary.BigEndian.Uint16(packet[4:6]))
	if payloadLen > len(packet)-40 {
		return parsedPacket{}, false
	}
	var src, dst [16]byte
	copy(src[:], packet[8:24])
	copy(dst[:], packet[24:40])
	p := parsedPacket{
		src:   netip.AddrFrom16(src),
		dst:   netip.AddrFrom16(dst),
		proto: packet[6],
	}
	offset := 40
	limit := 40 + payloadLen
	for {
		switch p.proto {
		case ipProtoICMPv6:
			p.allowICMP = true
			return p, true
		case ipProtoTCP:
			if limit-offset < 20 {
				return parsedPacket{}, false
			}
			p.srcPort = binary.BigEndian.Uint16(packet[offset : offset+2])
			p.dstPort = binary.BigEndian.Uint16(packet[offset+2 : offset+4])
			p.tcpFlags = packet[offset+13]
			p.hasPorts = true
			return p, true
		case ipProtoUDP:
			if limit-offset < 8 {
				return parsedPacket{}, false
			}
			p.srcPort = binary.BigEndian.Uint16(packet[offset : offset+2])
			p.dstPort = binary.BigEndian.Uint16(packet[offset+2 : offset+4])
			p.hasPorts = true
			return p, true
		case ipProtoHopByHop, ipProtoRouting, ipProtoDstOpts:
			if limit-offset < 2 {
				return parsedPacket{}, false
			}
			next := packet[offset]
			headerLen := (int(packet[offset+1]) + 1) * 8
			if headerLen <= 0 || limit-offset < headerLen {
				return parsedPacket{}, false
			}
			p.proto = next
			offset += headerLen
		case ipProtoFragment:
			if limit-offset < 8 {
				return parsedPacket{}, false
			}
			next := packet[offset]
			fragment := binary.BigEndian.Uint16(packet[offset+2 : offset+4])
			fragmentOffset := fragment >> 3
			moreFragments := fragment&1 != 0
			if fragmentOffset != 0 || moreFragments {
				return parsedPacket{}, false
			}
			p.proto = next
			offset += 8
		case ipProtoAH:
			if limit-offset < 2 {
				return parsedPacket{}, false
			}
			next := packet[offset]
			headerLen := (int(packet[offset+1]) + 2) * 4
			if headerLen <= 0 || limit-offset < headerLen {
				return parsedPacket{}, false
			}
			p.proto = next
			offset += headerLen
		case ipProtoESP, ipProtoNoNextHeader:
			return p, true
		default:
			return p, true
		}
	}
}

func reverseFlow(p parsedPacket) flowKey {
	return flowKey{
		proto:   p.proto,
		src:     p.dst,
		dst:     p.src,
		srcPort: p.dstPort,
		dstPort: p.srcPort,
	}
}

func packetFlow(p parsedPacket) flowKey {
	return flowKey{
		proto:   p.proto,
		src:     p.src,
		dst:     p.dst,
		srcPort: p.srcPort,
		dstPort: p.dstPort,
	}
}

func flowTTL(p parsedPacket) time.Duration {
	if p.proto == ipProtoUDP {
		return udpFlowTTL
	}
	return tcpFlowTTL
}

func portsToSet(ports []uint16) map[uint16]struct{} {
	out := make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		out[port] = struct{}{}
	}
	return out
}

func sortedPorts(ports map[uint16]struct{}) []uint16 {
	out := make([]uint16, 0, len(ports))
	for port := range ports {
		out = append(out, port)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
