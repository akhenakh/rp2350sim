package rp2350

import "testing"

func pwmTestChip() *Chip {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	return c
}

func pwmTestConfigure(c *Chip, slice int, top, cc, div, csr uint32) {
	base := uint32(pwmBase + slice*pwmStride)
	c.Write32(base+0x10, top)
	c.Write32(base+0x0C, cc)
	c.Write32(base+0x04, div)
	c.Write32(base+0x00, csr)
}

func TestPWMResetAndDivider(t *testing.T) {
	c := pwmTestChip()
	for slice := range pwmSlices {
		base := uint32(pwmBase + slice*pwmStride)
		if got := c.Read32(base + 0x04); got != 0x10 {
			t.Fatalf("slice %d DIV reset = %#x, want 0x10", slice, got)
		}
		if got := c.Read32(base + 0x10); got != 0xFFFF {
			t.Fatalf("slice %d TOP reset = %#x, want 0xffff", slice, got)
		}
	}

	pwmTestConfigure(c, 0, 999, 500, 0, 1) // DIV_INT=0 means 256
	for range 255 {
		c.Step()
	}
	if got := c.Read32(pwmBase + 0x08); got != 0 {
		t.Fatalf("CTR before first divided clock = %d, want 0", got)
	}
	c.Step()
	if got := c.Read32(pwmBase + 0x08); got != 1 {
		t.Fatalf("CTR after first divided clock = %d, want 1", got)
	}
}

func TestPWMEnableAtNonzeroCycle(t *testing.T) {
	c := pwmTestChip()
	c.Cycles = 1234
	pwmTestConfigure(c, 0, 9, 5, 0x10, 1)
	c.Step()
	if got := c.Read32(pwmBase + 0x08); got != 1 {
		t.Fatalf("CTR after enabling at cycle 1234 = %d, want 1", got)
	}
}

func TestPWMFractionalDividerPhase(t *testing.T) {
	c := pwmTestChip()
	pwmTestConfigure(c, 0, 0xFFFF, 0, 0x21, 1) // 2 + 1/16 cycles per count
	for range 9 {
		c.Step()
	}
	if got := c.Read32(pwmBase + 0x08); got != 4 {
		t.Fatalf("CTR after 9 cycles at DIV=2.0625 = %d, want 4", got)
	}
}

func TestPWMCTRAndControlWriteContinuation(t *testing.T) {
	c := pwmTestChip()
	pwmTestConfigure(c, 0, 1000, 500, 0x10, 1)
	c.Step()
	c.Write32(pwmBase+0x08, 10)
	if got := c.Read32(pwmBase + 0x08); got != 10 {
		t.Fatalf("CTR write readback = %d, want 10", got)
	}
	c.Step()
	if got := c.Read32(pwmBase + 0x08); got != 11 {
		t.Fatalf("CTR after write = %d, want 11", got)
	}
	c.Write32(pwmBase+0x00, 1|1<<2) // A_INV, counter keeps running
	c.Step()
	if got := c.Read32(pwmBase + 0x08); got != 12 {
		t.Fatalf("CTR after CSR write = %d, want 12", got)
	}
}

func TestPWMDoubleBufferedCompareAndTop(t *testing.T) {
	c := pwmTestChip()
	c.Pins.Func[0] = pwmFuncSel
	pwmTestConfigure(c, 0, 3, 2, 0x10, 1)
	c.Step()
	if got := c.Read32(pwmBase + 0x08); got != 1 || !c.Pins.Level(0) {
		t.Fatalf("initial counter/level = %d/%v, want 1/true", got, c.Pins.Level(0))
	}

	// These writes are pending until the counter wraps through zero.
	c.Write32(pwmBase+0x0C, 3)
	c.Write32(pwmBase+0x10, 7)
	c.Step()
	if got := c.Read32(pwmBase + 0x08); got != 2 || c.Pins.Level(0) {
		t.Fatalf("counter/level before wrap = %d/%v, want 2/false", got, c.Pins.Level(0))
	}
	c.Step()
	c.Step() // wrap and latch CC=3, TOP=7
	if got := c.Read32(pwmBase + 0x08); got != 0 || !c.Pins.Level(0) {
		t.Fatalf("counter/level at wrap = %d/%v, want 0/true", got, c.Pins.Level(0))
	}
	for range 5 {
		c.Step()
	}
	if got := c.Read32(pwmBase + 0x08); got != 5 {
		t.Fatalf("counter after latched TOP change = %d, want 5", got)
	}
}

func TestPWMGlobalEnableAndDisable(t *testing.T) {
	c := pwmTestChip()
	c.Pins.Func[0] = pwmFuncSel
	c.Pins.Func[2] = pwmFuncSel
	pwmTestConfigure(c, 0, 3, 2, 0x10, 0)
	pwmTestConfigure(c, 1, 3, 2, 0x10, 0)
	c.Write32(pwmBase+0xF0, 0b11)
	if got := c.Read32(pwmBase + 0xF0); got != 0b11 {
		t.Fatalf("PWM_EN = %#x, want 0x3", got)
	}
	c.Step()
	for _, slice := range []int{0, 1} {
		if got := c.Read32(uint32(pwmBase + slice*pwmStride)); got&1 == 0 {
			t.Fatalf("slice %d CSR_EN not aliased", slice)
		}
		if got := c.Read32(uint32(pwmBase + slice*pwmStride + 0x08)); got != 1 {
			t.Fatalf("slice %d CTR = %d, want 1", slice, got)
		}
	}

	c.Write32(pwmBase+0xF0, 0)
	for _, pin := range []int{0, 2} {
		if c.Pins.OE&(1<<uint(pin)) != 0 || c.Pins.Out&(1<<uint(pin)) != 0 || c.Pins.Level(pin) {
			t.Fatalf("disabled pin %d retained PWM drive: OE=%#x Out=%#x level=%v", pin, c.Pins.OE, c.Pins.Out, c.Pins.Level(pin))
		}
	}
}

