package rp2350

import "testing"

func TestADCReadyAndResult(t *testing.T) {
	c := New()
	c.SetADC(1, 0x800)
	// Select channel 1 and start a one-shot conversion.
	c.Write32(adcBase, 1|1<<12) // EN | AINSEL=1
	c.Write32(adcBase, 1|1<<12|0x4)
	if cs := c.Read32(adcBase); cs&adcCSReady != 0 {
		t.Fatalf("CS = %#x, READY set before conversion completed", cs)
	}
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Run(1000)
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
			c.CPU0.Sleeping = true
			c.CPU1.Parked = true
			for ch := range tt.channels {
				pin := tt.gpioBase + ch
				volts := float32(ch) * 0.3
				c.Pins.SetAnalog(pin, volts)
				c.SetADC(ch, 0xABC)
				c.Write32(adcBase, 1|uint32(ch)<<adcCSAinsel|0x4)
				c.Run(1000)
				want := uint32(float32(0x0FFF) * volts / 3.3)
				if got := c.Read32(adcBase + 4); got != want {
					t.Fatalf("channel %d RESULT = %#x, want %#x", ch, got, want)
				}
			}
			c.SetADC(tt.tempChannel, 0x789)
			c.Write32(adcBase, 1|uint32(tt.tempChannel)<<adcCSAinsel|0x4)
			c.Run(1000)
			if got := c.Read32(adcBase + 4); got != 0x789 {
				t.Fatalf("temperature RESULT = %#x, want 0x789", got)
			}
			c.Write32(adcBase, 1|15<<adcCSAinsel|0x4)
			c.Run(1000)
			if got := c.Read32(adcBase + 4); got != 0 {
				t.Fatalf("unbonded channel RESULT = %#x, want 0", got)
			}
		})
	}
}

func TestPWMCounterAdvances(t *testing.T) {
	c := New()
	configurePads(c)
	// Slice 0: divide by 1, top 999, compare A = 500, then enable.
	c.Pins.Func[0] = pwmFuncSel // A pin on slice 0 channel A is GPIO0
	c.Write32(pwmBase+0x10, 999)
	c.Write32(pwmBase+0x0C, 500)
	c.Write32(pwmBase+0x00, 1)
	const cycles = 300150 // just over 2ms of virtual time
	for range cycles {
		c.Step()
	}
	// The cycle-accurate counter has advanced 300150 counts modulo 1000.
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

func TestADCTimingAndFIFO(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	for ch := range 4 {
		c.SetADC(ch, uint16(100*(ch+1)))
	}
	// Enable, temperature sensor, all four channels round-robin; FCS.EN below
	// turns on FIFO capture.
	c.Write32(adcBase, adcCSEn|1<<1|0x1000|0xF<<adcCSRrobin)
	c.Write32(adcBase+8, 1) // FCS.EN
	c.Write32(adcBase, adcCSEn|1<<1|0x1000|0xF<<adcCSRrobin|adcCSStartOnce)
	// One conversion takes 300 system cycles at DIV=0.
	for range adcConversionCycles - 1 {
		c.Step()
	}
	if cs := c.Read32(adcBase); cs&adcCSReady != 0 {
		t.Fatalf("READY before 300 cycles: %#x", cs)
	}
	c.Step()
	if cs := c.Read32(adcBase); cs&adcCSReady == 0 {
		t.Fatalf("not READY after 300 cycles: %#x", cs)
	}
	// Free-run long enough for a full FIFO; it is 8 entries deep.
	c.Write32(adcBase, adcCSEn|1<<1|0x1000|0xF<<adcCSRrobin|adcCSStartMany)
	c.Run(adcConversionCycles * 10)
	fcs := c.Read32(adcBase + 8)
	if lvl := (fcs >> adcFCSLevel) & 0xF; lvl != adcFIFODepth {
		t.Fatalf("FIFO level = %d, want %d (FCS %#x)", lvl, adcFIFODepth, fcs)
	}
	if fcs&adcFCSFull == 0 {
		t.Fatalf("FIFO not full: %#x", fcs)
	}
	for i := range adcFIFODepth {
		got := int(c.Read32(adcBase + 12))
		if got < 100 || got > 400 {
			t.Fatalf("FIFO sample %d = %d", i, got)
		}
	}
	if fcs := c.Read32(adcBase + 8); fcs&adcFCSEmpty == 0 {
		t.Fatalf("FIFO not empty after drain: %#x", fcs)
	}
}

func TestADCDividerPacing(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.SetADC(0, 0x123)
	// DIV.INT=1 doubles the 300-cycle conversion period.
	c.Write32(adcBase+0x10, 1<<8)
	c.Write32(adcBase, 1|0x4)
	for range 599 {
		c.Step()
	}
	if cs := c.Read32(adcBase); cs&adcCSReady != 0 {
		t.Fatalf("READY before 600 cycles with DIV=1: %#x", cs)
	}
	c.Step()
	if got := c.Read32(adcBase + 4); got != 0x123 {
		t.Fatalf("RESULT = %#x after divider period", got)
	}
}

func TestADCDmaDrain(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.SetADC(0, 0x321)
	c.Write32(adcBase, 1|0x8)    // EN | START_MANY
	c.Write32(adcBase+8, 1|1<<3) // FCS: EN | DREQ_EN
	dst := uint32(0x20000100)
	ctrl := uint32(dmaCtrlEN | dmaCtrlINCRWrit | 2<<2 | 48<<17) // word, DREQ_ADC
	c.Write32(0x50000010, ctrl)                                 // AL1_CTRL
	c.Write32(0x50000008, 4)                                    // TRANS_COUNT
	c.Write32(0x50000004, dst)                                  // WRITE_ADDR
	c.Write32(0x50000000, adcBase+0x0C)                         // READ_ADDR (trigger via AL3 below)
	c.Write32(0x5000003C, adcBase+0x0C)
	c.Run(4000)
	for i := range 4 {
		if got := c.Read32(dst + uint32(i*4)); got != 0x321 {
			t.Fatalf("DMA sample %d = %#x, want 0x321", i, got)
		}
	}
}

// TestADCTemperatureRoundRobin checks that RROBIN can select the on-die
// temperature channel (after the package's GPIO inputs) and that TS_EN gates
// the default reading.
func TestADCTemperatureRoundRobin(t *testing.T) {
	tests := []struct {
		name string
		pkg  Package
		temp int
	}{
		{name: "qfn60", pkg: PackageQFN60, temp: 4},
		{name: "qfn80", pkg: PackageQFN80, temp: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.SetPackage(tt.pkg)
			c.CPU0.Sleeping = true
			c.CPU1.Parked = true

			c.Write32(adcBase, adcCSEn|uint32(tt.temp)<<adcCSAinsel|adcCSStartOnce)
			c.Run(1000)
			if got := c.Read32(adcBase + 4); got != 0 {
				t.Fatalf("unbiased temperature = %#x, want 0", got)
			}

			c.Write32(adcBase, adcCSEn|adcCSTSEn|uint32(tt.temp)<<adcCSAinsel|adcCSStartOnce)
			c.Run(1000)
			if got := c.Read32(adcBase + 4); got != adcDefaultTemp {
				t.Fatalf("temperature = %#x, want %#x", got, adcDefaultTemp)
			}

			// Round-robin from the last GPIO channel must reach the sensor,
			// not wrap back to channel 0.
			c.Write32(adcBase, adcCSEn|adcCSTSEn|uint32(tt.temp-1)<<adcCSAinsel|
				adcCSStartOnce|1<<uint(tt.temp)<<adcCSRrobin)
			c.Run(1000)
			if got := (c.Read32(adcBase) >> adcCSAinsel) & 0xF; int(got) != tt.temp {
				t.Fatalf("RROBIN selected AINSEL %d, want %d", got, tt.temp)
			}
		})
	}
}

