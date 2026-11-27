package cyw43439

import (
	"encoding/binary"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

type fakeBus struct {
	d   bool
	out bool
}

func (b *fakeBus) data() bool   { return b.d }
func (b *fakeBus) drive(v bool) { b.out = v }
func (b *fakeBus) release()     { b.out = false }

func cmdWord(write bool, fn int, addr, size uint32) uint32 {
	var w uint32
	if write {
		w = 1 << 31
	}
	return w | 1<<30 | uint32(fn)<<28 | (addr&0x1ffff)<<11 | (size & 0x7ff)
}

func (b *fakeBus) transfer(d *Device, cmd uint32, write []uint32, readWords int) (words []uint32) {
	d.setCS(false)
	for i := 31; i >= 0; i-- {
		b.d = cmd>>uint(i)&1 != 0
		d.setCLK(false)
		d.setCLK(true)
	}
	for _, w := range write {
		for i := 31; i >= 0; i-- {
			b.d = w>>uint(i)&1 != 0
			d.setCLK(false)
			d.setCLK(true)
		}
	}
	readBits := func(n int) uint32 {
		var v uint32
		for range n {
			d.setCLK(false)
			v = v << 1
			if b.out {
				v |= 1
			}
			d.setCLK(true)
		}
		return v
	}
	for range readWords {
		words = append(words, readBits(32))
	}
	_ = readBits(32) // status word
	d.setCS(true)
	return words
}

func (b *fakeBus) write32(d *Device, fn int, addr, val uint32, word32 bool) {
	cmd := cmdWord(true, fn, addr, 4)
	data := val
	if !word32 {
		cmd = swap16(cmd)
		data = swap16(data)
	}
	b.transfer(d, cmd, []uint32{data}, 0)
}

func (b *fakeBus) read32(d *Device, fn int, addr uint32, word32 bool) uint32 {
	cmd := cmdWord(false, fn, addr, 4)
	if !word32 {
		cmd = swap16(cmd)
	}
	words := b.transfer(d, cmd, nil, 1)
	if len(words) == 0 {
		return 0
	}
	v := words[0]
	if !word32 {
		v = swap16(v)
	}
	return v
}

func TestJoinEvents(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{MAC: [6]byte{2, 0, 0, 0, 0, 1}})
	d.firmwareUp = true

	name := "bsscfg:ssid\x00"
	payload := make([]byte, len(name)+4+32)
	copy(payload, name)
	binary.LittleEndian.PutUint32(payload[len(name):], 4)
	copy(payload[len(name)+4:], "test")
	req := make([]byte, sdpcmHeaderLen+cdcHeaderLen+len(payload))
	size := len(req)
	binary.LittleEndian.PutUint16(req, uint16(size))
	binary.LittleEndian.PutUint16(req[2:], ^uint16(size))
	req[7] = sdpcmHeaderLen
	binary.LittleEndian.PutUint32(req[12:], wlcSetVar)
	binary.LittleEndian.PutUint32(req[16:], uint32(len(payload)))
	copy(req[28:], payload)

	d.wlanWrite(req)
	if len(d.queue) != 5 {
		t.Fatalf("queue = %d, want 1 control + 4 events", len(d.queue))
	}
	if !d.LinkUp() || d.SSID() != "test" {
		t.Fatalf("join state: up=%v ssid=%q", d.LinkUp(), d.SSID())
	}
	resp := d.wlanRead(len(d.queue[0]))
	if got := resp[5] & 0xf; got != channelControl {
		t.Fatalf("control response channel = %d", got)
	}
	if got := binary.LittleEndian.Uint32(resp[sdpcmHeaderLen:]); got != wlcSetVar {
		t.Fatalf("control response cmd = %d", got)
	}

	want := []struct {
		typ    uint32
		status uint32
	}{
		{evSETSSID, 0},
		{evAUTH, 0},
		{evJOIN, 0},
		{evPSKSUP, 6},
	}
	for i, w := range want {
		pkt := d.wlanRead(len(d.queue[0]))
		if got := pkt[5] & 0xf; got != 1 {
			t.Fatalf("event %d channel = %d", i, got)
		}
		if got := binary.LittleEndian.Uint16(pkt); int(got) != len(pkt) {
			t.Fatalf("event %d size = %d, packet %d", i, got, len(pkt))
		}
		msg := sdpcmHeaderLen + 4 + 24
		if got := binary.BigEndian.Uint32(pkt[msg+4:]); got != w.typ {
			t.Fatalf("event %d type = %d, want %d", i, got, w.typ)
		}
		if got := binary.BigEndian.Uint32(pkt[msg+8:]); got != w.status {
			t.Fatalf("event %d status = %d, want %d", i, got, w.status)
		}
	}
	if len(d.queue) != 0 {
		t.Fatalf("%d packets left in the queue", len(d.queue))
	}
}

func TestBusRegisters(t *testing.T) {
	b := &fakeBus{}
	d := newDevice(b, Config{MAC: [6]byte{1, 2, 3, 4, 5, 6}})
	d.regOn = true

	if got := b.read32(d, 0, spiTestReg, false); got != spiTestPattern {
		t.Fatalf("16-bit test register = %#x", got)
	}
	// Switch to 32-bit words through the bus control register.
	b.write32(d, 0, spiBusControl, 1, false)
	if !d.word32 {
		t.Fatal("bus control did not select 32-bit words")
	}
	if got := b.read32(d, 0, spiTestReg, true); got != spiTestPattern {
		t.Fatalf("32-bit test register = %#x", got)
	}
	b.write32(d, 0, spiRWTestReg, 0x12345678, true)
	if got := b.read32(d, 0, spiRWTestReg, true); got != 0x12345678 {
		t.Fatalf("RW test register = %#x", got)
	}
}

