package rp2350

import "testing"

func TestITMStimulus(t *testing.T) {
	c := New()
	var got []TraceEvent
	c.OnTrace = func(ev TraceEvent) { got = append(got, ev) }
	c.Write32(itmTER, 3)
	c.Write32(itmTCR, 1)
	c.Write32(itmBase, 'A')
	c.Write32(itmBase+4, 'B')
	if len(got) != 2 || got[0].Text != "A" || got[1].Text != "B" {
		t.Fatalf("trace = %+v", got)
	}
	c.Write32(itmTER, 0)
	c.Write32(itmBase, 'C')
	if len(got) != 2 {
		t.Fatalf("disabled ITM emitted: %+v", got)
	}
}

func TestDWTCycleCounter(t *testing.T) {
	c := New()
	c.Cycles = 12345
	if got := c.Read32(dwtCYCC); got != 12345 {
		t.Fatalf("CYCCNT = %d", got)
	}
	if got := c.Read32(dwtPCSR); got != c.CPU0.R[15] {
		t.Fatalf("PCSR = %#x", got)
	}
}

func TestITMNarrowAccess(t *testing.T) {
	c := New()
	var got []TraceEvent
	c.OnTrace = func(ev TraceEvent) { got = append(got, ev) }
	c.Write32(itmTER, 3)
	c.Write32(itmTCR, 1)

	c.Write8(itmBase, 'A')    // byte store on port 0
	c.Write16(itmBase+4, 'B') // halfword store on port 1
	if len(got) != 2 || got[0].Text != "A" || got[0].Port != 0 || got[1].Text != "B" || got[1].Port != 1 {
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
	if len(got) != 2 {
		t.Fatalf("disabled byte ITM emitted: %+v", got)
	}
}
