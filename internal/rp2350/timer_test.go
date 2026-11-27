package rp2350

import "testing"

const timer0Base = 0x400B0000

// TestTimerArmedClearAlias: ARMED is write-1-to-clear. The +0x3000 alias must
// clear only the requested alarms; the generic read-modify-write cleared every
// other armed bit instead.
func TestTimerArmedClearAlias(t *testing.T) {
	c := New()
	for i := range 4 {
		c.Write32(timer0Base+0x10+uint32(i)*4, 1000+uint32(i))
	}
	if got := c.Read32(timer0Base + 0x20); got != 0xF {
		t.Fatalf("ARMED before clear = %#x", got)
	}
	c.Write32(timer0Base+0x3000+0x20, 1<<2)
	if got := c.Read32(timer0Base + 0x20); got != 0xB {
		t.Fatalf("ARMED after CLR = %#x, want 0xb", got)
	}
}

// TestTimerFarFutureAlarm: an alarm more than 2^31 counter units away must not
// fire on the next tick. The signed comparator treated it as already passed.
func TestTimerFarFutureAlarm(t *testing.T) {
	c := New()
	tm := &c.tmr[0]
	tm.count, tm.lastCycles = 0, 0
	tm.alarm[0] = 0x80000010
	tm.armed = 1
	c.Cycles = cyclesPerMicrosecond // advance exactly one microsecond
	tm.tick(c.Cycles)
	if tm.armed == 0 || tm.intR != 0 {
		t.Fatalf("far-future alarm fired: armed=%#x intr=%#x", tm.armed, tm.intR)
	}
}

// TestTimerAlarmAcrossWrap: an alarm just ahead of a 32-bit counter wrap fires
// when the range is crossed.
func TestTimerAlarmAcrossWrap(t *testing.T) {
	c := New()
	tm := &c.tmr[0]
	tm.count, tm.lastCycles = 0xFFFFFFF0, 0
	tm.alarm[0] = 0x10
	tm.armed = 1
	c.Cycles = 0x20 * cyclesPerMicrosecond
	tm.tick(c.Cycles)
	if tm.armed != 0 || tm.intR&1 == 0 {
		t.Fatalf("wrap alarm did not fire: armed=%#x intr=%#x", tm.armed, tm.intR)
	}
}

// TestTimerTimeWrite checks the documented two-step TIME write: TIMELW is
// latched, and TIMEHW applies both halves.
func TestTimerTimeWrite(t *testing.T) {
	c := New()
	c.Write32(timer0Base+0x04, 0x1234)
	if got := c.Read32(timer0Base + 0x28); got != 0 {
		t.Fatalf("TIMELW changed the counter before TIMEHW: %#x", got)
	}
	c.Write32(timer0Base+0x00, 1)
	if got := c.Read32(timer0Base + 0x28); got != 0x1234 {
		t.Fatalf("TIMERAWL = %#x", got)
	}
	if got := c.Read32(timer0Base + 0x24); got != 1 {
		t.Fatalf("TIMERAWH = %#x", got)
	}
}
