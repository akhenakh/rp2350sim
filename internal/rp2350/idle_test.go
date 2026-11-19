package rp2350

import "testing"

// TestIdleSkipAdvancesExactCycles guards the jump arithmetic: Run must still
// advance virtual time by exactly the requested number of cycles.
func TestIdleSkipAdvancesExactCycles(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	for _, n := range []uint64{1, 149, 150, 151, 12_345, 1_000_000} {
		c.Cycles = 0
		c.Run(n)
		if c.Cycles != n {
			t.Fatalf("Run(%d) advanced %d cycles", n, c.Cycles)
		}
	}
}

// TestIdleSkipWakesOnAlarm checks that a timer alarm still ends the skip: the
// core must leave WFI and the timer IRQ must be pending at the alarm time.
func TestIdleSkipWakesOnAlarm(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	// TIMER0 ALARM0 at 100 us; writing the alarm value arms it.
	c.Write32(0x400b0010, 100)
	c.Run(1000 * cyclesPerMicrosecond)
	if c.CPU0.Sleeping {
		t.Fatal("alarm did not wake the core")
	}
	if c.NVIC.pending[0]&1 == 0 {
		t.Fatal("alarm IRQ is not pending")
	}
}
