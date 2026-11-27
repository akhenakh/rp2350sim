package rp2350

import "testing"

func TestITMStimulus(t *testing.T) {
	c := New()
	var got []TraceEvent
	c.OnTrace = func(ev TraceEvent) { got = append(got, ev) }
	c.Write32(itmTER, 3)
	c.Write32(itmTCR, 1)
	c.Write32(itmBase, uint32('A')|uint32('B')<<8|uint32('C')<<16|uint32('D')<<24)
	if len(got) != 4 || got[0].Text != "A" || got[3].Text != "D" {
		t.Fatalf("trace = %+v", got)
	}
	c.Write32(itmTER, 0)
	c.Write32(itmBase, 'E')
	if len(got) != 4 {
		t.Fatalf("disabled ITM emitted: %+v", got)
	}
}

func TestDWTCycleCounter(t *testing.T) {
	c := New()
	c.Write32(dwtCTRL, 1)
	c.Cycles = 12345
	if got := c.Read32(dwtCYCC); got != 12345 {
		t.Fatalf("CYCCNT = %d", got)
	}
	c.Write32(dwtCYCC, 0)
	c.Cycles = 12445
	if got := c.Read32(dwtCYCC); got != 100 {
		t.Fatalf("CYCCNT after reset = %d, want 100", got)
	}
	c.CPU0.R[15] = 0x10001234
	if got := c.Read32(dwtPCSR); got != 0x10001234 {
		t.Fatalf("PCSR = %#x", got)
	}
	if got := c.Read8(dwtPCSR); got != 0x34 {
		t.Fatalf("PCSR byte = %#x", got)
	}
	if got := c.Read16(dwtPCSR + 2); got != 0x1000 {
		t.Fatalf("PCSR halfword = %#x", got)
	}
}

func TestITMNarrowAccess(t *testing.T) {
	c := New()
	var got []TraceEvent
	c.OnTrace = func(ev TraceEvent) { got = append(got, ev) }
	c.Write32(itmTER, 3)
	c.Write32(itmTCR, 1)

	c.Write8(itmBase+1, 'A')     // byte lane 1 is still port 0
	c.Write16(itmBase+2, 0x4241) // two LE bytes at +2, same port
	if len(got) != 3 || got[0].Port != 0 || got[0].Text != "A" ||
		got[1].Port != 0 || got[1].Text != "A" || got[2].Text != "B" {
		t.Fatalf("events = %+v", got)
	}
	if v := c.Read8(itmBase); v != 1 {
		t.Fatalf("ITM ready byte = %#x, want 1", v)
	}
	if v := c.Read16(itmBase); v != 1 {
		t.Fatalf("ITM ready halfword = %#x, want 1", v)
	}
	if v := c.Read8(itmTER); v != 3 {
		t.Fatalf("TER byte = %#x, want 3", v)
	}
	c.Write8(itmTER, 0)
	c.Write8(itmBase, 'C')
	if len(got) != 3 {
		t.Fatalf("disabled byte ITM emitted: %+v", got)
	}
}

func TestTracePerCoreAndLockAccess(t *testing.T) {
	c := New()
	var got []TraceEvent
	c.OnTrace = func(ev TraceEvent) { got = append(got, ev) }
	c.traceWrite32Core(0, itmTER, 1)
	c.traceWrite32Core(1, itmTER, 1)
	c.traceWrite32Core(0, itmTCR, 1)
	c.traceWrite32Core(1, itmTCR, 1)
	c.traceWrite32Core(0, itmBase, 'A')
	c.traceWrite32Core(1, itmBase, 'B')
	if len(got) != 8 {
		t.Fatalf("events = %d, want 8", len(got))
	}
	if got[0].Core != 0 || got[0].Text != "A" || got[4].Core != 1 || got[4].Text != "B" {
		t.Fatalf("cross-core events = %+v", got)
	}
	c.traceWrite32Core(0, itmTER, 0)
	c.traceWrite32Core(1, itmBase, 'C')
	if len(got) != 12 {
		t.Fatalf("core 1 TER was clobbered: %+v", got)
	}
	c.traceWrite32Core(0, itmLAR, 0xC5ACCE55)
	if v, ok := c.traceRead32Core(0, itmLSR); !ok || v != 0 {
		t.Fatalf("ITM LSR = %#x, ok=%v", v, ok)
	}
	c.traceWrite32Core(1, dwtCTRL, 1)
	c.Cycles = 100
	if got, _ := c.traceRead32Core(1, dwtCYCC); got != 100 {
		t.Fatalf("core 1 CYCCNT = %d", got)
	}
	c.traceWrite32Core(0, dwtCTRL, 0)
	if got, _ := c.traceRead32Core(1, dwtCYCC); got != 100 {
		t.Fatalf("core 0 CTRL clobbered core 1 CYCCNT: %d", got)
	}
}

func TestResetHooks(t *testing.T) {
	c := New()
	count := 0
	c.AddResetHook(func() { count++ })
	c.Reset()
	c.Reset()
	if count != 2 {
		t.Fatalf("reset hooks = %d, want 2", count)
	}
}

func TestDEMCRAccess(t *testing.T) {
	c := New()
	c.Write32(demcr, 1<<24)
	if got := c.Read32(demcr); got != 1<<24 {
		t.Fatalf("DEMCR = %#x", got)
	}
	c.traceWrite32Core(1, demcr, 1<<24)
	if got, _ := c.traceRead32Core(1, demcr); got != 1<<24 {
		t.Fatalf("core 1 DEMCR = %#x", got)
	}
}
