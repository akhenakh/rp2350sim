package rp2350

import "testing"

func TestPADSResetValuesAndPulls(t *testing.T) {
	c := New()
	const base = 0x40038000
	for pin := range 48 {
		if got := c.Read32(base + 4 + 4*uint32(pin)); got != 0x116 {
			t.Fatalf("GPIO%d PAD reset = %#x, want 0x116", pin, got)
		}
		if !c.Pins.PullDown[pin] || c.Pins.PullUp[pin] {
			t.Fatalf("GPIO%d reset pulls: up=%v down=%v", pin, c.Pins.PullUp[pin], c.Pins.PullDown[pin])
		}
	}
	edges := 0
	c.Pins.OnChange = func(pin int, level bool) {
		if pin == 0 && level {
			edges++
		}
	}
	c.Write32(base+4, 0)
	if c.Pins.PullDown[0] || c.Pins.PullUp[0] {
		t.Fatal("PAD write did not clear the pull-down")
	}
	c.Write32(base+4, 1<<3)
	if !c.Pins.PullUp[0] || !c.Pins.Level(0) {
		t.Fatal("PAD write did not enable the pull-up")
	}
	if edges != 1 {
		t.Fatalf("pull-up produced %d resolved-level edges, want 1", edges)
	}
	c.Reset()
	if got := c.Read32(base + 4); got != 0x116 || !c.Pins.PullDown[0] || c.Pins.PullUp[0] {
		t.Fatalf("GPIO0 PAD after reset = %#x, up=%v down=%v", got, c.Pins.PullUp[0], c.Pins.PullDown[0])
	}
}

func TestPinExternalChangesUseResolvedLevel(t *testing.T) {
	p := NewPinState()
	type edge struct {
		pin   int
		level bool
	}
	var edges []edge
	p.OnChange = func(pin int, level bool) { edges = append(edges, edge{pin, level}) }

	p.SetExternal(3, true)
	p.SetExternal(3, true)
	p.ReleaseExternal(3)
	p.ReleaseExternal(3)
	want := []edge{{3, true}, {3, false}}
	if len(edges) != len(want) {
		t.Fatalf("external edges = %v, want %v", edges, want)
	}
	for i := range want {
		if edges[i] != want[i] {
			t.Fatalf("external edges = %v, want %v", edges, want)
		}
	}

	edges = nil
	p.PullDown[3] = false
	p.SetExternal(3, true)
	p.setOE(3, true) // driven low now masks the external high
	p.setOutput(3, true)
	p.setOE(3, false) // resolves back to the external high
	p.ReleaseExternal(3)
	want = []edge{{3, true}, {3, false}, {3, true}, {3, false}}
	if len(edges) != len(want) {
		t.Fatalf("resolved edges = %v, want %v", edges, want)
	}
	for i := range want {
		if edges[i] != want[i] {
			t.Fatalf("resolved edges = %v, want %v", edges, want)
		}
	}
}

func TestPinExternalChangeMaskedByOutput(t *testing.T) {
	p := NewPinState()
	p.setOE(3, true)
	p.setOutput(3, false)
	var edges []bool
	p.OnChange = func(_ int, level bool) { edges = append(edges, level) }
	p.SetExternal(3, true)
	p.SetExternal(3, false)
	p.ReleaseExternal(3)
	if len(edges) != 0 {
		t.Fatalf("masked external changes produced edges: %v", edges)
	}
	p.setOutput(3, true)
	p.setOE(3, false)
	want := []bool{true, false}
	if len(edges) != len(want) {
		t.Fatalf("resolved edges = %v, want %v", edges, want)
	}
	for i := range want {
		if edges[i] != want[i] {
			t.Fatalf("resolved edges = %v, want %v", edges, want)
		}
	}
}

func TestSIOE2SpinlockWriteMirror(t *testing.T) {
	c := New()
	const lock0 = SIOBase + 0x100
	const lock1 = SIOBase + 0x104
	if got := c.Read32(lock0); got != 1 {
		t.Fatalf("first SPINLOCK0 claim = %#x", got)
	}
	if got := c.Read32(lock1); got != 2 {
		t.Fatalf("first SPINLOCK1 claim = %#x", got)
	}
	c.Read32(SIOBase + 0x180) // reads are correctly decoded
	if got := c.Read32(lock0); got != 0 {
		t.Fatalf("high-region read released SPINLOCK0: %#x", got)
	}
	c.Write32(SIOBase+0x184, 0)
	if got := c.Read32(lock0); got != 0 {
		t.Fatalf("0x184 write released SPINLOCK0 instead of SPINLOCK1: %#x", got)
	}
	if got := c.Read32(lock1); got != 2 {
		t.Fatalf("0x184 write did not release SPINLOCK1: %#x", got)
	}
	c.Write32(SIOBase+0x180, 0)
	if got := c.Read32(lock0); got != 1 {
		t.Fatalf("0x180 write did not release SPINLOCK0: %#x", got)
	}
}
