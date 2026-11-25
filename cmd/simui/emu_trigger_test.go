package main

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// TestScopeTriggerRise drives GP5 low/high in 100 us periods and checks that
// the rising-edge trigger freezes a window with the trigger inside it.
// TestScopeTriggerAddsPin checks that arming a trigger on a pin outside the
// channel list adds the channel instead of silently arming a dead trigger.
func TestScopeTriggerAddsPin(t *testing.T) {
	c := rp2350.New()
	tr := devices.NewTrace(c, []int{5}, devices.ScopeInterval, devices.ScopeDepth)
	e := &Emu{scope: tr, scopePins: []int{5}}
	e.SetScopeTrigger(ScopeTriggerConfig{Mode: "rise", Pin: 7})
	pins := tr.Pins()
	if len(pins) != 2 || pins[0] != 5 || pins[1] != 7 {
		t.Fatalf("scope pins = %v, want [5 7]", pins)
	}
}

// TestScopeTriggerSeesZeroBaseline checks that an edge from the attach-instant
// sample at T=0 to the first new sample is not lost.
func TestScopeTriggerSeesZeroBaseline(t *testing.T) {
	const pin = 5
	bit := uint64(1) << pin
	c := rp2350.New()
	tr := devices.NewTrace(c, []int{pin}, devices.ScopeInterval, devices.ScopeDepth)
	e := &Emu{scope: tr, scopePins: []int{pin}}
	e.SetScopeTrigger(ScopeTriggerConfig{Mode: "rise", Pin: pin})
	c.Pins.ExtValid |= bit
	c.Pins.ExtLevel &^= bit
	tr.Tick() // baseline at T=0
	c.Pins.ExtLevel |= bit
	c.Cycles += devices.ScopeInterval
	tr.Tick()
	e.mu.Lock()
	e.checkScopeTriggerLocked()
	found, at := e.frozenScope.found, e.frozenScope.trigT
	e.mu.Unlock()
	if !found || at != devices.ScopeInterval {
		t.Fatalf("baseline edge: found=%v trigT=%d", found, at)
	}
}

// TestScopeLevelTriggers covers fall/high/low, which share the scanner with
// the rise case but freeze on a different sample.
func TestScopeLevelTriggers(t *testing.T) {
	cases := []struct {
		mode  string
		start bool
		want  bool
	}{
		{"fall", true, false},
		{"high", true, true},
		{"low", false, false},
	}
	const pin = 5
	bit := uint64(1) << pin
	for _, tc := range cases {
		c := rp2350.New()
		tr := devices.NewTrace(c, []int{pin}, devices.ScopeInterval, devices.ScopeDepth)
		e := &Emu{scope: tr, scopePins: []int{pin}}
		e.SetScopeTrigger(ScopeTriggerConfig{Mode: tc.mode, Pin: pin})
		c.Pins.ExtValid |= bit
		for i := range 9000 {
			high := tc.start
			if tc.mode == "fall" && i >= 50 {
				high = false
			}
			if high {
				c.Pins.ExtLevel |= bit
			} else {
				c.Pins.ExtLevel &^= bit
			}
			c.Cycles += devices.ScopeInterval
			tr.Tick()
			if i%16 == 0 {
				e.mu.Lock()
				e.checkScopeTriggerLocked()
				e.mu.Unlock()
			}
		}
		if on, _ := e.ScopeTriggered(); !on {
			t.Fatalf("%s trigger did not fire", tc.mode)
		}
		_, samples, _ := e.ScopeData()
		idx := e.ScopeTriggerIndex()
		if idx < 0 || idx >= len(samples) {
			t.Fatalf("%s index %d outside %d samples", tc.mode, idx, len(samples))
		}
		if got := samples[idx].Bit(0); got != tc.want {
			t.Fatalf("%s trigger sample = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestScopeTriggerRise(t *testing.T) {
	const pin = 5
	c := rp2350.New()
	tr := devices.NewTrace(c, []int{pin}, devices.ScopeInterval, devices.ScopeDepth)
	e := &Emu{scope: tr, scopePins: []int{pin}}
	e.SetScopeTrigger(ScopeTriggerConfig{Mode: "rise", Pin: pin})
	bit := uint64(1) << pin
	c.Pins.ExtValid |= bit

	for i := range 9000 {
		if i%100 >= 50 {
			c.Pins.ExtLevel |= bit
		} else {
			c.Pins.ExtLevel &^= bit
		}
		c.Cycles += devices.ScopeInterval
		tr.Tick()
		// Check less often than every sample; the trigger scanner walks the
		// samples added since its cursor.
		if i%16 == 0 {
			e.mu.Lock()
			e.checkScopeTriggerLocked()
			e.mu.Unlock()
		}
	}
	if on, _ := e.ScopeTriggered(); !on {
		t.Fatal("scope trigger did not fire")
	}
	pins, samples, _ := e.ScopeData()
	if len(pins) != 1 || pins[0] != pin {
		t.Fatalf("frozen pins = %v", pins)
	}
	if len(samples) == 0 {
		t.Fatal("empty frozen capture")
	}
	idx := e.ScopeTriggerIndex()
	if idx <= 0 || idx >= len(samples) {
		t.Fatalf("trigger index %d outside %d samples", idx, len(samples))
	}
	if !samples[idx].Bit(0) {
		t.Fatal("trigger sample is not high")
	}
	// More samples must not overwrite the frozen window.
	_, before, _ := e.ScopeData()
	for range 500 {
		c.Cycles += devices.ScopeInterval
		tr.Tick()
		e.mu.Lock()
		e.checkScopeTriggerLocked()
		e.mu.Unlock()
	}
	_, after, _ := e.ScopeData()
	if len(before) != len(after) || before[0] != after[0] || before[len(before)-1] != after[len(after)-1] {
		t.Fatal("scope trigger overwrote the frozen capture")
	}
	e.RearmScope()
	if on, _ := e.ScopeTriggered(); on {
		t.Fatal("rearm did not clear the frozen capture")
	}
	// Rearming must wait for a new edge, not replay the captured history.
	e.mu.Lock()
	e.checkScopeTriggerLocked()
	found := e.frozenScope.found
	e.mu.Unlock()
	if found {
		t.Fatal("rearm triggered on an old sample")
	}
}
