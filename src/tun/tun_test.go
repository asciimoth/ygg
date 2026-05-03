package tun

import (
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/gonnect-netstack/vtun"
	gtun "github.com/asciimoth/gonnect/tun"

	"github.com/asciimoth/ygg/src/address"
)

type testLogger struct{}

func (testLogger) Printf(string, ...interface{}) {}
func (testLogger) Println(...interface{})        {}
func (testLogger) Infof(string, ...interface{})  {}
func (testLogger) Infoln(...interface{})         {}
func (testLogger) Warnf(string, ...interface{})  {}
func (testLogger) Warnln(...interface{})         {}
func (testLogger) Errorf(string, ...interface{}) {}
func (testLogger) Errorln(...interface{})        {}
func (testLogger) Debugf(string, ...interface{}) {}
func (testLogger) Debugln(...interface{})        {}
func (testLogger) Traceln(...interface{})        {}

type fakeRWC struct {
	readCh chan []byte
	mu     sync.Mutex
	writes [][]byte
	mtu    uint64
}

func newFakeRWC() *fakeRWC {
	return &fakeRWC{
		readCh: make(chan []byte, 32),
		mtu:    9000,
	}
}

func (f *fakeRWC) Read(p []byte) (int, error) {
	packet, ok := <-f.readCh
	if !ok {
		return 0, io.EOF
	}
	return copy(p, packet), nil
}

func (f *fakeRWC) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (f *fakeRWC) Close() error             { close(f.readCh); return nil }
func (f *fakeRWC) Address() address.Address { return address.Address{} }
func (f *fakeRWC) Subnet() address.Subnet   { return address.Subnet{} }
func (f *fakeRWC) MaxMTU() uint64           { return 9000 }
func (f *fakeRWC) SetMTU(mtu uint64)        { f.mu.Lock(); f.mtu = mtu; f.mu.Unlock() }
func (f *fakeRWC) currentMTU() uint64       { f.mu.Lock(); defer f.mu.Unlock(); return f.mtu }
func (f *fakeRWC) writeCount() int          { f.mu.Lock(); defer f.mu.Unlock(); return len(f.writes) }
func (f *fakeRWC) lastWrite() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.writes[len(f.writes)-1]...)
}

type fakeTun struct {
	name    string
	mtu     int
	mwo     int
	mro     int
	batch   int
	events  chan gtun.Event
	readCh  chan []byte
	writeCh chan []byte
	closeCh chan struct{}
	once    sync.Once
}

func newFakeTun(name string, mtu, mwo, mro int) *fakeTun {
	events := make(chan gtun.Event, 8)
	events <- gtun.EventUp
	return &fakeTun{
		name:    name,
		mtu:     mtu,
		mwo:     mwo,
		mro:     mro,
		batch:   1,
		events:  events,
		readCh:  make(chan []byte, 8),
		writeCh: make(chan []byte, 8),
		closeCh: make(chan struct{}),
	}
}

func (f *fakeTun) File() *os.File            { return nil }
func (f *fakeTun) MWO() int                  { return f.mwo }
func (f *fakeTun) MRO() int                  { return f.mro }
func (f *fakeTun) MTU() (int, error)         { return f.mtu, nil }
func (f *fakeTun) Name() (string, error)     { return f.name, nil }
func (f *fakeTun) Events() <-chan gtun.Event { return f.events }
func (f *fakeTun) BatchSize() int            { return f.batch }

func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-f.closeCh:
		return 0, os.ErrClosed
	case packet := <-f.readCh:
		copy(bufs[0][offset:], packet)
		sizes[0] = len(packet)
		return 1, nil
	}
}

func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	select {
	case <-f.closeCh:
		return 0, os.ErrClosed
	default:
	}
	for _, buf := range bufs {
		f.writeCh <- append([]byte(nil), buf[offset:]...)
	}
	return len(bufs), nil
}

func (f *fakeTun) Close() error {
	f.once.Do(func() {
		close(f.closeCh)
		close(f.events)
	})
	return nil
}

func waitFor(tb testing.TB, fn func() bool) {
	tb.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tb.Fatalf("condition not met before timeout")
}