func TestBackplaneChipAndFirmware(t *testing.T) {
	b := &fakeBus{}
	d := newDevice(b, Config{})
	d.regOn = true
	d.word32 = true

	// Set the backplane window to the chip-common base.
	for _, w := range []struct{ addr, val uint32 }{
		{sdioWindowHigh, 0x18}, {sdioWindowMid, 0x00}, {sdioWindowLow, 0x00},
	} {
		b.write32(d, 1, w.addr, w.val, true)
	}
	words := b.transfer(d, cmdWord(false, 1, 0, 2), nil, 2)
	if got := uint16(words[1]); got != 0x3439 {
		t.Fatalf("chip id = %#x", got)
	}

	// Release the WLAN core so the driver sees "firmware running". The driver
	// reaches these via the backplane window, so drive bpWrite directly.
	d.bpWrite(wlanCoreBase+coreIOCtrl, []byte{coreSICFClock})
	d.bpWrite(wlanCoreBase+coreResetCtrl, []byte{0})
	if !d.FirmwareUp() {
		t.Fatal("WLAN core reset did not bring firmware up")
	}
}

func TestControlIoctls(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{MAC: [6]byte{2, 1, 2, 3, 4, 5}})
	d.firmwareUp = true

	// cur_etheraddr GET: name + zero padding, 6-byte answer.
	req := make([]byte, sdpcmHeaderLen+cdcHeaderLen+len("cur_etheraddr")+1+6)
	size := len(req)
	binary.LittleEndian.PutUint16(req, uint16(size))
	binary.LittleEndian.PutUint16(req[2:], ^uint16(size))
	req[7] = sdpcmHeaderLen
	binary.LittleEndian.PutUint32(req[12:], wlcGetVar)
	binary.LittleEndian.PutUint32(req[16:], uint32(len(req)-28))
	copy(req[28:], "cur_etheraddr\x00")
	d.wlanWrite(req)

	if len(d.queue) != 1 {
		t.Fatalf("queue = %d", len(d.queue))
	}
	resp := d.wlanRead(len(d.queue[0]))
	payload := resp[28:]
	if len(payload) < 6 || [6]byte(payload[:6]) != d.cfg.MAC {
		t.Fatalf("MAC response = % x", payload)
	}

	// gpioout SET turns WL_GPIO0 on.
	req = make([]byte, sdpcmHeaderLen+cdcHeaderLen+len("gpioout")+1+8)
	size = len(req)
	binary.LittleEndian.PutUint16(req, uint16(size))
	binary.LittleEndian.PutUint16(req[2:], ^uint16(size))
	req[7] = sdpcmHeaderLen
	binary.LittleEndian.PutUint32(req[12:], wlcSetVar)
	binary.LittleEndian.PutUint32(req[16:], uint32(len(req)-28))
	copy(req[28:], "gpioout\x00")
	binary.LittleEndian.PutUint32(req[28+len("gpioout")+1:], 1)
	binary.LittleEndian.PutUint32(req[32+len("gpioout")+1:], 1)
	d.wlanWrite(req)
	if !d.LED() {
		t.Fatal("gpioout did not turn the LED on")
	}
}

func TestBackplane32BitFlag(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{})
	d.window = 0x18100000
	if got := d.fullAddr(0xb408); got != 0x18103408 {
		t.Fatalf("flagged core address = %#x", got)
	}
	if got := d.fullAddr(0x3408); got != 0x18103408 {
		t.Fatalf("plain core address = %#x", got)
	}
	if got := d.fullAddr(0x1000a); got != 0x1000a {
		t.Fatalf("SDIO register address = %#x", got)
	}
}

func TestBTSharedBusReset(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	d := newDevice(&fakeBus{}, Config{BLE: true, BLEAddr: [6]byte{1, 2, 3, 4, 5, 6}})
	d.chip = c
	d.bt = newBTBus(d)

	if got := binary.LittleEndian.Uint32(d.bpRead(btRAMBaseReg, 4)); got != btSharedBase {
		t.Fatalf("WLAN_RAM_BASE = %#x", got)
	}
	if got := binary.LittleEndian.Uint32(d.bpRead(btCtrlReg, 4)); got&(1<<24) == 0 || got&(1<<8) == 0 {
		t.Fatalf("BT_CTRL = %#x, want FW_RDY|BT_AWAKE", got)
	}

	// HCI Reset: payload length 3, type 1, opcode 0x0C03, no parameters.
	hci := []byte{3, 0, 0, 0x01, 0x03, 0x0C, 0x00, 0}
	d.bpWrite(btSharedBase, hci)
	d.bpWrite(btSharedBase+0x2000, []byte{8, 0, 0, 0})

	if got := binary.LittleEndian.Uint32(d.bpRead(btSharedBase+0x2008, 4)); got != 12 {
		t.Fatalf("bt2host_in = %d, want 12", got)
	}
	ev := d.bpRead(btSharedBase+0x1000, 12)
	if ev[3] != 0x04 || ev[4] != 0x0E {
		t.Fatalf("HCI event = % x, want command complete", ev)
	}
}
