package rp2350

import "testing"

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
	// Clearing INTF drops the line once the pending bit is consumed.
	c.Write32(0x400b0044, 0)
	if got := c.Read32(0x400b0048); got != 0 {
		t.Fatalf("INTS after clear = %#x", got)
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
	if hi := c.Read32(base + 0x08); hi != 0 {
		t.Fatalf("TIMEHR after wrap = %#x, want latched 0", hi)
	}
}
