package ipv6rwc

import "testing"

func TestWritePCRejectsMalformedPackets(t *testing.T) {
	tests := []struct {
		name    string
		packet  []byte
		wantErr string
	}{
		{name: "nil", packet: nil, wantErr: "empty packet"},
		{name: "empty", packet: []byte{}, wantErr: "empty packet"},
		{name: "one-byte IPv6", packet: []byte{0x60}, wantErr: "undersized IPv6 packet, length: 1"},
		{name: "short IPv6", packet: append([]byte{0x60}, make([]byte, 38)...), wantErr: "undersized IPv6 packet, length: 39"},
		{name: "short non-IPv6", packet: []byte{0x45}, wantErr: "not an IPv6 packet"},
		{name: "header-sized non-IPv6", packet: append([]byte{0x45}, make([]byte, 39)...), wantErr: "not an IPv6 packet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var store keyStore
			n, err := store.writePC(tt.packet)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("writePC() = (%d, %v), want (0, %q)", n, err, tt.wantErr)
			}
			if n != 0 {
				t.Fatalf("writePC() bytes = %d, want 0", n)
			}
		})
	}
}

func TestValidateIPv6PacketAcceptsCompleteHeader(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 0x60

	if err := validateIPv6Packet(packet); err != nil {
		t.Fatalf("validateIPv6Packet() error = %v", err)
	}
}

func TestWritePCContinuesWithValidIPv6Header(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 0x60

	_, err := (&keyStore{}).writePC(packet)
	if err == nil || err.Error() != "invalid destination address" {
		t.Fatalf("writePC() error = %v, want invalid destination address", err)
	}
}
