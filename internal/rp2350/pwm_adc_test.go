package rp2350

import "testing"

func TestADCReadyAndResult(t *testing.T) {
	c := New()
	c.SetADC(1, 0x800)
	// Select channel 1 and start a one-shot conversion.
	c.Write32(adcBase, 1|1<<12) // EN | AINSEL=1
	c.Write32(adcBase, 1|1<<12|0x4)
	if cs := c.Read32(adcBase); cs&adcCSReady == 0 {
		t.Fatalf("CS = %#x, READY not set", cs)
	}
	if got := c.Read32(adcBase + 4); got != 0x800 {
		t.Fatalf("RESULT = %#x, want 0x800", got)
	}
}

func TestPWMCounterAdvances(t *testing.T) {
	c := New()
	// Slice 0: enable, divide by 1, top 999, compare A = 500.
	c.Write32(pwmBase+0x00, 1)
	c.Write32(pwmBase+0x10, 999)
	c.Write32(pwmBase+0x0C, 500)
	// A pin on slice 0 channel A is GPIO0.
	c.Pins.Func[0] = pwmFuncSel
	const cycles = 300150 // just over 2ms of virtual time
	for range cycles {
		c.Step()
	}
	// The last microsecond tick saw 2001us at 150MHz with a divide-by-1 clock.
	want := uint32(150)
	if ctr := c.Read32(pwmBase + 8); ctr != want {
		t.Fatalf("CTR = %d, want %d", ctr, want)
	}
	if c.Pins.OE&1 == 0 {
		t.Fatal("PWM pin was not driven")
	}
	if c.Pins.Level(0) != (want < 500) {
		t.Fatalf("pin0 level = %v at counter %d", c.Pins.Level(0), want)
	}
}
