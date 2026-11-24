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
// core must leave WFI and the raw alarm interrupt must remain latched.
func TestIdleSkipWakesOnAlarm(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	// TIMER0 ALARM0 at 100 us; writing the alarm value arms it.
	c.Write32(0x400b0040, 1) // timer INTE0
	c.Write32(0xE000E100, 1) // NVIC ISER0
	c.Write32(0x400b0010, 100)
	c.Run(1000 * cyclesPerMicrosecond)
	if c.CPU0.Sleeping {
		t.Fatal("alarm did not wake the core")
	}
	if got := c.Read32(0x400b003c); got&1 == 0 {
		t.Fatal("alarm interrupt is not latched")
	}
}

// TestIdleSkipRefusedByEnabledPWM checks that a running PWM slice keeps the
// per-cycle schedule: fast-forwarding would lose the counter's intermediate
// states and the pin waveform.
func TestIdleSkipRefusedByEnabledPWM(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	if !c.canIdleSkip() {
		t.Fatal("an idle chip should allow the fast-forward")
	}
	c.Write32(pwmBase+0x10, 1000) // TOP
	c.Write32(pwmBase+0x00, 1)    // enable slice 0
	if c.canIdleSkip() {
		t.Fatal("an enabled PWM slice must forbid the fast-forward")
	}
	c.Run(100)
	if got := c.Read32(pwmBase + 0x08); got != 100 {
		t.Fatalf("CTR after Run(100) = %d, want 100", got)
	}
	// With the slice disabled the skip returns and the counter stops.
	c.Write32(pwmBase+0x00, 0)
	if !c.canIdleSkip() {
		t.Fatal("a disabled PWM slice should allow the fast-forward")
	}
	c.Run(1000)
	if got := c.Read32(pwmBase + 0x08); got != 100 {
		t.Fatalf("idle skip advanced a disabled PWM counter to %d", got)
	}
}

// TestIdleSkipRefusedWhilePIORunning checks that an enabled state machine
// prevents the fast-forward.
func TestIdleSkipRefusedWhilePIORunning(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(pioBase0+0x48, 0xA042) // nop
	c.Write32(pioBase0+0x4C, 0xA042) // nop
	c.Write32(pioBase0+0xCC, 1<<12)  // wrap top 1
	c.Write32(pioBase0+0x00, 1)      // enable SM0
	if c.canIdleSkip() {
		t.Fatal("a running PIO state machine must forbid the fast-forward")
	}
	c.Write32(pioBase0+0x00, 0) // disable
	if !c.canIdleSkip() {
		t.Fatal("a stopped PIO block should allow the fast-forward")
	}
}

// TestIdleSkipRefusedByCycleHook checks that a device sampling pins every
// cycle keeps the per-cycle schedule.
func TestIdleSkipRefusedByCycleHook(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	ticks := 0
	c.CycleHooks = append(c.CycleHooks, func() { ticks++ })
	if c.canIdleSkip() {
		t.Fatal("a cycle hook must forbid the fast-forward")
	}
	c.Run(1000)
	if ticks != 1000 {
		t.Fatalf("cycle hook ran %d times for Run(1000)", ticks)
	}
}

// busyChip returns a chip whose PWM slice, PIO state machine and cycle hook
// all require per-cycle execution, plus the hook's invocation counter.
func busyChip() (*Chip, *int) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(pwmBase+0x10, 1000)
	c.Write32(pwmBase+0x0C, 500)
	c.Write32(pwmBase+0x00, 1)
	for addr, instr := range map[uint32]uint32{0x48: 0xA042, 0x4C: 0xA042, 0x50: 0xA042} {
		c.Write32(pioBase0+addr, instr)
	}
	c.Write32(pioBase0+0xCC, 2<<12) // wrap top 2
	c.Write32(pioBase0+0x00, 1)     // enable SM0
	ticks := 0
	c.CycleHooks = append(c.CycleHooks, func() { ticks++ })
	return c, &ticks
}

// TestChunkedRunMatchesStepWhenBusy checks that Run's chunk boundaries are
// invisible when the fast-forward is forbidden.
func TestChunkedRunMatchesStepWhenBusy(t *testing.T) {
	chunked, chunkTicks := busyChip()
	if chunked.canIdleSkip() {
		t.Fatal("busy chip must not fast-forward")
	}
	for _, n := range []uint64{17, 3, 80} {
		chunked.Run(n)
	}

	stepped, stepTicks := busyChip()
	for range 100 {
		stepped.Step()
	}

	if chunked.Cycles != stepped.Cycles {
		t.Fatalf("cycles: chunked %d, stepped %d", chunked.Cycles, stepped.Cycles)
	}
	if *chunkTicks != *stepTicks || *stepTicks != 100 {
		t.Fatalf("cycle hooks: chunked %d, stepped %d, want 100", *chunkTicks, *stepTicks)
	}
	if a, b := chunked.Read32(pwmBase+0x08), stepped.Read32(pwmBase+0x08); a != b || a != 100 {
		t.Fatalf("PWM CTR: chunked %d, stepped %d, want 100", a, b)
	}
	pcAddr := uint32(pioBase0 + 0xC8 + 0x0C)
	if a, b := chunked.Read32(pcAddr), stepped.Read32(pcAddr); a != b || a != 100%3 {
		t.Fatalf("PIO SM0 ADDR: chunked %d, stepped %d, want %d", a, b, 100%3)
	}
}
