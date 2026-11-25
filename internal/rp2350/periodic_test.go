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
	// A per-cycle hook still forbids the jump (WS2812B, audio, chip select).
	c.CycleHooks = append(c.CycleHooks, func() {})
	if c.canIdleSkip() {
		t.Fatal("a per-cycle hook must forbid the fast-forward")
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
