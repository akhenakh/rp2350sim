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
	if ctrl := dmaChanReg(c, 0, 3); ctrl&dmaCtrlBusy != 0 || ctrl&dmaCtrlEN == 0 {
		t.Fatalf("CTRL_TRIG busy/enable = %#x, want BUSY clear and EN set", ctrl)
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

// TestDMAReTrigger checks that a completed channel keeps EN set and can be
// restarted through MULTI_CHAN_TRIGGER and an AL1 trigger alias.
func TestDMAReTrigger(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x5A)
	c.Write32(dmaBase+0x00, 0x20000000)
	c.Write32(dmaBase+0x04, 0x20000300)
	c.Write32(dmaBase+0x08, 1)
	c.Write32(dmaBase+0x0C, dmaCtrlEN) // no increment: addresses stay put
	c.Step()
	if got := c.Read8(0x20000300); got != 0x5A {
		t.Fatalf("first copy = %#x", got)
	}
	if ctrl := dmaChanReg(c, 0, 3); ctrl&dmaCtrlBusy != 0 || ctrl&dmaCtrlEN == 0 {
		t.Fatalf("CTRL after completion = %#x", ctrl)
	}

	// MULTI_CHAN_TRIGGER starts a channel without touching CTRL.
	c.Write32(dmaBase+0x08, 1)
	c.Write32(dmaBase+0x450, 1)
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("MULTI_CHAN_TRIGGER did not start the channel")
	}
	c.Step()

	// The AL1 count alias triggers too.
	c.Write32(dmaBase+0x1C, 1) // AL1_TRANS_COUNT_TRIG
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("AL1_TRANS_COUNT_TRIG did not start the channel")
	}
	c.Step()
}

// TestDMAChainPendingReload checks that reloading TRANS_COUNT through the AL2
// alias wakes a channel whose chained trigger found a zero count.
func TestDMAChainPendingReload(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x77)
	c.Write8(0x20000001, 0x88)

	// Channel 1: idle, enabled, no count yet.
	c.Write32(dmaBase+0x40, 0x20000001)
	c.Write32(dmaBase+0x44, 0x20000301)
	c.Write32(dmaBase+0x48, 0)
	c.Write32(dmaBase+0x4C, dmaCtrlEN)
	// Channel 0: one byte, chain to channel 1.
	c.Write32(dmaBase+0x00, 0x20000000)
	c.Write32(dmaBase+0x04, 0x20000300)
	c.Write32(dmaBase+0x08, 1)
	c.Write32(dmaBase+0x0C, dmaCtrlEN|1<<13)
	c.Step()
	if c.dma.chainPending&(1<<1) == 0 {
		t.Fatal("chain trigger was not latched")
	}

	// AL2_TRANS_COUNT is the reload path the DVI example uses.
	c.Write32(dmaBase+0x64, 1)
	if dmaChanReg(c, 1, 3)&dmaCtrlBusy == 0 {
		t.Fatal("AL2_TRANS_COUNT did not start the chained channel")
	}
	c.Step()
	if got := c.Read8(0x20000301); got != 0x88 {
		t.Fatalf("chained copy = %#x", got)
	}
}
