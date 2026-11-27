package rp2350

import "testing"

func dmaModeSetup(t *testing.T, count uint32, ctrl uint32) *Chip {
	t.Helper()
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	for i := range 16 {
		c.Write32(SRAMBase+uint32(i*4), 0x1000+uint32(i))
	}
	dmaConfigure(c, 0, SRAMBase, SRAMBase+0x200, count, ctrl)
	c.Write32(dmaBase+0x450, 1)
	return c
}

// TestDMATriggerSelf: MODE=1 completes a block, raises the interrupt, and
// re-arms the channel for the next block.
func TestDMATriggerSelf(t *testing.T) {
	c := dmaModeSetup(t, 2|1<<28, dmaCtrlINCRRead|dmaCtrlINCRWrit|2<<2)
	c.dma.tick()
	c.dma.tick()
	if c.dma.intr&1 == 0 {
		t.Fatal("first block did not raise INTR")
	}
	if c.dma.ch[0].count != 2 || c.dma.active&1 == 0 {
		t.Fatalf("channel not re-armed: count=%d active=%#x", c.dma.ch[0].count, c.dma.active)
	}
	if c.Read32(SRAMBase+0x200) != 0x1000 || c.Read32(SRAMBase+0x204) != 0x1001 {
		t.Fatal("first block copied the wrong words")
	}

	c.Write32(dmaBase+0x400, 1) // clear INTR
	c.dma.tick()
	c.dma.tick()
	if c.dma.intr&1 == 0 {
		t.Fatal("second block did not raise INTR")
	}
	if got := c.Read32(SRAMBase + 0x208); got != 0x1002 {
		t.Fatalf("second block copied %#x", got)
	}
}

// TestDMAEndless: MODE=0xf never decrements, completes, chains or raises an
// interrupt.
func TestDMAEndless(t *testing.T) {
	c := dmaModeSetup(t, 3|0xF<<28, dmaCtrlINCRRead|dmaCtrlINCRWrit|2<<2)
	for range 10 {
		c.dma.tick()
	}
	if c.dma.intr != 0 {
		t.Fatalf("ENDLESS raised INTR=%#x", c.dma.intr)
	}
	if c.dma.active&1 == 0 {
		t.Fatal("ENDLESS channel stopped")
	}
	if c.dma.ch[0].count != 3 {
		t.Fatalf("ENDLESS count = %d, want 3", c.dma.ch[0].count)
	}
	if got := c.Read32(SRAMBase + 0x200 + 9*4); got != 0x1009 {
		t.Fatalf("last endless copy = %#x", got)
	}
}
