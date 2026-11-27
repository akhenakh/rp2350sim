package rp2350

import "testing"

// TestWatchpointOverlap checks that a wide store overlapping a byte
// watchpoint stops, not only a store whose first byte matches.
func TestWatchpointOverlap(t *testing.T) {
	c := New()
	c.Debug = NewDebugger()
	c.Debug.AddWriteWatch(0x20000002, 1)
	c.Write32(0x20000000, 0x11223344)
	if !c.Debug.Stopped {
		t.Fatal("32-bit write did not hit the byte watchpoint")
	}
	if c.Debug.StopReason != "watchpoint" || c.Debug.StopAddr != 0x20000002 {
		t.Fatalf("stop = %q at %#x", c.Debug.StopReason, c.Debug.StopAddr)
	}
}

// TestBreakpointSkipIsPerCore checks that stepping over a breakpoint on one
// core does not hide the same shared-code breakpoint on the other core.
func TestBreakpointSkipIsPerCore(t *testing.T) {
	c := New()
	c.Debug = NewDebugger()
	c.Debug.AddBreakpoint(0x20000010)
	c.Debug.Pause(0, 0x20000010, 0)
	c.Debug.Resume() // skip the breakpoint once on core 0
	if c.Debug.checkPC(1, 0x20000010, 0) != true {
		t.Fatal("core 1 breakpoint was skipped by core 0's step-over")
	}
	c.Debug.Stopped = false
	if c.Debug.checkPC(0, 0x20000010, 0) != false {
		t.Fatal("core 0 breakpoint was not skipped")
	}
}

// TestStepDebugKeepsWatchpoint checks that a watchpoint hit during a single
// step is reported as a watchpoint, not overwritten by the step stop.
func TestStepDebugKeepsWatchpoint(t *testing.T) {
	c := New()
	c.Debug = NewDebugger()
	c.Debug.AddWriteWatch(0x20000100, 1)
	c.Write16(0x20000000, 0x6001) // str r1, [r0]
	c.CPU0.R[0] = 0x20000100
	c.CPU0.R[1] = 0x5A
	c.CPU0.R[15] = 0x20000000
	c.StepDebug(0)
	if c.Debug.StopReason != "watchpoint" {
		t.Fatalf("stop reason = %q, want watchpoint", c.Debug.StopReason)
	}
	if c.Debug.StopAddr != 0x20000100 {
		t.Fatalf("stop address = %#x", c.Debug.StopAddr)
	}
	if got := c.Read32(0x20000100); got != 0x5A {
		t.Fatalf("stored value = %#x", got)
	}
}

// TestPeekRegisterSIO checks that the diagnostic peek does not consume FIFO
// words or claim spinlocks.
func TestPeekRegisterSIO(t *testing.T) {
	c := New()
	c.core = 1
	c.Write32(SIOBase+0x54, 0x12345678) // core 1 pushes to core 0's FIFO
	c.core = 0
	if got, ok := c.PeekRegister(SIOBase + 0x58); !ok || got != 0x12345678 {
		t.Fatalf("peek FIFO_RD = %#x, %v", got, ok)
	}
	if got := c.Read32(SIOBase + 0x50); got&1 == 0 {
		t.Fatal("peek consumed the FIFO word")
	}
	if got := c.Read32(SIOBase + 0x58); got != 0x12345678 {
		t.Fatalf("read FIFO_RD = %#x", got)
	}

	if got, _ := c.PeekRegister(SIOBase + 0x100); got != 1 {
		t.Fatalf("peek SPINLOCK0 = %#x, want 1", got)
	}
	if got := c.Read32(SIOBase + 0x100); got != 1 {
		t.Fatal("peek claimed SPINLOCK0")
	}
}

// TestPeekRegisterSysTick checks that peeking SYST_CSR does not clear the
// latched COUNTFLAG.
func TestPeekRegisterSysTick(t *testing.T) {
	c := New()
	c.ppb[0].tick.ctrl = 1 << 16 // COUNTFLAG latched, timer disabled
	if got, _ := c.PeekRegister(SCSBase + 0x010); got&(1<<16) == 0 {
		t.Fatal("peek SYST_CSR lost COUNTFLAG")
	}
	if got := c.Read32(SCSBase + 0x010); got&(1<<16) == 0 {
		t.Fatal("peek cleared COUNTFLAG")
	}
}
