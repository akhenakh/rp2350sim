package cyw43439

import (
	"encoding/binary"
	"testing"
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
