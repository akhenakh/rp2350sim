package rp2350

import "testing"

func TestHangDetectorTightLoop(t *testing.T) {
	c := New()
	c.SetHangDetection(300_000)
	c.CPU1.Parked = true
	c.Write16(SRAMBase, 0x6808)   // ldr r0, [r1]
	c.Write16(SRAMBase+2, 0xE7FC) // b SRAMBase
	c.CPU0.R[1] = 0x40050000      // PLL_SYS_CS (never locks in this stub)
	c.CPU0.R[15] = SRAMBase
	c.Run(2_000_000)
	if c.Hang == nil {
		t.Fatal("tight poll loop was not detected")
	}
	if c.Hang.Core != 0 || c.Hang.MMIOAddr != 0x40050000 {
		t.Fatalf("hang = %+v", *c.Hang)
	}
	if !c.Paused {
		t.Fatal("hang did not pause the cores")
	}
	// Re-arming detection must clear the report and resume the cores.
	c.SetHangDetection(300_000)
	if c.Hang != nil || c.Paused {
		t.Fatal("SetHangDetection did not clear the hang")
	}
}

func TestHangDetectorResetClears(t *testing.T) {
	c := New()
	c.SetHangDetection(100_000)
	c.CPU1.Parked = true
	c.Write16(SRAMBase, 0xE7FE) // b .
	c.CPU0.R[15] = SRAMBase
	c.Run(1_000_000)
	if c.Hang == nil {
		t.Fatal("spin loop was not detected")
	}
	c.Reset()
	if c.Hang != nil || c.Paused {
		t.Fatal("reset left the hang report active")
	}
}
