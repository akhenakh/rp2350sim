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
	configurePadState(p)
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
	configurePadState(p)
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

// padReg returns the PADS_BANK0 register offset for a GPIO.
func padReg(pin int) uint32 { return 4 + 4*uint32(pin) }

// TestPadInputEnableGatesPeripheralInput checks that IE gates the pad's digital
// input without changing the wire level seen by the board/scope.
func TestPadInputEnableGatesPeripheralInput(t *testing.T) {
	c := New()
	c.Pins.SetExternal(2, true)
	if !c.Pins.Level(2) {
		t.Fatal("external drive is not visible on the wire")
	}
	if got := c.Read32(SIOBase + 0x04); got&(1<<2) != 0 {
		t.Fatal("a pad with input enable clear presented a high input")
	}
	c.Write32(padsBase+padReg(2), 0x056) // IE, isolation clear
	if got := c.Read32(SIOBase + 0x04); got&(1<<2) == 0 {
		t.Fatal("enabled pad input did not reach SIO_GPIO_IN")
	}
	c.Write32(padsBase+padReg(2), 0x016) // IE clear again
	if got := c.Read32(SIOBase + 0x04); got&(1<<2) != 0 {
		t.Fatal("clearing IE did not mask the peripheral input")
	}
	if !c.Pins.Level(2) {
		t.Fatal("clearing IE changed the wire level")
	}
}

// TestPadIsolationLatchesOutputAndPulls checks the RP2350 isolation latch: while
// ISO is set the output enable/level and pull-up state captured on assertion
// stay on the wire, and clearing ISO makes the live controls transparent again.
func TestPadIsolationLatchesOutputAndPulls(t *testing.T) {
	c := New()
	// At reset every pad is isolated with output disabled.
	c.Write32(SIOBase+0x38, 1<<3) // GPIO_OE_SET
	c.Write32(SIOBase+0x18, 1<<3) // GPIO_OUT_SET
	if c.Pins.Level(3) {
		t.Fatal("an isolated pad drove the wire at reset")
	}
	c.Write32(padsBase+padReg(3), 0x056) // release isolation
	if !c.Pins.Level(3) {
		t.Fatal("released pad did not reflect the SIO output")
	}
	c.Write32(padsBase+padReg(3), 0x156) // assert isolation (ISO)
	c.Write32(SIOBase+0x40, 1<<3)        // GPIO_OE_CLR
	c.Write32(SIOBase+0x20, 1<<3)        // GPIO_OUT_CLR
	if !c.Pins.Level(3) {
		t.Fatal("isolated pad did not hold its latched output")
	}
	c.Write32(padsBase+padReg(3), 0x056) // release isolation
	if c.Pins.Level(3) {
		t.Fatal("released pad retained the stale latched output")
	}

	// The pull-up enable is latched the same way.
	c.Write32(padsBase+padReg(4), 0x00A) // pull-up, not isolated
	if !c.Pins.Level(4) {
		t.Fatal("pull-up did not raise the pad")
	}
	c.Write32(padsBase+padReg(4), 0x10A) // isolate, keeping the pull-up
	c.Write32(padsBase+padReg(4), 0x102) // clear the pull-up while isolated
	if !c.Pins.Level(4) {
		t.Fatal("isolated pad did not hold its latched pull-up")
	}
	c.Write32(padsBase+padReg(4), 0x002) // release isolation, pull-up off
	if c.Pins.Level(4) {
		t.Fatal("released pad kept the stale latched pull-up")
	}
}

// TestPadOutputDisableReleasesDrive checks PADS_BANK0.OD has priority over the
// output enable from the core, PIO and PWM.
func TestPadOutputDisableReleasesDrive(t *testing.T) {
	c := New()
	c.Write32(padsBase+padReg(0), 0x056)
	c.Write32(SIOBase+0x38, 1)
	c.Write32(SIOBase+0x18, 1)
	if !c.Pins.Level(0) {
		t.Fatal("core drive did not reach the pad")
	}
	c.Write32(padsBase+padReg(0), 0x0D6) // OD
	if c.Pins.Level(0) {
		t.Fatal("OD did not release the core-driven pin")
	}
	if c.Pins.OE&1 == 0 {
		t.Fatal("OD must not clear the SIO output enable latch")
	}
	c.Write32(padsBase+padReg(0), 0x056)
	if !c.Pins.Level(0) {
		t.Fatal("clearing OD did not restore the core drive")
	}

	// PWM drive goes through the same output enable.
	c.Write32(padsBase+padReg(0), 0x056)
	c.Pins.Func[0] = pwmFuncSel
	c.Write32(pwmBase+0x10, 3)
	c.Write32(pwmBase+0x0C, 3)
	c.Write32(pwmBase+0x04, 0x10)
	c.Write32(pwmBase+0x00, 1)
	c.Step()
	if !c.Pins.Level(0) {
		t.Fatal("enabled PWM did not drive the pad")
	}
	c.Write32(padsBase+padReg(0), 0x0D6)
	if c.Pins.Level(0) {
		t.Fatal("OD did not release the PWM-driven pin")
	}
	c.Write32(padsBase+padReg(0), 0x056)
	if !c.Pins.Level(0) {
		t.Fatal("clearing OD did not restore the PWM drive")
	}

	// PIO drive is gated by the same bit.
	c = New()
	configureWS2812(c, 1)
	c.Write32(pioBase0+0x10, 0xFFFFFFFF)
	for range 10 {
		if c.Pins.Level(0) {
			break
		}
		c.Step()
	}
	if !c.Pins.Level(0) {
		t.Fatal("PIO did not drive the pad")
	}
	c.Write32(padsBase+padReg(0), 0x0D6)
	if c.Pins.Level(0) {
		t.Fatal("OD did not release the PIO-driven pin")
	}
	c.Write32(padsBase+padReg(0), 0x056)
	if !c.Pins.Level(0) {
		t.Fatal("clearing OD did not restore the PIO drive")
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
