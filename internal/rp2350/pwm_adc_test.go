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
	if cs := c.Read32(adcBase); cs&0x4 != 0 {
		t.Fatalf("CS = %#x, START_ONCE did not self-clear", cs)
	}
	if got := c.Read32(adcBase + 4); got != 0x800 {
		t.Fatalf("RESULT = %#x, want 0x800", got)
	}
}

func TestADCPackageMapping(t *testing.T) {
	tests := []struct {
		name        string
		pkg         Package
		gpioBase    int
		channels    int
		tempChannel int
	}{
		{name: "qfn60", pkg: PackageQFN60, gpioBase: 26, channels: 4, tempChannel: 4},
		{name: "qfn80", pkg: PackageQFN80, gpioBase: 40, channels: 8, tempChannel: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.SetPackage(tt.pkg)
			for ch := range tt.channels {
				pin := tt.gpioBase + ch
				volts := float32(ch) * 0.3
				c.Pins.SetAnalog(pin, volts)
				c.SetADC(ch, 0xABC)
				c.Write32(adcBase, 1|uint32(ch)<<adcCSAinsel)
				want := uint32(float32(0x0FFF) * volts / 3.3)
				if got := c.Read32(adcBase + 4); got != want {
					t.Fatalf("channel %d RESULT = %#x, want %#x", ch, got, want)
				}
			}
			c.SetADC(tt.tempChannel, 0x789)
			c.Write32(adcBase, 1|uint32(tt.tempChannel)<<adcCSAinsel)
			if got := c.Read32(adcBase + 4); got != 0x789 {
				t.Fatalf("temperature RESULT = %#x, want 0x789", got)
			}
			c.Write32(adcBase, 1|15<<adcCSAinsel)
			if got := c.Read32(adcBase + 4); got != 0 {
				t.Fatalf("unbonded channel RESULT = %#x, want 0", got)
			}
		})
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
