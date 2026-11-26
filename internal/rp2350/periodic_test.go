package rp2350

import "testing"

// The interval samplers attached by the UI (scope, logic) and the CLI (logic
// analyzer) run on a grid, not once per cycle; these tests pin the cadence and
// the interaction with the idle fast-forward.

func TestPeriodicHookGrid(t *testing.T) {
	c := New()
	var seen []uint64
	c.AddPeriodicHook(100, func() { seen = append(seen, c.Cycles) })
	// Keep one core awake so the step loop, not the idle skip, drives the
	// clock.
	c.SRAM[0], c.SRAM[1] = 0x01, 0x30 // adds r0, #1
	c.SRAM[2], c.SRAM[3] = 0xfd, 0xe7 // b .-2
	c.CPU0.R[15] = SRAMBase
	c.CPU1.Parked = true

	c.Run(1000)
	if len(seen) != 10 {
		t.Fatalf("hook fired %d times in 1000 cycles, want 10", len(seen))
	}
	for i, at := range seen {
		if want := uint64((i + 1) * 100); at != want {
			t.Fatalf("sample %d at cycle %d, want %d", i, at, want)
		}
	}
}

// A periodic hook must not forbid the idle fast-forward, and the jump must
// still land on the hook's grid with only sub-grid jitter.
func TestPeriodicHookAllowsIdleSkip(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	if !c.canIdleSkip() {
		t.Fatal("idle chip refuses the fast-forward")
	}
	var fired int
	c.AddPeriodicHook(75, func() { fired++ })
	if !c.canIdleSkip() {
		t.Fatal("a periodic hook must not forbid the fast-forward")
	}
	c.Run(15000) // 100 µs
	if c.Cycles != 15000 {
		t.Fatalf("cycles = %d, want 15000", c.Cycles)
	}
	if want := 200; fired < want-4 || fired > want {
		t.Fatalf("hook fired %d times, want about %d", fired, want)
	}
	// A one-shot deadline bounds the jump but does not forbid it; the old
	// per-cycle hooks (WS2812B, audio, chip select) did forbid it.
	d := c.AddDeadline(func() {})
	d.Schedule(c.Cycles + 50)
	if !c.canIdleSkip() {
		t.Fatal("a deadline must not forbid the fast-forward")
	}
	c.Run(100)
	if c.Cycles != 15100 {
		t.Fatalf("deadline run ended at %d, want 15100", c.Cycles)
	}
}

// The skip path jumps in one go; a sampler must observe the landing cycle, not
// every skipped slot.
func TestPeriodicHookSkipLandsOnDeadline(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	var last uint64
	c.AddPeriodicHook(1000, func() { last = c.Cycles })
	c.Run(1000)
	if last != 1000 {
		t.Fatalf("sampled at %d, want the 1000-cycle deadline", last)
	}
}

// TestPeriodicHookAddDuringDispatch checks that a callback which appends a
// hook does not leave the current hook pointing at a stale backing array.
func TestPeriodicHookAddDuringDispatch(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	ticks := 0
	added := false
	c.AddPeriodicHook(100, func() {
		ticks++
		if !added {
			added = true
			c.AddPeriodicHook(100, func() { ticks++ })
		}
	})
	c.Run(1000)
	// First hook: 10 samples. Hook added at cycle 100: 9 more.
	if ticks != 19 {
		t.Fatalf("hooks fired %d times, want 19", ticks)
	}
	if c.periodicNext != 1100 {
		t.Fatalf("next deadline = %d, want 1100", c.periodicNext)
	}
}

// TestResetRewindsLazyAnchors checks that a reset clears SysTick and the
// microsecond anchors, not only the periodic-hook grid.
func TestResetRewindsLazyAnchors(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(SCSBase+0x10, 1|4) // ENABLE | CLKSOURCE, no interrupt
	c.Write32(SCSBase+0x14, 1000)
	c.Run(500)
	c.Reset()
	if c.ppb[0].tick.baseCycle != 0 || c.ppb[0].tick.reloadAt != 0 {
		t.Fatalf("SysTick anchor survived reset: base=%d reloadAt=%d",
			c.ppb[0].tick.baseCycle, c.ppb[0].tick.reloadAt)
	}
	if c.nextMicros != cyclesPerMicrosecond || c.lastMicros != 0 {
		t.Fatalf("microsecond anchors after reset: next=%d last=%d", c.nextMicros, c.lastMicros)
	}
	// Re-enable and run: a stale baseCycle would make sync spin on an
	// unsigned underflow instead of returning.
	c.Write32(SCSBase+0x10, 1|4)
	c.Write32(SCSBase+0x14, 99)
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Run(1000)
	if c.Cycles != 1000 {
		t.Fatalf("cycles after reset run = %d, want 1000", c.Cycles)
	}
}

func TestPeriodicHookResetRephases(t *testing.T) {
	c := New()
	var last uint64
	c.AddPeriodicHook(100, func() { last = c.Cycles })
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	c.Run(500)
	if last == 0 {
		t.Fatal("hook did not run")
	}
	c.Reset()
	if c.periodicNext != 100 {
		t.Fatalf("periodic deadline after reset = %d, want 100", c.periodicNext)
	}
	last = 0
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	c.Run(100)
	if last != 100 {
		t.Fatalf("first sample after reset at %d, want 100", last)
	}
}
