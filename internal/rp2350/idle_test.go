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

// TestPeriodicHookPauseReleasesIdleSkip checks that a paused sampler neither
// runs nor caps the idle jump, and that resuming restarts its grid.
func TestPeriodicHookPauseReleasesIdleSkip(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	calls := 0
	h := c.AddPeriodicHook(50, func() { calls++ })
	h.SetActive(false)
	c.Run(1000)
	if calls != 0 {
		t.Fatalf("paused hook ran %d times", calls)
	}
	if h.Active() {
		t.Fatal("hook reports active after pausing")
	}
	if c.nextEventCycle() != noPeriodic {
		t.Fatalf("paused hook still bounds the jump: next=%d", c.nextEventCycle())
	}
	h.SetActive(true)
	c.Run(200)
	if calls < 3 {
		t.Fatalf("resumed hook ran %d times in 200 cycles", calls)
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

// TestDeadlineBoundsIdleSkip checks that a live one-shot deadline still allows
// the fast-forward and that Run lands on the exact cycle.
func TestDeadlineBoundsIdleSkip(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	var fired []uint64
	c.AddDeadline(func() { fired = append(fired, c.Cycles) }).Schedule(500)
	if !c.canIdleSkip() {
		t.Fatal("a live deadline must not forbid the fast-forward")
	}
	c.Run(1000)
	if len(fired) != 1 || fired[0] != 500 {
		t.Fatalf("deadline fired at %v, want [500]", fired)
	}
	if c.Cycles != 1000 {
		t.Fatalf("Run advanced %d cycles, want 1000", c.Cycles)
	}
}

// TestDeadlineRescheduleAndCancel covers a callback that re-arms itself and a
// cancelled deadline that must not fire.
func TestDeadlineRescheduleAndCancel(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	var fired []uint64
	var d *Deadline
	d = c.AddDeadline(func() {
		fired = append(fired, c.Cycles)
		if c.Cycles < 300 {
			d.Schedule(300)
		}
	})
	d.Schedule(100)
	c.Run(400)
	if len(fired) != 2 || fired[0] != 100 || fired[1] != 300 {
		t.Fatalf("rescheduled deadline fired at %v, want [100 300]", fired)
	}
	d.Schedule(500)
	d.Cancel()
	c.Run(400)
	if len(fired) != 2 {
		t.Fatalf("cancelled deadline fired: %v", fired)
	}
}

// TestResetCancelsDeadlines checks that a rewound clock cannot leave an
// absolute deadline pending against the old timeline.
func TestResetCancelsDeadlines(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	fired := 0
	d := c.AddDeadline(func() { fired++ })
	d.Schedule(100)
	c.Reset()
	if d.Live() {
		t.Fatal("reset left the deadline armed")
	}
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Run(200)
	if fired != 0 {
		t.Fatalf("deadline fired %d times after reset", fired)
	}
}

// TestHaltedCoreIsIdle checks the halted/lockup path: a core that cannot make
// progress must not keep the whole chip out of the fast-forward.
func TestHaltedCoreIsIdle(t *testing.T) {
	c := New()
	c.CPU0.Halted = true
	c.CPU1.Parked = true
	if !c.canIdleSkip() {
		t.Fatal("a halted core plus a parked core must allow the fast-forward")
	}
}

// busyChip returns a chip whose PWM slice, PIO state machine and 1-cycle
// sampler all require per-cycle execution, plus the sampler's invocation
// counter.
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
	c.AddPeriodicHook(1, func() { ticks++ })
	return c, &ticks
}

// TestChunkedRunMatchesStepWhenBusy checks that Run's chunk boundaries are
// invisible when a 1-cycle sampler forces per-cycle dispatch.
func TestChunkedRunMatchesStepWhenBusy(t *testing.T) {
	chunked, chunkTicks := busyChip()
	if next := chunked.nextEventCycle(); next > chunked.Cycles+1 {
		t.Fatalf("next event at %d, want the 1-cycle grid", next)
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

// TestStalledPIOAllowsIdleSkip covers a PTY-style peripheral that leaves an
// enabled state machine blocked on an empty FIFO while the cores sleep.
func TestStalledPIOAllowsIdleSkip(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	p := c.pio[0]
	if p == nil {
		t.Fatal("PIO0 missing")
	}
	s := &p.sm[0]
	s.enabled = true
	p.refreshActive()
	s.stalled = true
	s.stallKind = pioStallPull
	s.txN = 0
	if !c.canIdleSkip() {
		t.Fatal("empty TX FIFO stall refused the fast-forward")
	}
	s.txN = 1
	if c.canIdleSkip() {
		t.Fatal("a pending TX word refused the fast-forward")
	}
	s.txN = 0
	s.stallKind = pioStallWait
	if c.canIdleSkip() {
		t.Fatal("a WAIT stall refused the fast-forward")
	}
	s.stalled = false
	s.stallKind = pioStallNone
	if c.canIdleSkip() {
		t.Fatal("a running state machine refused the fast-forward")
	}
}
