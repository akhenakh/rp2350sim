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
	if c.Read32(powmanBase+powmanINTR)&powmanIntTimer != 0 {
		t.Fatal("alarm fired early")
	}
	c.Cycles += 10 * 150_000
	c.powman.tick(c)
	if c.Read32(powmanBase+powmanINTR)&powmanIntTimer == 0 {
		t.Fatal("alarm did not fire")
	}
	if c.Read32(powmanBase+powmanINTS)&powmanIntTimer == 0 {
		t.Fatal("masked status does not show the timer")
	}
	if c.NVIC.pending[powmanIRQ/32]&(1<<uint(powmanIRQ%32)) == 0 {
		t.Fatal("POWMAN timer IRQ not pending")
	}
	// Writing the timer ALARM bit clears the fired state.
	c.Write32(powmanBase+powmanTimer, powmanTimerRun|powmanTimerAlarmEnab|powmanTimerFired)
	if c.Read32(powmanBase+powmanINTR)&powmanIntTimer != 0 {
		t.Fatal("alarm clear did not clear INTR")
	}
	if c.NVIC.pending[powmanIRQ/32]&(1<<uint(powmanIRQ%32)) != 0 {
		t.Fatal("alarm clear left the IRQ pending")
	}
}

// TestPOWMANClearAlias follows pico-sdk's powman_clear_bits path: the +0x3000
// alias carries the password in the top half and must still update the timer
// state and clear the ALARM latch.
func TestPOWMANClearAlias(t *testing.T) {
	c := New()
	writeAONValue(c, powmanBase+powmanSetTime0, 1000)
	c.Write32(powmanBase+powmanTimer, powmanTimerRun|powmanTimerAlarmEnab)
	writeAONValue(c, powmanBase+powmanAlarm0, 1001)
	c.Write32(powmanBase+powmanINTE, powmanIntTimer)
	c.Cycles += 2 * 150_000
	c.powman.tick(c)
	if c.Read32(powmanBase+powmanINTR)&powmanIntTimer == 0 {
		t.Fatal("alarm did not fire")
	}

	// Clearing ALARM_ENAB through the CLR alias disables the alarm.
	c.Write32(powmanBase+0x3000+powmanTimer, 0x5AFE0000|powmanTimerAlarmEnab)
	if c.Read32(powmanBase+powmanTimer)&powmanTimerAlarmEnab != 0 {
		t.Fatal("CLR alias did not clear ALARM_ENAB")
	}
	// Clearing ALARM through the same alias clears the latch and IRQ.
	c.Write32(powmanBase+0x3000+powmanTimer, 0x5AFE0000|powmanTimerFired)
	if c.Read32(powmanBase+powmanINTR)&powmanIntTimer != 0 {
		t.Fatal("CLR alias did not clear the ALARM latch")
	}
	if c.NVIC.pending[powmanIRQ/32]&(1<<uint(powmanIRQ%32)) != 0 {
		t.Fatal("POWMAN IRQ still pending after the clear")
	}

	// The timer is still running and can arm a later alarm.
	writeAONValue(c, powmanBase+powmanAlarm0, 1003)
	c.Write32(powmanBase+powmanTimer, powmanTimerRun|powmanTimerAlarmEnab)
	c.Cycles += 2 * 150_000
	c.powman.tick(c)
	if c.Read32(powmanBase+powmanINTR)&powmanIntTimer == 0 {
		t.Fatal("re-armed alarm did not fire")
	}
}

// TestPOWMANForce checks that INTF is the write-forced mask, not the raw
// status register at 0xE0.
func TestPOWMANForce(t *testing.T) {
	c := New()
	c.Write32(powmanBase+powmanINTE, powmanIntTimer)
	c.Write32(powmanBase+powmanINTF, powmanIntTimer)
	if got := c.Read32(powmanBase + powmanINTF); got&powmanIntTimer == 0 {
		t.Fatalf("INTF readback = %#x", got)
	}
	if got := c.Read32(powmanBase + powmanINTR); got&powmanIntTimer != 0 {
		t.Fatalf("forcing set raw INTR: %#x", got)
	}
	if got := c.Read32(powmanBase + powmanINTS); got&powmanIntTimer == 0 {
		t.Fatalf("forced bit missing from INTS: %#x", got)
	}
	c.Write32(powmanBase+powmanINTF, 0)
	if got := c.Read32(powmanBase + powmanINTS); got&powmanIntTimer != 0 {
		t.Fatalf("INTS still forced: %#x", got)
	}
}
