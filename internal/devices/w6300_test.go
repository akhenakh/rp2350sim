package devices

import (
	"testing"

	"github.com/akh/rp2350sim/internal/rp2350"
)

func newTestW6300() (*W6300, *rp2350.Chip) {
	c := rp2350.New()
	w := NewW6300(c, 5)
	c.Pins.SetExternal(5, false)
	return w, c
}

func (w *W6300) xfer(b ...byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = w.Transfer(v)
	}
	return out
}

func (w *W6300) endFrame(c *rp2350.Chip) {
	c.Pins.SetExternal(5, true)
	w.Tick()
	c.Pins.SetExternal(5, false)
}

func (w *W6300) writeBlock(c *rp2350.Chip, bsb uint8, addr uint16, data ...byte) {
	w.xfer(0x20|bsb, byte(addr>>8), byte(addr), 0)
	w.xfer(data...)
	w.endFrame(c)
}

func (w *W6300) readBlock(c *rp2350.Chip, bsb uint8, addr uint16, n int) []byte {
	w.xfer(bsb, byte(addr>>8), byte(addr), 0)
	out := w.xfer(make([]byte, n)...)
	w.endFrame(c)
	return out
}

func TestW6300Registers(t *testing.T) {
	w, c := newTestW6300()

	// Chip identification: 0x61, 0x00, 0x00, 0x00, 0x11 at offsets 0..4.
	got := w.readBlock(c, w6BlockCommon, w6CIDR0, 5)
	want := []byte{0x61, 0x00, 0x00, 0x00, 0x11}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CIDR = % x, want % x", got, want)
		}
	}
	// PHYSR reports link up.
	if got := w.readBlock(c, w6BlockCommon, w6PHYSR, 1); got[0]&1 == 0 {
		t.Fatalf("PHYSR = %#x, link down", got[0])
	}
	// Network configuration registers round-trip.
	w.writeBlock(c, w6BlockCommon, w6SHAR, 0x02, 0x11, 0x22, 0x33, 0x44, 0x55)
	if got := w.readBlock(c, w6BlockCommon, w6SHAR, 6); string(got) != string([]byte{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}) {
		t.Fatalf("SHAR = % x", got)
	}
}

func TestW6300SocketOpenClose(t *testing.T) {
	w, c := newTestW6300()

	// UDP socket: Sn_MR = 2, then Sn_CR = OPEN.
	w.writeBlock(c, w6BlockRegs, w6SnMR, 2)
	w.writeBlock(c, w6BlockRegs, w6SnCR, w6CmdOpen)
	if got := w.readBlock(c, w6BlockRegs, w6SnSR, 1); got[0] != w6StatusUDP {
		t.Fatalf("status after open = %#x", got[0])
	}
	w.writeBlock(c, w6BlockRegs, w6SnCR, w6CmdClose)
	if got := w.readBlock(c, w6BlockRegs, w6SnSR, 1); got[0] != w6StatusClosed {
		t.Fatalf("status after close = %#x", got[0])
	}
}

func TestW6300BufferPointers(t *testing.T) {
	w, c := newTestW6300()
	// Default 4 KB TX buffer, all free.
	if got := w.readBlock(c, w6BlockRegs, w6SnTXFSR, 2); int(got[0])<<8|int(got[1]) != 4096 {
		t.Fatalf("TX free = % x", got)
	}
	// Write three bytes into the TX block and publish the write pointer.
	w.writeBlock(c, w6BlockTX, 0, 'a', 'b', 'c')
	w.writeBlock(c, w6BlockRegs, w6SnTXWR, 0, 3)
	if got := w.readBlock(c, w6BlockRegs, w6SnTXWR, 2); got[0] != 0 || got[1] != 3 {
		t.Fatalf("TX write ptr = % x", got)
	}
	if got := w.readBlock(c, w6BlockRegs, w6SnTXFSR, 2); int(got[0])<<8|int(got[1]) != 4093 {
		t.Fatalf("TX free = % x", got)
	}
	if got := w.readBlock(c, w6BlockTX, 0, 3); string(got) != "abc" {
		t.Fatalf("TX buffer = %q", got)
	}
}
