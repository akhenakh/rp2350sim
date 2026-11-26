package rp2350

import "testing"

// TestPredecodeSelfModifyingSRAM rewrites an instruction through the bus after
// it has been fetched. The cache must drop the stale entry, which is the
// pattern used by TinyGo's runtime and by flash-resident loaders that copy a
// routine into SRAM and jump into it.
func TestPredecodeSelfModifyingSRAM(t *testing.T) {
	c := New()
	c.CPU1.Parked = true
	c.CPU0.R[13] = SRAMBase + 0x1000
	c.Write16(SRAMBase, 0x2001)   // movs r0, #1
	c.Write16(SRAMBase+2, 0xE7FE) // b .
	c.CPU0.R[15] = SRAMBase
	c.Step()
	if c.CPU0.R[0] != 1 {
		t.Fatalf("r0 = %d before rewrite, want 1", c.CPU0.R[0])
	}

	c.Write16(SRAMBase, 0x2002) // movs r0, #2
	c.CPU0.R[15] = SRAMBase
	c.Step()
	if c.CPU0.R[0] != 2 {
		t.Fatalf("r0 = %d after rewrite, want 2 (stale predecode entry)", c.CPU0.R[0])
	}
}

// TestPredecodeFlashUpdate loads code, runs it, then reloads flash with new
// code at the same address. A generation bump must invalidate both cores.
func TestPredecodeFlashUpdate(t *testing.T) {
	c := New()
	c.CPU1.Parked = true
	if err := c.LoadSegment(FlashBase, []byte{0x03, 0x20, 0xFE, 0xE7}); err != nil {
		t.Fatal(err)
	}
	c.CPU0.R[15] = FlashBase
	c.Step()
	if c.CPU0.R[0] != 3 {
		t.Fatalf("r0 = %d, want 3", c.CPU0.R[0])
	}

	c.LoadFlashImage([]byte{0x04, 0x20})
	c.CPU0.R[15] = FlashBase
	c.Step()
	if c.CPU0.R[0] != 4 {
		t.Fatalf("r0 = %d after flash reload, want 4 (stale predecode entry)", c.CPU0.R[0])
	}
}
