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
