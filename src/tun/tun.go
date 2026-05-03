package tun

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	gtun "github.com/asciimoth/gonnect/tun"

	"github.com/asciimoth/ygg/src/address"
	"github.com/asciimoth/ygg/src/config"
	"github.com/asciimoth/ygg/src/core"
)

type ReadWriteCloser interface {
	io.ReadWriteCloser
	Address() address.Address
	Subnet() address.Subnet
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
	rwc    ReadWriteCloser
	log    core.Logger
	addr   address.Address
	subnet address.Subnet

	config struct {
		fd   int32
		name InterfaceName
		mtu  InterfaceMTU
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

func getSupportedMTU(mtu uint64) uint64 {
	if mtu < 1280 {
		return 1280
	}
	if mtu > MaximumMTU() {
		return MaximumMTU()
	}
	return mtu
}

func DefaultName() string {
	return config.GetDefaults().DefaultIfName
}

func DefaultMTU() uint64 {
	return config.GetDefaults().DefaultIfMTU
}

func MaximumMTU() uint64 {
	return config.GetDefaults().MaximumIfMTU
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
	tun.config.name = InterfaceName(DefaultName())
	tun.config.mtu = InterfaceMTU(DefaultMTU())
	for _, opt := range opts {
		tun._applyOption(opt)
	}
	tun.addr = tun.rwc.Address()
	tun.subnet = tun.rwc.Subnet()
	tun.started.Store(true)
	go tun.supervisor()
	go tun.queue()

	if tun.config.name == "none" || tun.config.name == "dummy" {
		tun.log.Debugln("Not attaching native TUN as ifname is none or dummy")
		tun.rwc.SetMTU(tun.desiredMTU())
		return tun, nil
	}

	if err := tun.attachNative(false); err != nil {
		_ = tun.Stop()
		return nil, err
	}
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

func (tun *TunAdapter) desiredMTU() uint64 {
	mtu := uint64(tun.config.mtu)
	if mtu == 0 {
		mtu = DefaultMTU()
	}
	if max := tun.rwc.MaxMTU(); max > 0 && max < mtu {
		mtu = max
	}
	return getSupportedMTU(mtu)
}

func (tun *TunAdapter) buildTunAddress() string {
	prefix := address.GetPrefix()
	if !tun.addr.IsValid() {
		return ""
	}
	return fmt.Sprintf("%s/%d", net.IP(tun.addr[:]).String(), 8*len(prefix[:])-1)
}

func (tun *TunAdapter) attachNative(replace bool) error {
	addr := tun.buildTunAddress()
	device, err := tun.createNativeTun(addr, tun.desiredMTU())
	if err != nil {
		return err
	}
	spec, err := tun.buildAttachSpec(device, AttachmentType("native"))
	if err != nil {
		_ = device.Close()
		return err
	}
	if err := tun.submitAttach(spec, replace); err != nil {
		_ = device.Close()
		return err
	}
	if spec.mtu != tun.desiredMTU() {
		tun.log.Warnf(
			"Warning: Interface MTU %d automatically adjusted to %d (supported range is 1280-%d)",
			tun.config.mtu,
			spec.mtu,
			MaximumMTU(),
		)
	}
	return nil
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
			s.status.MTU = report.session.mtu
			tun.rwc.SetMTU(report.session.mtu)
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
