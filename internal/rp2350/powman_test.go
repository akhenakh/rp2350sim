package rp2350

import "testing"

func writeAONValue(c *Chip, base uint32, v uint64) {
	c.Write32(base+12, uint32(v))
	c.Write32(base+8, uint32(v>>16))
	c.Write32(base+4, uint32(v>>32))
	c.Write32(base, uint32(v>>48))
}

func TestPOWMANAONTimer(t *testing.T) {
	c := New()
	writeAONValue(c, powmanBase+powmanSetTime0, 1000)
	c.Write32(powmanBase+powmanTimer, powmanTimerRun)
	if got := c.Read32(powmanBase + powmanReadLower); got != 1000 {
		t.Fatalf("time = %d, want 1000", got)
	}
	c.Cycles += 5 * 150_000
	if got := c.Read32(powmanBase + powmanReadLower); got != 1005 {
		t.Fatalf("time after 5 ms = %d, want 1005", got)
	}

	writeAONValue(c, powmanBase+powmanAlarm0, 1010)
	c.Write32(powmanBase+powmanINTE, powmanIntTimer)
	c.Write32(powmanBase+powmanTimer, powmanTimerRun|powmanTimerAlarmEnab)
	c.powman.tick(c)
	if c.Read32(powmanBase+powmanINTF)&powmanIntTimer != 0 {
		t.Fatal("alarm fired early")
	}
	c.Cycles += 10 * 150_000
	c.powman.tick(c)
	if c.Read32(powmanBase+powmanINTF)&powmanIntTimer == 0 {
		t.Fatal("alarm did not fire")
	}
	if c.NVIC.pending[powmanIRQ/32]&(1<<uint(powmanIRQ%32)) == 0 {
		t.Fatal("POWMAN timer IRQ not pending")
	}
	// Writing the timer ALARM bit clears the fired state.
	c.Write32(powmanBase+powmanTimer, powmanTimerRun|powmanTimerAlarmEnab|powmanTimerFired)
	if c.Read32(powmanBase+powmanINTF)&powmanIntTimer != 0 {
		t.Fatal("alarm clear did not clear INTF")
	}
}
