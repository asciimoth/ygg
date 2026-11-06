package multicast

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/asciimoth/gonnect"
)

type recordingMulticastNetwork struct {
	*gonnect.RejectNetwork
	called           bool
	network, address string
	opts             gonnect.MulticastOptions
}

func (n *recordingMulticastNetwork) ListenMulticastUDP(
	_ context.Context, network, address string, opts gonnect.MulticastOptions,
) (gonnect.MulticastPacketConn, error) {
	n.called = true
	n.network, n.address, n.opts = network, address, opts
	return nil, errRecordedMulticastListen
}

var errRecordedMulticastListen = errors.New("multicast reached selected network")

func TestStartUsesSelectedNetworkWithoutUnsupportedOption(t *testing.T) {
	n := &recordingMulticastNetwork{}
	_, err := New(
		&listenTestCore{},
		&listenTestLogger{},
		Network{Network: n},
		ProtocolVersion{Major: 1, Minor: 2},
		MulticastInterface{Regex: regexp.MustCompile(".*"), Listen: true},
	)
	if !errors.Is(err, errRecordedMulticastListen) {
		t.Fatalf("start error = %v, want selected network error", err)
	}
	if !n.called || n.network != "udp6" || n.address != "[::]:9001" {
		t.Fatalf("ListenMulticastUDP called=%v network=%q address=%q", n.called, n.network, n.address)
	}
	if n.opts.RecvAnyIf || !n.opts.ReuseAddr || !n.opts.ReusePort || n.opts.ControlFlags&gonnect.ControlDst == 0 {
		t.Fatalf("multicast options = %+v", n.opts)
	}
}
