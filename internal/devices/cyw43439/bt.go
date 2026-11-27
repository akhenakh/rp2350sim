package cyw43439

import (
	"log/slog"

	"github.com/akhenakh/rp2350sim/internal/devices/ble"
)

// CYW43439 Bluetooth shared-bus model.
//
// The Wi-Fi and Bluetooth cores exchange HCI through a shared-memory ring
// buffer, not a FIFO on the gSPI pins. The host downloads the BT firmware
// into the core, then uses four pointers at WLAN_RAM_BASE+0x2000..0x200C:
//
//	host2bt_in/out  host -> BT ring at +0x0000, 4 KiB
//	bt2host_in/out  BT -> host ring at +0x1000, 4 KiB
//
// Each packet is a 3-byte payload length, one HCI packet-type byte and the
// payload, padded to four bytes. The existing internal/devices/ble controller
// handles the HCI command/event/ACL protocol; this file is only the transport.

const (
	btSharedBase = 0x1A000000 // returned by WLAN_RAM_BASE_REG_ADDR
	btFwBase     = 0x19000000 // CYW_BT_BASE_ADDRESS: firmware upload, ignored
	btRAMBaseReg = 0x18000d68
	btCtrlReg    = 0x18000c7c
	btHostCtrl   = 0x18000d6c
	btFCChange   = 1 << 5 // I_HMB_FC_CHANGE in SDIO_INT_STATUS

	btRingSize = 0x1000
	btBufSize  = 0x2000
)

type btBus struct {
	d        *Device
	ctrl     *ble.Controller
	mem      [btBufSize]byte
	h2bIn    uint32
	h2bOut   uint32
	b2hIn    uint32
	b2hOut   uint32
	hostCtrl uint32
}

func newBTBus(d *Device) *btBus {
	b := &btBus{d: d}
	b.ctrl = ble.New(d.chip, ble.Options{
		HostWake: -1,
		Address:  d.cfg.BLEAddr,
		Seed:     d.cfg.BLESeed,
	})
	if d.cfg.HCILog != nil {
		if err := b.ctrl.SetHCILog(d.cfg.HCILog); err != nil {
			d.log.Error("cyw43439 hci log", slog.Any("err", err))
		}
	}
	d.chip.AddResetHook(b.reset)
	return b
}

func (b *btBus) reset() {
	b.mem = [btBufSize]byte{}
	b.h2bIn, b.h2bOut, b.b2hIn, b.b2hOut = 0, 0, 0, 0
	b.hostCtrl = 0
}

// ctrlBits is BT_CTRL_REG_ADDR: firmware ready and the BT core awake.
func (b *btBus) ctrlBits() uint32 { return 1<<24 | 1<<8 }

func (b *btBus) read(full uint32, length int) ([]byte, bool) {
	switch full {
	case btRAMBaseReg:
		return leBytes(btSharedBase, length), true
	case btCtrlReg:
		return leBytes(b.ctrlBits(), length), true
	case btHostCtrl:
		return leBytes(b.hostCtrl, length), true
	case btSharedBase + 0x2000:
		return leBytes(b.h2bIn, length), true
	case btSharedBase + 0x2004:
		return leBytes(b.h2bOut, length), true
	case btSharedBase + 0x2008:
		return leBytes(b.b2hIn, length), true
	case btSharedBase + 0x200C:
		return leBytes(b.b2hOut, length), true
	}
	if full >= btSharedBase && full < btSharedBase+btBufSize {
		return b.readRing(full-btSharedBase, length), true
	}
	return nil, false
}

func (b *btBus) write(full uint32, data []byte) bool {
	switch full {
	case btHostCtrl:
		b.hostCtrl = leValue(data)
		return true
	case btSharedBase + 0x2000:
		b.h2bIn = leValue(data) & (btRingSize - 1)
		b.drainHost()
		return true
	case btSharedBase + 0x2004:
		b.h2bOut = leValue(data) & (btRingSize - 1)
		return true
	case btSharedBase + 0x2008:
		b.b2hIn = leValue(data) & (btRingSize - 1)
		return true
	case btSharedBase + 0x200C:
		b.b2hOut = leValue(data) & (btRingSize - 1)
		// The host just reclaimed space; push anything waiting now instead of
		// waiting up to a millisecond for the periodic BLE tick.
		b.pushEvents()
		return true
	}
	if full >= btSharedBase && full < btSharedBase+btBufSize {
		off := full - btSharedBase
		ring := 0
		if off >= btRingSize {
			ring = 1
			off -= btRingSize
		}
		b.writeRing(ring, off, data)
		return true
	}
	// The BT firmware download and the power-up word are accepted, not
	// executed: the HCI controller is simulated above the bus.
	if full >= btFwBase && full < btFwBase+0x100000 {
		return true
	}
	return false
}

func (b *btBus) readRing(off uint32, n int) []byte {
	out := make([]byte, n)
	ring := 0
	if off >= btRingSize {
		ring = 1
		off -= btRingSize
	}
	for i := range out {
		out[i] = b.mem[ring*btRingSize+int((off+uint32(i))&(btRingSize-1))]
	}
	return out
}

func (b *btBus) writeRing(ring int, off uint32, data []byte) {
	for i, v := range data {
		b.mem[ring*btRingSize+int((off+uint32(i))&(btRingSize-1))] = v
	}
}

// drainHost parses the HCI packets the host just wrote into the host2bt ring.
func (b *btBus) drainHost() {
	for b.h2bOut != b.h2bIn {
		used := (b.h2bIn - b.h2bOut) & (btRingSize - 1)
		if used < 4 {
			break
		}
		hdr := b.readRing(b.h2bOut, 4)
		n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
		total := align4(4 + n)
		if n > btRingSize-8 || uint32(total) > used {
			b.h2bOut = b.h2bIn // malformed or torn packet
			break
		}
		pkt := b.readRing(b.h2bOut, total)
		b.ctrl.WriteHCI(pkt[3], pkt[4:4+n])
		b.h2bOut = (b.h2bOut + uint32(total)) & (btRingSize - 1)
	}
	b.pushEvents()
}

// pushEvents copies queued controller events into the bt2host ring and raises
// the flow-control interrupt the C driver polls.
func (b *btBus) pushEvents() {
	queued := 0
	for {
		pkt := b.ctrl.PeekEvent()
		if pkt == nil {
			break
		}
		if len(pkt) < 2 {
			b.ctrl.PopEvent()
			continue
		}
		n := len(pkt) - 1
		total := align4(4 + n)
		if total > btRingSize {
			b.ctrl.PopEvent()
			continue
		}
		used := (b.b2hIn - b.b2hOut) & (btRingSize - 1)
		if used+uint32(total) >= btRingSize {
			break // the host is not draining; keep the rest queued
		}
		b.ctrl.PopEvent()
		hdr := [4]byte{byte(n), byte(n >> 8), byte(n >> 16), pkt[0]}
		b.writeRing(1, b.b2hIn, hdr[:])
		b.writeRing(1, b.b2hIn+4, pkt[1:])
		b.b2hIn = (b.b2hIn + uint32(total)) & (btRingSize - 1)
		queued++
	}
	if queued > 0 {
		b.d.intStatus |= btFCChange
	}
}

func (b *btBus) tick() { b.pushEvents() }

func align4(v int) int { return (v + 3) &^ 3 }

// BLEStatus returns the gSPI Bluetooth controller state and whether it is
// enabled.
func (d *Device) BLEStatus() (ble.Status, bool) {
	if d.bt == nil {
		return ble.Status{}, false
	}
	return d.bt.ctrl.Status(), true
}
