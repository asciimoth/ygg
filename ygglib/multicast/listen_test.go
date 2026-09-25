package multicast

import (
	"crypto/ed25519"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"

	"github.com/asciimoth/gonnect"
)

type unexpectedPacketAddr struct{}

func (unexpectedPacketAddr) Network() string { return "test" }
func (unexpectedPacketAddr) String() string  { return "unexpected" }

type listenTestCore struct {
	public    ed25519.PublicKey
	peerCalls int
}

func (*listenTestCore) ListenLocal(*url.URL, string) (Listener, error) { return nil, nil }
func (c *listenTestCore) CallPeer(*url.URL, string) error {
	c.peerCalls++
	return nil
}
func (c *listenTestCore) PublicKey() ed25519.PublicKey { return c.public }

type listenTestLogger struct {
	sync.Mutex
	warnings int
}

func (*listenTestLogger) Debug(...any)          {}
func (*listenTestLogger) Debugf(string, ...any) {}
func (*listenTestLogger) Info(...any)           {}
func (*listenTestLogger) Infof(string, ...any)  {}
func (l *listenTestLogger) Warn(...any) {
	l.Lock()
	l.warnings++
	l.Unlock()
}
func (*listenTestLogger) Warnf(string, ...any)  {}
func (*listenTestLogger) Err(...any)            {}
func (*listenTestLogger) Errf(string, ...any)   {}
func (*listenTestLogger) Fatal(...any)          {}
func (*listenTestLogger) Fatalf(string, ...any) {}

type scriptedMulticastSocket struct {
	gonnect.MulticastPacketConn
	m     *Multicast
	data  []byte
	reads int
}

func (s *scriptedMulticastSocket) ReadFromControl(b []byte) (int, gonnect.ControlMessage, net.Addr, error) {
	s.reads++
	switch s.reads {
	case 1:
		return 0, gonnect.ControlMessage{}, nil, errors.New("temporary read failure")
	case 2:
		return copy(b, s.data), gonnect.ControlMessage{}, unexpectedPacketAddr{}, nil
	default:
		s.m.running.Store(false)
		return 0, gonnect.ControlMessage{}, nil, errors.New("socket stopped")
	}
}

func TestListenContinuesAfterReadErrorAndIgnoresUnexpectedAddress(t *testing.T) {
	localKey := make(ed25519.PublicKey, ed25519.PublicKeySize)
	remoteKey := make(ed25519.PublicKey, ed25519.PublicKeySize)
	remoteKey[0] = 1
	adv := multicastAdvertisement{
		MajorVersion: 1,
		MinorVersion: 2,
		PublicKey:    remoteKey,
		Port:         9001,
		Hash:         []byte("hash"),
	}
	data, err := adv.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal advertisement: %v", err)
	}

	core := &listenTestCore{public: localKey}
	log := &listenTestLogger{}
	m := &Multicast{
		core:        core,
		log:         log,
		_interfaces: make(map[string]*interfaceInfo),
	}
	m.config._groupAddr = GroupAddress("[ff02::114]:9001")
	m.config._protocolVersion = ProtocolVersion{Major: 1, Minor: 2}
	m.running.Store(true)
	sock := &scriptedMulticastSocket{m: m, data: data}
	m.sock = sock

	m.listen()

	if sock.reads != 3 {
		t.Fatalf("ReadFromControl() calls = %d, want 3", sock.reads)
	}
	if log.warnings != 1 {
		t.Fatalf("read warnings = %d, want 1", log.warnings)
	}
	if core.peerCalls != 0 {
		t.Fatalf("CallPeer() calls = %d, want 0", core.peerCalls)
	}
}
