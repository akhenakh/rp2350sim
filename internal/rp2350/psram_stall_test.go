package rp2350

import "testing"

// A PSRAM stall is virtual time like any other: the per-cycle engines must
// advance with it rather than missing the latency cycles.
func TestPSRAMLatencyAdvancesCycleEngines(t *testing.T) {
	c := pwmTestChip()
	c.ConfigurePSRAM(8*1024*1024, 5, 8)
	pwmTestConfigure(c, 0, 999, 500, 0x10, 1)

	before := c.Cycles
	c.Read32(PSRAMBase)
	if got := c.Cycles - before; got != 5 {
		t.Fatalf("PSRAM read cost = %d cycles, want 5", got)
	}
	if got := c.Read32(pwmBase + 0x08); got != 5 {
		t.Fatalf("PWM counter after PSRAM stall = %d, want 5", got)
	}
}
