package rp2350

import "testing"

const dmaBase = 0x50000000

func dmaChanReg(c *Chip, ch, reg int) uint32 {
	return c.Read32(dmaBase + uint32(ch*0x40+reg*4))
}

// TestDMAPacedTransfer checks that a transfer takes one element per cycle,
// reports BUSY meanwhile, and raises the channel's completion interrupt.
func TestDMAPacedTransfer(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true

	src := uint32(0x20000000)
	dst := uint32(0x20000100)
	for i := range 4 {
		c.Write8(src+uint32(i), byte(0xA0+i))
	}

	c.Write32(dmaBase+0x00, src)
	c.Write32(dmaBase+0x04, dst)
	c.Write32(dmaBase+0x08, 4)
	c.Write32(dmaBase+0x404, 1) // INTE0: channel 0
	c.Write32(dmaBase+0x0C, dmaCtrlEN|dmaCtrlINCRRead|dmaCtrlINCRWrit)

	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("BUSY not set after trigger")
	}
	if c.Read8(dst) == 0xA0 {
		t.Fatal("transfer completed before any cycle")
	}

	for range 4 {
		c.Step()
	}
	for i := range 4 {
		if got := c.Read8(dst + uint32(i)); got != byte(0xA0+i) {
			t.Fatalf("byte %d = %#x, want %#x", i, got, 0xA0+i)
		}
	}
	if ctrl := dmaChanReg(c, 0, 3); ctrl&(dmaCtrlBusy|dmaCtrlEN) != 0 {
		t.Fatalf("CTRL_TRIG still busy/enabled: %#x", ctrl)
	}
	if dmaChanReg(c, 0, 2) != 0 {
		t.Fatal("TRANS_COUNT not cleared")
	}
	if c.dma.pending(0)&1 == 0 {
		t.Fatal("completion interrupt not raised")
	}
	if c.NVIC.pending[0]&(1<<10) == 0 {
		t.Fatal("DMA IRQ 0 not pending in the NVIC")
	}
	// Writing INTR clears the channel bit.
	c.Write32(dmaBase+0x400, 1)
	if c.dma.intr&1 != 0 {
		t.Fatal("INTR not cleared by write-1")
	}
}

// TestDMAIRQQuiet checks that IRQ_QUIET suppresses the completion interrupt.
func TestDMAIRQQuiet(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x42)
	c.Write32(dmaBase+0x40, 0x20000000)
	c.Write32(dmaBase+0x44, 0x20000200)
	c.Write32(dmaBase+0x48, 1)
	c.Write32(dmaBase+0x414, 1) // INTE1: channel 1
	c.Write32(dmaBase+0x4C, dmaCtrlEN|dmaCtrlIRQQuiet)

	for range 1 {
		c.Step()
	}
	if got := c.Read8(0x20000200); got != 0x42 {
		t.Fatalf("copy = %#x", got)
	}
	if c.dma.pending(1) != 0 {
		t.Fatal("IRQ_QUIET still raised the completion interrupt")
	}
}
