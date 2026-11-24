package rp2350

import (
	"fmt"
	"testing"
)

// The Pico SDK alarm pool queues a timer in software and then forces the
// hardware IRQ (INTF); the handler programs the real alarm. Forcing must both
// latch INTF and raise the NVIC line, and writing 0 must clear it.
func TestTimerForceIRQ(t *testing.T) {
	c := New()
	c.Boot()
	// Enable alarm 3 in the timer and in the NVIC, then force it.
	c.Write32(0xE000E100, 1<<3) // NVIC ISER: IRQ 3
	c.Write32(0x400b0040, 1<<3)
	c.Write32(0x400b0044, 1<<3)
	if got := c.NVIC.PeekPending(); got != 19 { // exception number: IRQ 3 + 16
		t.Fatalf("pending exception = %d, want 19", got)
	}
	if got := c.Read32(0x400b003c); got != 0 {
		t.Fatalf("INTR = %#x, want 0 (force is separate)", got)
	}
	if got := c.Read32(0x400b0048); got != 1<<3 {
		t.Fatalf("INTS = %#x, want 0x8", got)
	}
	// Clearing INTF drops both INTS and the NVIC line.
	c.Write32(0x400b0044, 0)
	if got := c.Read32(0x400b0048); got != 0 {
		t.Fatalf("INTS after clear = %#x", got)
	}
	if c.NVIC.pending[0]&(1<<3) != 0 {
		t.Fatal("clearing INTF left timer IRQ 3 pending")
	}

	// Changing the mask also resynchronizes the line in both directions.
	c.Write32(0x400b0044, 1<<3)
	c.Write32(0x400b0040, 0)
	if c.NVIC.pending[0]&(1<<3) != 0 {
		t.Fatal("masking timer IRQ 3 left it pending")
	}
	c.Write32(0x400b0040, 1<<3)
	if c.NVIC.pending[0]&(1<<3) == 0 {
		t.Fatal("unmasking timer IRQ 3 did not pend it")
	}
}

// An armed alarm raises its IRQ when the virtual clock passes the target.
func TestTimerAlarmFire(t *testing.T) {
	c := New()
	c.Boot()
	c.LoadSegment(0x20000000, []byte{0xfe, 0xe7}) // b .
	c.CPU0.R[15] = 0x20000000
	c.Write32(0x400b0040, 1<<0) // enable alarm 0
	c.Write32(0x400b0010, 10)   // 10 us
	for c.NowMicros() < 20 {
		c.Step()
	}
	if got := c.Read32(0x400b003c); got&1 == 0 {
		t.Fatalf("INTR = %#x, alarm 0 did not fire", got)
	}
	if got := c.NVIC.PeekPending(); got != 0 {
		t.Fatalf("pending irq = %d, want 0", got)
	}
}

func TestTimerReadLatch(t *testing.T) {
	c := New()
	const base = 0x400B0000
	c.Cycles = (uint64(1)<<32)*cyclesPerMicrosecond - cyclesPerMicrosecond
	if lo := c.Read32(base + 0x0C); lo != 0xFFFFFFFF {
		t.Fatalf("TIMELR = %#x, want 0xffffffff", lo)
	}
	if hi := c.Read32(base + 0x08); hi != 0 {
		t.Fatalf("TIMEHR latch = %#x, want 0", hi)
	}
	c.Cycles += 2 * cyclesPerMicrosecond
	if hi := c.Read32(base + 0x24); hi != 1 {
		t.Fatalf("TIMERAWH = %#x, want 1", hi)
	}
	// TIMEHR consumes the latch; a later read sees the live high word.
	if hi := c.Read32(base + 0x08); hi != 1 {
		t.Fatalf("TIMEHR after latch consume = %#x, want live 1", hi)
	}
	c.Cycles = (uint64(2) << 32) * cyclesPerMicrosecond
	if hi := c.Read32(base + 0x08); hi != 2 {
		t.Fatalf("TIMEHR after another wrap = %#x, want live 2", hi)
	}
}

func TestTimerPauseSourceAndLock(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		c.Write32(0x400B0038, 1)
		for range 7 {
			c.Step()
		}
		if got := c.Read32(0x400B000C); got != 7 {
			t.Fatalf("TIMELR = %d, want 7 system cycles", got)
		}
	})

	t.Run("pause", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		for range cyclesPerMicrosecond {
			c.Step()
		}
		if got := c.Read32(0x400B000C); got != 1 {
			t.Fatalf("TIMELR = %d, want 1", got)
		}
		c.Write32(0x400B0030, 1)
		for range 2 * cyclesPerMicrosecond {
			c.Step()
		}
		if got := c.Read32(0x400B000C); got != 1 {
			t.Fatalf("paused TIMELR = %d, want 1", got)
		}
		c.Write32(0x400B0030, 0)
		for range cyclesPerMicrosecond - 1 {
			c.Step()
		}
		if got := c.Read32(0x400B000C); got != 1 {
			t.Fatalf("TIMELR before resumed tick = %d, want 1", got)
		}
		c.Step()
		if got := c.Read32(0x400B000C); got != 2 {
			t.Fatalf("TIMELR after resumed tick = %d, want 2", got)
		}
	})

	t.Run("locked", func(t *testing.T) {
		c := New()
		const base = 0x400B0000
		c.Write32(base+0x10, 5)
		c.Write32(base+0x34, 1)
		c.Write32(base+0x10, 9)
		c.Write32(base+0x30, 1)
		c.Write32(base+0x40, 1)
		c.Write32(base+0x34, 0)
		if got := c.Read32(base + 0x10); got != 5 {
			t.Fatalf("locked ALARM0 = %#x, want 5", got)
		}
		if got := c.Read32(base + 0x30); got != 0 {
			t.Fatalf("locked PAUSE = %#x, want 0", got)
		}
		if got := c.Read32(base + 0x40); got != 0 {
			t.Fatalf("locked INTE = %#x, want 0", got)
		}
		if got := c.Read32(base + 0x34); got != 1 {
			t.Fatalf("LOCKED = %#x, want sticky 1", got)
		}
		c.Reset()
		if got := c.Read32(base + 0x34); got != 0 {
			t.Fatalf("LOCKED after reset = %#x, want 0", got)
		}
	})
}

func TestTimerDebugPause(t *testing.T) {
	for _, core := range []int{0, 1} {
		t.Run(fmt.Sprintf("core%d", core), func(t *testing.T) {
			c := New()
			c.CPU0.Parked = true
			c.CPU1.Parked = true
			c.Debug = NewDebugger()
			c.Debug.Pause(core, 0, c.Cycles)
			if got := c.Read32(0x400B002C); got != 0x6 {
				t.Fatalf("DBGPAUSE reset = %#x, want 0x6", got)
			}
			for range cyclesPerMicrosecond {
				c.Step()
			}
			if got := c.Read32(0x400B000C); got != 0 {
				t.Fatalf("debug-paused TIMELR = %d, want 0", got)
			}
			c.Write32(0x400B002C, 0)
			for range cyclesPerMicrosecond {
				c.Step()
			}
			if got := c.Read32(0x400B000C); got != 1 {
				t.Fatalf("unmasked debug TIMELR = %d, want 1", got)
			}
		})
	}
}
