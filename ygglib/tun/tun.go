package tun

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	gtun "github.com/asciimoth/gonnect/tun"

	"github.com/asciimoth/ygg/ygglib/core"
)

type ReadWriteCloser interface {
	io.ReadWriteCloser
	MaxMTU() uint64
	SetMTU(uint64)
}

type State string

const (
	StateDetached State = "detached"
	StateAttached State = "attached"
	StateStopped  State = "stopped"
)

type Status struct {
	State    State  `json:"state"`
	Enabled  bool   `json:"enabled"`
	Attached bool   `json:"attached"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	MTU      uint64 `json:"mtu,omitempty"`
	MRO      int    `json:"mro,omitempty"`
	MWO      int    `json:"mwo,omitempty"`
}

type AttachOption interface {
	applyAttach(*attachSpec)
}

type AttachmentType string

func (t AttachmentType) applyAttach(spec *attachSpec) {
	spec.kind = string(t)
}

type TunAdapter struct {
	rwc ReadWriteCloser
	log core.Logger

	config struct {
		mtu      InterfaceMTU
		firewall *Firewall
	}

	controlCh chan any
	packetCh  chan []byte
	reportCh  chan sessionReport
	stopCh    chan struct{}

	currentSessionID atomic.Uint64
	started          atomic.Bool
	stopped          atomic.Bool
	stopOnce         sync.Once
}

type attachSpec struct {
	device gtun.Tun
	kind   string
	name   string
	mtu    uint64
	mro    int
	mwo    int
	batch  int
}

type attachRequest struct {
	spec    *attachSpec
	replace bool
	resp    chan error
}

type detachRequest struct {
	resp chan error
}

type statusRequest struct {
	resp chan Status
}

type stopRequest struct {
	resp chan error
}

type sessionReport struct {
	session *attachmentSession
	kind    sessionReportKind
	event   gtun.Event
	mtu     uint64
}

type sessionReportKind uint8

const (
	sessionReportReadError sessionReportKind = iota + 1
	sessionReportWriteError
	sessionReportEventsClosed
	sessionReportEvent
)

type attachmentSession struct {
	id       uint64
	kind     string
	device   gtun.Tun
	name     string
	mtu      uint64
	mro      int
	mwo      int
	batch    int
	packetCh chan []byte
	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopping atomic.Bool
}

type supervisorState struct {
	status  Status
	current *attachmentSession
	nextID  uint64
}

const defaultMTU uint64 = 65535

func getSupportedMTU(mtu uint64) uint64 {
	if mtu < 1280 {
		return 1280
	}
	return mtu
}

func New(rwc ReadWriteCloser, log core.Logger, opts ...SetupOption) (*TunAdapter, error) {
	tun := &TunAdapter{
		rwc:       rwc,
		log:       log,
		controlCh: make(chan any),
		packetCh:  make(chan []byte, 256),
		reportCh:  make(chan sessionReport, 64),
		stopCh:    make(chan struct{}),
	}
	tun.config.mtu = InterfaceMTU(defaultMTU)
	tun.config.firewall = NewFirewall(FirewallConfig{})
	for _, opt := range opts {
		tun._applyOption(opt)
	}
	tun.started.Store(true)
	go tun.supervisor()
	go tun.queue()
	tun.rwc.SetMTU(tun.desiredMTU())
	return tun, nil
}

func (tun *TunAdapter) IsStarted() bool {
	return tun.started.Load() && !tun.stopped.Load()
}

func (tun *TunAdapter) Stop() error {
	if !tun.started.Load() {
		return nil
	}
	tun.stopOnce.Do(func() {
		tun.stopped.Store(true)
	})
	resp := make(chan error, 1)
	select {
	case tun.controlCh <- stopRequest{resp: resp}:
		return <-resp
	case <-tun.stopCh:
		return nil
	}
}

func (tun *TunAdapter) Attach(device gtun.Tun, opts ...AttachOption) error {
	spec, err := tun.buildAttachSpec(device, opts...)
	if err != nil {
		return err
	}
	return tun.submitAttach(spec, false)
}

func (tun *TunAdapter) Replace(device gtun.Tun, opts ...AttachOption) error {
	spec, err := tun.buildAttachSpec(device, opts...)
	if err != nil {
		return err
	}
	return tun.submitAttach(spec, true)
}

func (tun *TunAdapter) Detach() error {
	resp := make(chan error, 1)
	select {
	case tun.controlCh <- detachRequest{resp: resp}:
		return <-resp
	case <-tun.stopCh:
		return nil
	}
}

func (tun *TunAdapter) Status() Status {
	resp := make(chan Status, 1)
	select {
	case tun.controlCh <- statusRequest{resp: resp}:
		return <-resp
	case <-tun.stopCh:
		return Status{State: StateStopped}
	}
}

func (tun *TunAdapter) Name() string {
	return tun.Status().Name
}

func (tun *TunAdapter) MTU() uint64 {
	return tun.Status().MTU
}

func (tun *TunAdapter) SetFirewallConfig(cfg FirewallConfig) {
	tun.config.firewall.SetConfig(cfg)
}

func (tun *TunAdapter) FirewallConfig() FirewallConfig {
	return tun.config.firewall.Config()
}

func (tun *TunAdapter) FirewallStatus() FirewallStatus {
	return tun.config.firewall.Status()
}

func (tun *TunAdapter) desiredMTU() uint64 {
	mtu := uint64(tun.config.mtu)
	if mtu == 0 {
		mtu = defaultMTU
	}
	if max := tun.rwc.MaxMTU(); max > 0 && max < mtu {
		mtu = max
	}
	return getSupportedMTU(mtu)
}

func (tun *TunAdapter) submitAttach(spec *attachSpec, replace bool) error {
	resp := make(chan error, 1)
	select {
	case tun.controlCh <- attachRequest{spec: spec, replace: replace, resp: resp}:
		return <-resp
	case <-tun.stopCh:
		return io.ErrClosedPipe
	}
}

func (tun *TunAdapter) buildAttachSpec(device gtun.Tun, opts ...AttachOption) (*attachSpec, error) {
	spec := &attachSpec{
		device: device,
		kind:   "external",
	}
	for _, opt := range opts {
		opt.applyAttach(spec)
	}
	name, err := device.Name()
	if err != nil {
		return nil, fmt.Errorf("read tun name: %w", err)
	}
	mtu, err := device.MTU()
	if err != nil {
		return nil, fmt.Errorf("read tun mtu: %w", err)
	}
	spec.name = name
	spec.mtu = getSupportedMTU(uint64(mtu))
	spec.mro = device.MRO()
	spec.mwo = device.MWO()
	spec.batch = device.BatchSize()
	if spec.batch <= 0 {
		spec.batch = 1
	}
	return spec, nil
}

func (tun *TunAdapter) supervisor() {
	state := supervisorState{
		status: Status{
			State: StateDetached,
		},
	}
	for {
		select {
		case msg := <-tun.controlCh:
			switch req := msg.(type) {
			case attachRequest:
				state.handleAttach(tun, req)
			case detachRequest:
				req.resp <- state.detachCurrent(tun, StateDetached)
			case statusRequest:
				req.resp <- state.status
			case stopRequest:
				req.resp <- state.detachCurrent(tun, StateStopped)
				close(tun.stopCh)
				return
			}
		case packet := <-tun.packetCh:
			state.routePacket(tun, packet)
		case report := <-tun.reportCh:
			state.handleReport(tun, report)
		case <-tun.stopCh:
			return
		}
	}
}

func (s *supervisorState) handleAttach(tun *TunAdapter, req attachRequest) {
	if s.status.State == StateStopped {
		req.resp <- io.ErrClosedPipe
		return
	}
	if s.current != nil && !req.replace {
		req.resp <- fmt.Errorf("tun already attached: %s", s.current.name)
		return
	}
	next := s.nextSession(req.spec)
	next.start(tun)

	old := s.current
	s.current = next
	tun.currentSessionID.Store(next.id)
	s.status = next.status(true, StateAttached)
	tun.rwc.SetMTU(next.mtu)
	if old != nil {
		go old.stop()
	}
	req.resp <- nil
}

func (s *supervisorState) nextSession(spec *attachSpec) *attachmentSession {
	s.nextID++
	return &attachmentSession{
		id:       s.nextID,
		kind:     spec.kind,
		device:   spec.device,
		name:     spec.name,
		mtu:      spec.mtu,
		mro:      spec.mro,
		mwo:      spec.mwo,
		batch:    spec.batch,
		packetCh: make(chan []byte, spec.batch*4),
		stopCh:   make(chan struct{}),
	}
}

func (s *supervisorState) detachCurrent(tun *TunAdapter, nextState State) error {
	if s.current != nil {
		old := s.current
		s.current = nil
		tun.currentSessionID.Store(0)
		s.status = Status{State: nextState}
		tun.rwc.SetMTU(tun.desiredMTU())
		go old.stop()
		return nil
	}
	s.status = Status{State: nextState}
	tun.rwc.SetMTU(tun.desiredMTU())
	return nil
}

func (s *supervisorState) routePacket(tun *TunAdapter, packet []byte) {
	current := s.current
	if current == nil || !s.status.Enabled {
		bufPool.Put(packet[:bufPoolSize])
		return
	}
	if current.mtu > 0 && uint64(len(packet)) > current.mtu {
		tun.log.Debugf("Dropping oversized packet len=%d mtu=%d for %s", len(packet), current.mtu, current.name)
		bufPool.Put(packet[:bufPoolSize])
		return
	}
	if !tun.config.firewall.AllowIncoming(packet, time.Now()) {
		bufPool.Put(packet[:bufPoolSize])
		return
	}
	select {
	case current.packetCh <- packet:
	default:
		bufPool.Put(packet[:bufPoolSize])
	}
}

func (s *supervisorState) handleReport(tun *TunAdapter, report sessionReport) {
	if s.current == nil || s.current.id != report.session.id {
		return
	}
	switch report.kind {
	case sessionReportReadError, sessionReportWriteError, sessionReportEventsClosed:
		_ = s.detachCurrent(tun, StateDetached)
	case sessionReportEvent:
		if report.event&gtun.EventDown != 0 {
			s.status.Enabled = false
		}
		if report.event&gtun.EventUp != 0 {
			s.status.Enabled = true
		}
		if report.event&gtun.EventMTUUpdate != 0 {
			s.current.mtu = report.mtu
			s.status.MTU = report.mtu
			tun.rwc.SetMTU(report.mtu)
		}
	}
}

func (s *attachmentSession) status(enabled bool, state State) Status {
	return Status{
		State:    state,
		Enabled:  enabled,
		Attached: true,
		Type:     s.kind,
		Name:     s.name,
		MTU:      s.mtu,
		MRO:      s.mro,
		MWO:      s.mwo,
	}
}
