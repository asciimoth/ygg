package tun

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

func TestFirewallAllowsICMPAndBlocksUnknownTraffic(t *testing.T) {
	fw := NewFirewall(FirewallConfig{Enabled: true})
	now := time.Now()

	if !fw.AllowIncoming(testIPv6Packet(ipProtoICMPv6, 0, 0, 0), now) {
		t.Fatal("ICMPv6 should always be allowed inbound")
	}
	if !fw.AllowOutgoing(testIPv6Packet(ipProtoICMPv6, 0, 0, 0), now) {
		t.Fatal("ICMPv6 should always be allowed outbound")
	}
	if fw.AllowIncoming(testIPv6Packet(132, 0, 0, 0), now) {
		t.Fatal("unknown inbound protocol should be blocked")
	}
	if fw.AllowOutgoing(testIPv6Packet(132, 0, 0, 0), now) {
		t.Fatal("unknown outbound protocol should be blocked")
	}
	if fw.AllowIncoming([]byte{0x45, 0, 0, 0}, now) {
		t.Fatal("non-IPv6 packet should be blocked")
	}
}

func TestFirewallTracksOutboundTCPAndUDPReturnFlows(t *testing.T) {
	fw := NewFirewall(FirewallConfig{Enabled: true})
	now := time.Now()

	outTCP := testIPv6Packet(ipProtoTCP, 41000, 443, 0x02)
	inTCP := testReverseIPv6Packet(ipProtoTCP, 443, 41000, 0x12)
	if !fw.AllowOutgoing(outTCP, now) {
		t.Fatal("outbound TCP should be allowed")
	}
	if !fw.AllowIncoming(inTCP, now.Add(time.Second)) {
		t.Fatal("return TCP flow should be allowed")
	}

	outUDP := testIPv6Packet(ipProtoUDP, 41001, 53, 0)
	inUDP := testReverseIPv6Packet(ipProtoUDP, 53, 41001, 0)
	if !fw.AllowOutgoing(outUDP, now) {
		t.Fatal("outbound UDP should be allowed")
	}
	if !fw.AllowIncoming(inUDP, now.Add(time.Second)) {
		t.Fatal("return UDP flow should be allowed")
	}
	if fw.AllowIncoming(inUDP, now.Add(udpFlowTTL+time.Second)) {
		t.Fatal("expired UDP flow should be blocked")
	}
}

func TestFirewallAllowsOnlyConfiguredUnsolicitedPorts(t *testing.T) {
	fw := NewFirewall(FirewallConfig{
		Enabled:         true,
		AllowedTCPPorts: []uint16{443, 22, 22},
		AllowedUDPPorts: []uint16{53},
	})
	now := time.Now()

	if !fw.AllowIncoming(testIPv6Packet(ipProtoTCP, 50000, 22, 0x02), now) {
		t.Fatal("configured TCP port should be allowed")
	}
	if fw.AllowIncoming(testIPv6Packet(ipProtoTCP, 50000, 23, 0x02), now) {
		t.Fatal("unconfigured TCP port should be blocked")
	}
	if !fw.AllowIncoming(testIPv6Packet(ipProtoUDP, 50000, 53, 0), now) {
		t.Fatal("configured UDP port should be allowed")
	}
	if fw.AllowIncoming(testIPv6Packet(ipProtoUDP, 50000, 5353, 0), now) {
		t.Fatal("unconfigured UDP port should be blocked")
	}

	status := fw.Status()
	if len(status.AllowedTCPPorts) != 2 || status.AllowedTCPPorts[0] != 22 || status.AllowedTCPPorts[1] != 443 {
		t.Fatalf("TCP ports not sorted/deduplicated: %#v", status.AllowedTCPPorts)
	}
}

func TestFirewallDisabledPassesMalformedPackets(t *testing.T) {
	fw := NewFirewall(FirewallConfig{Enabled: false})
	if !fw.AllowIncoming([]byte{1, 2, 3}, time.Now()) {
		t.Fatal("disabled firewall should pass inbound packets")
	}
	if !fw.AllowOutgoing([]byte{1, 2, 3}, time.Now()) {
		t.Fatal("disabled firewall should pass outbound packets")
	}
}