func TestTunAdapterStartsDetached(t *testing.T) {
	rwc := newFakeRWC()
	adapter, err := New(rwc, testLogger{}, InterfaceName("none"), InterfaceMTU(1400))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer func() {
		_ = adapter.Stop()
		_ = rwc.Close()
	}()

	status := adapter.Status()
	if status.State != StateDetached {
		t.Fatalf("state = %q, want %q", status.State, StateDetached)
	}
	if status.Attached {
		t.Fatalf("attached = true, want false")
	}
}

func TestTunAdapterAttachDetachReplace(t *testing.T) {
	rwc := newFakeRWC()
	adapter, err := New(rwc, testLogger{}, InterfaceName("none"), InterfaceMTU(1500))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer func() {
		_ = adapter.Stop()
		_ = rwc.Close()
	}()

	tun1 := newFakeTun("tun-a", 1400, 4, 2)
	if err := adapter.Attach(tun1, AttachmentType("fake")); err != nil {
		t.Fatalf("Attach(tun1): %v", err)
	}
	waitFor(t, func() bool {
		status := adapter.Status()
		return status.Attached && status.Name == "tun-a" && status.Type == "fake" && status.MTU == 1400
	})

	rwc.readCh <- []byte{1, 2, 3, 4}
	select {
	case got := <-tun1.writeCh:
		if string(got) != string([]byte{1, 2, 3, 4}) {
			t.Fatalf("write payload = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tun1 write")
	}

	tun2 := newFakeTun("tun-b", 1300, 8, 6)
	if err := adapter.Replace(tun2, AttachmentType("replacement")); err != nil {
		t.Fatalf("Replace(tun2): %v", err)
	}
	waitFor(t, func() bool {
		status := adapter.Status()
		return status.Attached && status.Name == "tun-b" && status.Type == "replacement" && status.MTU == 1300
	})

	rwc.readCh <- []byte{5, 6, 7}
	select {
	case got := <-tun2.writeCh:
		if string(got) != string([]byte{5, 6, 7}) {
			t.Fatalf("replacement payload = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tun2 write")
	}
	select {
	case got := <-tun1.writeCh:
		t.Fatalf("old tun received packet after replace: %v", got)
	default:
	}

	if err := adapter.Detach(); err != nil {
		t.Fatalf("Detach(): %v", err)
	}
	waitFor(t, func() bool { return !adapter.Status().Attached && adapter.Status().State == StateDetached })
}

func TestTunAdapterEventsAndMTU(t *testing.T) {
	rwc := newFakeRWC()
	adapter, err := New(rwc, testLogger{}, InterfaceName("none"), InterfaceMTU(1500))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer func() {
		_ = adapter.Stop()
		_ = rwc.Close()
	}()

	ft := newFakeTun("tun-events", 1500, 0, 0)
	if err := adapter.Attach(ft, AttachmentType("fake")); err != nil {
		t.Fatalf("Attach(): %v", err)
	}
	waitFor(t, func() bool { return adapter.Status().Enabled })

	ft.events <- gtun.EventDown
	waitFor(t, func() bool { return !adapter.Status().Enabled })

	ft.events <- gtun.EventUp
	waitFor(t, func() bool { return adapter.Status().Enabled })

	ft.mtu = 1280
	ft.events <- gtun.EventMTUUpdate
	waitFor(t, func() bool { return adapter.Status().MTU == 1280 && rwc.currentMTU() == 1280 })

	if err := ft.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Close(): %v", err)
	}
	waitFor(t, func() bool { return adapter.Status().State == StateDetached && !adapter.Status().Attached })
}

func TestTunAdapterAttachVTun(t *testing.T) {
	rwc := newFakeRWC()
	adapter, err := New(rwc, testLogger{}, InterfaceName("none"), InterfaceMTU(1500))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer func() {
		_ = adapter.Stop()
		_ = rwc.Close()
	}()

	vt, err := (&vtun.Opts{Name: "vtun-test"}).Build()
	if err != nil {
		t.Fatalf("vtun build: %v", err)
	}
	defer vt.Close()

	if err := adapter.Attach(vt, AttachmentType("vtun")); err != nil {
		t.Fatalf("Attach(vtun): %v", err)
	}
	waitFor(t, func() bool {
		status := adapter.Status()
		return status.Attached && status.Type == "vtun" && status.Name == "vtun-test"
	})
}