func TestPWMDivModes(t *testing.T) {
	t.Run("level", func(t *testing.T) {
		c := pwmTestChip()
		c.Pins.Func[1] = pwmFuncSel
		c.Pins.SetExternal(1, true)
		pwmTestConfigure(c, 0, 10, 0, 0x10, 1|1<<4)
		for range 3 {
			c.Step()
		}
		if got := c.Read32(pwmBase + 0x08); got != 3 {
			t.Fatalf("high-level CTR = %d, want 3", got)
		}
		c.Pins.SetExternal(1, false)
		for range 2 {
			c.Step()
		}
		if got := c.Read32(pwmBase + 0x08); got != 3 {
			t.Fatalf("low-level CTR = %d, want 3", got)
		}
		if c.Pins.OE&(1<<1) != 0 {
			t.Fatal("PWM B pin remained an output in input mode")
		}
	})

	t.Run("rising edge", func(t *testing.T) {
		c := pwmTestChip()
		c.Pins.Func[1] = pwmFuncSel
		c.Pins.SetExternal(1, false)
		pwmTestConfigure(c, 0, 10, 0, 0x10, 1|2<<4)
		c.Step()
		c.Pins.SetExternal(1, true)
		c.Step()
		c.Step()
		if got := c.Read32(pwmBase + 0x08); got != 1 {
			t.Fatalf("CTR after one rising edge = %d, want 1", got)
		}
		c.Pins.SetExternal(1, false)
		c.Step()
		c.Pins.SetExternal(1, true)
		c.Step()
		if got := c.Read32(pwmBase + 0x08); got != 2 {
			t.Fatalf("CTR after two rising edges = %d, want 2", got)
		}
	})

	t.Run("falling edge", func(t *testing.T) {
		c := pwmTestChip()
		c.Pins.Func[1] = pwmFuncSel
		c.Pins.SetExternal(1, true)
		pwmTestConfigure(c, 0, 10, 0, 0x10, 1|3<<4)
		c.Step()
		c.Pins.SetExternal(1, false)
		c.Step()
		c.Step()
		if got := c.Read32(pwmBase + 0x08); got != 1 {
			t.Fatalf("CTR after one falling edge = %d, want 1", got)
		}
		c.Pins.SetExternal(1, true)
		c.Step()
		c.Pins.SetExternal(1, false)
		c.Step()
		if got := c.Read32(pwmBase + 0x08); got != 2 {
			t.Fatalf("CTR after two falling edges = %d, want 2", got)
		}
	})
}

func TestPWMPhaseAdjustAndCorrect(t *testing.T) {
	t.Run("advance and retard", func(t *testing.T) {
		c := pwmTestChip()
		pwmTestConfigure(c, 0, 10, 5, 0x20, 1)
		for range 2 {
			c.Step()
		}
		c.Write32(pwmBase+0x00, 1|1<<7)
		if got := c.Read32(pwmBase + 0x08); got != 2 {
			t.Fatalf("CTR after PH_ADV = %d, want 2", got)
		}
		if got := c.Read32(pwmBase); got&(1<<7) != 0 {
			t.Fatalf("CSR = %#x, PH_ADV did not self-clear", got)
		}
		c.Write32(pwmBase+0x00, 1|1<<6)
		for range 2 {
			c.Step()
		}
		if got := c.Read32(pwmBase + 0x08); got != 2 {
			t.Fatalf("CTR after PH_RET = %d, want 2", got)
		}
		if got := c.Read32(pwmBase); got&(1<<6) != 0 {
			t.Fatalf("CSR = %#x, PH_RET did not self-clear", got)
		}
	})

	t.Run("phase correct", func(t *testing.T) {
		c := pwmTestChip()
		pwmTestConfigure(c, 0, 3, 2, 0x10, 1|1<<1)
		want := []uint32{1, 2, 3, 3, 2, 1, 0, 0, 1}
		for i, count := range want {
			c.Step()
			if got := c.Read32(pwmBase + 0x08); got != count {
				t.Fatalf("cycle %d CTR = %d, want %d", i+1, got, count)
			}
		}
	})
}

func TestPWMDisableReleasesDrive(t *testing.T) {
	c := pwmTestChip()
	c.Pins.Func[0] = pwmFuncSel
	pwmTestConfigure(c, 0, 3, 3, 0x10, 1)
	c.Step()
	if !c.Pins.Level(0) || c.Pins.OE&1 == 0 {
		t.Fatal("enabled PWM did not drive pin 0 high")
	}
	c.Write32(pwmBase, 0)
	if c.Pins.Level(0) || c.Pins.OE&1 != 0 || c.Pins.Out&1 != 0 {
		t.Fatalf("disabled PWM retained drive: level=%v OE=%#x Out=%#x", c.Pins.Level(0), c.Pins.OE, c.Pins.Out)
	}
}

func TestPWMRouteAfterEnable(t *testing.T) {
	c := pwmTestChip()
	pwmTestConfigure(c, 0, 3, 3, 0x10, 1)
	c.Write32(0x40028004, pwmFuncSel) // GPIO0 FUNCSEL=PWM
	if c.Pins.OE&1 == 0 || !c.Pins.Level(0) {
		t.Fatal("routing an enabled slice to PWM did not drive the pin")
	}
	c.Write32(0x40028004, 0x1F) // FUNCSEL_NULL
	if c.Pins.OE&1 != 0 || c.Pins.Out&1 != 0 {
		t.Fatal("leaving PWM retained the peripheral drive")
	}
}
