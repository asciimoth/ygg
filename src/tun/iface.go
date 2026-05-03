package tun

import (
	"errors"
	"io"
	"os"
	"sync"
)

const maxPacketSize = 65535
const bufPoolSize = maxPacketSize

var bufPool = sync.Pool{
	New: func() any {
		b := [bufPoolSize]byte{}
		return b[:]
	},
}

func (tun *TunAdapter) queue() {
	for {
		p := bufPool.Get().([]byte)[:bufPoolSize]
		n, err := tun.rwc.Read(p)
		if err != nil {
			bufPool.Put(p[:bufPoolSize])
			select {
			case <-tun.stopCh:
				return
			default:
				tun.log.Errorln("Exiting TUN queue due to core read error:", err)
				return
			}
		}
		select {
		case tun.packetCh <- p[:n]:
		case <-tun.stopCh:
			bufPool.Put(p[:bufPoolSize])
			return
		}
	}
}

func (s *attachmentSession) start(tun *TunAdapter) {
	s.wg.Add(3)
	go s.readLoop(tun)
	go s.writeLoop(tun)
	go s.eventLoop(tun)
}

func (s *attachmentSession) stop() {
	if !s.stopping.CompareAndSwap(false, true) {
		return
	}
	close(s.stopCh)
	_ = s.device.Close()
	s.wg.Wait()
}

func (s *attachmentSession) readLoop(tun *TunAdapter) {
	defer s.wg.Done()

	bufs := make([][]byte, s.batch)
	sizes := make([]int, s.batch)
	readOffset := s.mro
	for i := range bufs {
		bufs[i] = make([]byte, readOffset+maxPacketSize)
	}
	for {
		n, err := s.device.Read(bufs, sizes, readOffset)
		if err != nil {
			if !s.stopping.Load() && !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.EOF) {
				tun.log.Errorln("Error reading TUN:", err)
				tun.reportCh <- sessionReport{session: s, kind: sessionReportReadError}
			}
			return
		}
		if tun.currentSessionID.Load() != s.id {
			continue
		}
		for i, b := range bufs[:n] {
			if _, err := tun.rwc.Write(b[readOffset : readOffset+sizes[i]]); err != nil {
				tun.log.Debugln("Unable to send packet:", err)
			}
		}
	}
}

func (s *attachmentSession) writeLoop(tun *TunAdapter) {
	defer s.wg.Done()

	batch := s.batch
	if batch < 1 {
		batch = 1
	}
	packetBatch := make([][]byte, 0, batch)
	for {
		packetBatch = packetBatch[:0]

		select {
		case <-s.stopCh:
			return
		case packet := <-s.packetCh:
			packetBatch = append(packetBatch, packet)
		}

		drain := true
		for drain && len(packetBatch) < batch {
			select {
			case packet := <-s.packetCh:
				packetBatch = append(packetBatch, packet)
			default:
				drain = false
			}
		}

		bufs := make([][]byte, len(packetBatch))
		for i, packet := range packetBatch {
			buf := make([]byte, s.mwo+len(packet))
			copy(buf[s.mwo:], packet)
			bufs[i] = buf
		}
		for _, packet := range packetBatch {
			bufPool.Put(packet[:bufPoolSize])
		}
		for len(bufs) > 0 {
			written, err := s.device.Write(bufs, s.mwo)
			if err != nil {
				if !s.stopping.Load() && !errors.Is(err, os.ErrClosed) {
					tun.log.Errorln("TUN iface write error:", err)
					tun.reportCh <- sessionReport{session: s, kind: sessionReportWriteError}
				}
				return
			}
			if written >= len(bufs) {
				break
			}
			bufs = bufs[written:]
		}
	}
}

func (s *attachmentSession) eventLoop(tun *TunAdapter) {
	defer s.wg.Done()

	for {
		select {
		case <-s.stopCh:
			return
		case event, ok := <-s.device.Events():
			if !ok {
				if !s.stopping.Load() {
					tun.reportCh <- sessionReport{session: s, kind: sessionReportEventsClosed}
				}
				return
			}
			if event&1 != 0 || event&2 != 0 || event&4 != 0 {
				if event&4 != 0 {
					if mtu, err := s.device.MTU(); err == nil {
						s.mtu = getSupportedMTU(uint64(mtu))
					}
				}
				tun.reportCh <- sessionReport{session: s, kind: sessionReportEvent, event: event}
			}
		}
	}
}
