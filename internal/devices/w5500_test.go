package devices

import (
	"testing"

	"github.com/akh/rp2350sim/internal/rp2350"
)

func newTestW5500() (*W5500, *rp2350.Chip) {
	c := rp2350.New()
	w := NewW5500(c, 5)
	c.Pins.SetExternal(5, false) // chip select asserted (low)
	return w, c
}

// xfer sends bytes with CS low.
func (w *W5500) xfer(b ...byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = w.Transfer(v)
	}
	return out
}

func (w *W5500) endFrame(c *rp2350.Chip) {
	c.Pins.SetExternal(5, true)
	w.Tick()
	c.Pins.SetExternal(5, false)
}

func (w *W5500) writeReg(c *rp2350.Chip, addr uint16, bsb uint8, data ...byte) {
	w.xfer(byte(addr>>8), byte(addr), bsb<<3|0x04)
	w.xfer(data...)
	w.endFrame(c)
}

func (w *W5500) readReg(c *rp2350.Chip, addr uint16, bsb uint8, n int) []byte {
	w.xfer(byte(addr>>8), byte(addr), bsb<<3)
	out := w.xfer(make([]byte, n)...)
	w.endFrame(c)
	return out
}

func TestW5500Registers(t *testing.T) {
	w, c := newTestW5500()
	if got := w.readReg(c, w5CommonChipVer, 0, 1); got[0] != 0x04 {
		t.Fatalf("chip version = %#x", got[0])
	}
	if got := w.readReg(c, w5CommonPHYCfg, 0, 1); got[0]&1 == 0 {
		t.Fatalf("PHYCFG = %#x, link down", got[0])
	}
	// Common register write/read.
	w.writeReg(c, 0x0009, 0, 0x02, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66)
	if got := w.readReg(c, 0x0009, 0, 6); string(got) != string([]byte{2, 0x11, 0x22, 0x33, 0x44, 0x55}) {
		t.Fatalf("MAC readback = % x", got)
	}
}

func TestW5500SocketOpenClose(t *testing.T) {
	w, c := newTestW5500()
	// UDP socket: mode 2, then OPEN.
	w.writeReg(c, w5SockMode, 1, 2)
	w.writeReg(c, w5SockCmd, 1, w5CmdOpen)
	if got := w.readReg(c, w5SockStatus, 1, 1); got[0] != w5StatusUDP {
		t.Fatalf("status after open = %#x", got[0])
	}
	w.writeReg(c, w5SockCmd, 1, w5CmdClose)
	if got := w.readReg(c, w5SockStatus, 1, 1); got[0] != w5StatusClosed {
		t.Fatalf("status after close = %#x", got[0])
	}
}

func TestW5500BuffersAndPointers(t *testing.T) {
	w, c := newTestW5500()
	w.writeReg(c, w5SockTXBufSize, 1, 2) // 2 KB
	w.writeReg(c, w5SockMode, 1, 1)      // TCP
	w.writeReg(c, w5SockCmd, 1, w5CmdOpen)

	// Fill three bytes at TX offset 0 and publish the write pointer.
	w.writeReg(c, 0, 2, 'a', 'b', 'c')
	w.writeReg(c, w5SockTXWritePtr, 1, 0x00, 0x03)
	if got := w.readReg(c, w5SockTXWritePtr, 1, 2); got[0] != 0 || got[1] != 3 {
		t.Fatalf("TX write ptr = % x", got)
	}
	if got := w.readReg(c, w5SockTXFreeSize, 1, 2); int(got[0])<<8|int(got[1]) != 2045 {
		t.Fatalf("TX free = % x", got)
	}
	// Read the payload back through the TX block.
	if got := w.readReg(c, 0, 2, 3); string(got) != "abc" {
		t.Fatalf("TX buffer = %q", got)
	}
}
