package outproxy

import (
	"net"
	"testing"
)

func TestYggdrasilListenAddressReplacesLocalHosts(t *testing.T) {
	addr := net.ParseIP("200:db8::1")
	tests := []struct {
		name   string
		listen string
		want   string
	}{
		{name: "loopback", listen: "127.0.0.1:1080", want: "[200:db8::1]:1080"},
		{name: "unspecified v4", listen: "0.0.0.0:1080", want: "[200:db8::1]:1080"},
		{name: "unspecified v6", listen: "[::]:1080", want: "[200:db8::1]:1080"},
		{name: "localhost", listen: "localhost:1080", want: "[200:db8::1]:1080"},
		{name: "explicit ygg", listen: "[200:db8::2]:2080", want: "[200:db8::2]:2080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := yggdrasilListenAddress(tt.listen, addr); got != tt.want {
				t.Fatalf("yggdrasilListenAddress(%q) = %q, want %q", tt.listen, got, tt.want)
			}
		})
	}
}