func TestFirewallParsesExtensionHeadersAndDropsFragments(t *testing.T) {
	fw := NewFirewall(FirewallConfig{Enabled: true, AllowedTCPPorts: []uint16{80}})
	now := time.Now()

	withOpts := testIPv6PacketWithExtension(ipProtoDstOpts, ipProtoTCP, 50000, 80, 0x02)
	if !fw.AllowIncoming(withOpts, now) {
		t.Fatal("TCP behind destination options should be parsed")
	}

	fragment := testIPv6Packet(ipProtoTCP, 50000, 80, 0x02)
	fragment[6] = ipProtoFragment
	payload := append([]byte{ipProtoTCP, 0, 0, 1, 0, 0, 0, 0}, fragment[40:]...)
	fragment = append(fragment[:40], payload...)
	binary.BigEndian.PutUint16(fragment[4:6], uint16(len(payload)))
	if fw.AllowIncoming(fragment, now) {
		t.Fatal("fragmented TCP should be blocked")
	}
}

func TestFirewallConcurrentAccess(t *testing.T) {
	fw := NewFirewall(FirewallConfig{Enabled: true})
	packet := testIPv6Packet(ipProtoUDP, 10000, 10001, 0)
	reverse := testReverseIPv6Packet(ipProtoUDP, 10001, 10000, 0)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if i%3 == 0 {
					fw.SetConfig(FirewallConfig{Enabled: true, AllowedUDPPorts: []uint16{uint16(10001 + j%3)}})
					continue
				}
				now := time.Now()
				_ = fw.AllowOutgoing(packet, now)
				_ = fw.AllowIncoming(reverse, now)
				_ = fw.Status()
			}
		}(i)
	}
	wg.Wait()
}

func testIPv6Packet(proto uint8, srcPort, dstPort uint16, tcpFlags uint8) []byte {
	src := net.ParseIP("300::1").To16()
	dst := net.ParseIP("300::2").To16()
	return buildIPv6Packet(src, dst, proto, srcPort, dstPort, tcpFlags)
}

func testReverseIPv6Packet(proto uint8, srcPort, dstPort uint16, tcpFlags uint8) []byte {
	src := net.ParseIP("300::2").To16()
	dst := net.ParseIP("300::1").To16()
	return buildIPv6Packet(src, dst, proto, srcPort, dstPort, tcpFlags)
}

func testIPv6PacketWithExtension(extProto, finalProto uint8, srcPort, dstPort uint16, tcpFlags uint8) []byte {
	packet := testIPv6Packet(finalProto, srcPort, dstPort, tcpFlags)
	ext := []byte{finalProto, 0, 0, 0, 0, 0, 0, 0}
	payload := append(ext, packet[40:]...)
	packet[6] = extProto
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(payload)))
	return append(packet[:40], payload...)
}

func buildIPv6Packet(src, dst []byte, proto uint8, srcPort, dstPort uint16, tcpFlags uint8) []byte {
	payloadLen := 0
	switch proto {
	case ipProtoTCP:
		payloadLen = 20
	case ipProtoUDP:
		payloadLen = 8
	case ipProtoICMPv6:
		payloadLen = 8
	default:
		payloadLen = 4
	}
	packet := make([]byte, 40+payloadLen)
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], uint16(payloadLen))
	packet[6] = proto
	packet[7] = 64
	copy(packet[8:24], src)
	copy(packet[24:40], dst)
	if proto == ipProtoTCP || proto == ipProtoUDP {
		binary.BigEndian.PutUint16(packet[40:42], srcPort)
		binary.BigEndian.PutUint16(packet[42:44], dstPort)
	}
	if proto == ipProtoTCP {
		packet[52] = 5 << 4
		packet[53] = tcpFlags
	}
	if proto == ipProtoUDP {
		binary.BigEndian.PutUint16(packet[44:46], uint16(payloadLen))
	}
	return packet
}
