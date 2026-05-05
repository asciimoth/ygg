package sockstun

import (
	"net"
	"testing"
)

func TestCreateStartsLocalSocksListener(t *testing.T) {
	tun, err := Create(Config{
		Name:    "sockstun-test",
		Listen:  "127.0.0.1:0",
		Address: net.ParseIP("200::1"),
		MTU:     1400,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	addr := tun.SocksAddr()
	if addr == nil {
		t.Fatal("expected socks listener address")
	}
	conn, err := net.Dial(addr.Network(), addr.String())
	if err != nil {
		t.Fatalf("dial socks listener: %v", err)
	}
	_ = conn.Close()

	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := net.Dial(addr.Network(), addr.String()); err == nil {
		t.Fatal("expected socks listener to be closed")
	}
}