// TestPWMWrapInterrupt checks the shared wrap flag and both IRQ banks.
func TestPWMWrapInterrupt(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true

	// Slice 0: TOP=3, run, IRQ0 enabled.
	c.Write32(pwmBase+0x10, 3)
	c.Write32(pwmBase, 1)
	c.Write32(pwmBase+pwmInte0, 1)
	for range 3 {
		c.Step()
	}
	if got := c.Read32(pwmBase + pwmIntrOffset); got != 0 {
		t.Fatalf("wrap fired early: INTR=%#x", got)
	}
	c.Step()
	if got := c.Read32(pwmBase + pwmIntrOffset); got&1 == 0 {
		t.Fatalf("wrap did not set INTR: %#x", got)
	}
	if got := c.Read32(pwmBase + pwmInts0); got != 1 {
		t.Fatalf("INTS0 = %#x", got)
	}
	if got := c.Read32(pwmBase + pwmInts1); got != 0 {
		t.Fatalf("INTS1 = %#x", got)
	}
	if c.ppb[0].pending[0]&(1<<pwmIRQ0) == 0 {
		t.Fatal("PWM_IRQ_WRAP_0 not pending")
	}

	c.Write32(pwmBase+pwmIntrOffset, 1)
	if c.ppb[0].pending[0]&(1<<pwmIRQ0) != 0 {
		t.Fatal("clearing INTR left the IRQ pending")
	}

	// Slice 9 through the second bank.
	slice9 := uint32(pwmBase + 9*pwmStride)
	c.Write32(slice9+0x10, 1)
	c.Write32(slice9, 1)
	c.Write32(pwmBase+pwmInte1, 1<<9)
	for range 2 {
		c.Step()
	}
	if got := c.Read32(pwmBase + pwmInts1); got != 1<<9 {
		t.Fatalf("INTS1 = %#x, want %#x", got, 1<<9)
	}
	if c.ppb[0].pending[0]&(1<<pwmIRQ1) == 0 {
		t.Fatal("PWM_IRQ_WRAP_1 not pending")
	}
}

// TestPWMWrapPhaseCorrect checks that phase-correct mode raises the wrap when
// the counter returns to zero, not at the top.
func TestPWMWrapPhaseCorrect(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write32(pwmBase+0x10, 3)
	c.Write32(pwmBase, 1|pwmCSRCorrect)
	c.Write32(pwmBase+pwmInte0, 1)
	for range 6 {
		c.Step()
	}
	if got := c.Read32(pwmBase + pwmIntrOffset); got != 0 {
		t.Fatalf("phase-correct wrap fired at the top: %#x", got)
	}
	c.Step() // counter reaches 0
	if got := c.Read32(pwmBase + pwmIntrOffset); got&1 == 0 {
		t.Fatalf("phase-correct wrap did not fire: %#x", got)
	}
}
