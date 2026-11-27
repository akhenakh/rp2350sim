package rp2350

import "testing"

func TestClocksEnabledOffsets(t *testing.T) {
	c := New()
	for _, off := range []uint32{0xbc, 0xc0} {
		if got := c.Read32(0x40010000 + off); got != 0xffffffff {
			t.Fatalf("CLOCKS offset %#x = %#x, want all enabled", off, got)
		}
	}
	for _, off := range []uint32{0xb0, 0xb4} {
		if got := c.Read32(0x40010000 + off); got != 0 {
			t.Fatalf("obsolete CLOCKS offset %#x = %#x", off, got)
		}
	}
}

func TestQMIResetRegisters(t *testing.T) {
	c := New()
	tests := []struct {
		off  uint32
		want uint32
	}{
		{off: 0x0c, want: 0x40000004},
		{off: 0x10, want: 0x00001000},
		{off: 0x14, want: 0x0000a003},
		{off: 0x18, want: 0x00001000},
		{off: 0x1c, want: 0x0000a002},
		{off: 0x20, want: 0x40000004},
		{off: 0x24, want: 0x00001000},
		{off: 0x28, want: 0x0000a003},
		{off: 0x2c, want: 0x00001000},
		{off: 0x30, want: 0x0000a002},
	}
	for i := range 8 {
		tests = append(tests, struct {
			off  uint32
			want uint32
		}{off: 0x34 + uint32(i)*4, want: 0x04000000 + uint32(i)*0x400})
	}
	check := func() {
		t.Helper()
		for _, tt := range tests {
			if got := c.Read32(qmiBase + tt.off); got != tt.want {
				t.Fatalf("QMI offset %#x = %#x, want %#x", tt.off, got, tt.want)
			}
		}
	}
	check()
	for _, tt := range tests {
		c.Write32(qmiBase+tt.off, ^tt.want)
	}
	c.Reset()
	check()
}

func TestBootRAMWriteOnceORsWrites(t *testing.T) {
	c := New()
	const off = bootramWriteOnce0
	const addr = bootramBase + off
	c.Write32(addr, 0x0000000f)
	c.Write32(addr, 0x000000f0)
	c.Write8(addr+1, 0xaa)
	if got := c.Read32(addr); got != 0x0000aaff {
		t.Fatalf("write-once word = %#x, want 0xaaff", got)
	}
	c.Write32(bootramBase+0x3000+off, 0xff)
	if got := c.Read32(addr); got != 0x0000aaff {
		t.Fatalf("CLR alias cleared write-once bits: %#x", got)
	}
	c.Reset()
	if got := c.Read32(addr); got != 0 {
		t.Fatalf("write-once word after reset = %#x", got)
	}
}

func TestFlashDevinfoDefaultAndNarrowXOR(t *testing.T) {
	c := New()
	const addr = bootramBase + bootramFlashDevinfo
	if got := c.Read16(addr); got != 12<<8 {
		t.Fatalf("default FLASH_DEVINFO = %#x, want CS0 16 MiB", got)
	}
	c.ConfigurePSRAM(2*1024*1024, 0, 19)
	if got := c.Read16(addr); got != 9<<12|12<<8|19 {
		t.Fatalf("PSRAM FLASH_DEVINFO = %#x", got)
	}

	// SDK-equivalent update of the low six-bit CS1 GPIO field.
	old := c.Read16(addr)
	wdata := (old ^ ((old &^ 0x3f) | 8)) & 0x3f
	c.Write16(addr+0x1000, wdata)
	if got := c.Read16(addr); got != 9<<12|12<<8|8 {
		t.Fatalf("FLASH_DEVINFO after narrow XOR = %#x", got)
	}

	// A halfword strobe must leave the other halfword untouched.
	c.Write32(addr, 0x12340c00)
	c.Write16(addr+0x1000, 0x000a)
	c.Write16(addr+0x1002, 0x0005)
	if got := c.Read32(addr); got != 0x12310c0a {
		t.Fatalf("halfword XOR lanes = %#x, want 0x12310c0a", got)
	}
	c.ConfigurePSRAM(0, 0, 0)
	if got := c.Read16(addr); got != 12<<8 {
		t.Fatalf("FLASH_DEVINFO after removing PSRAM = %#x", got)
	}
}

// TestClocksFrequencyCounter checks the FC0 measurement the SDK's
// frequency_count_khz() polls: DONE must set and the result is in kHz.
func TestClocksFrequencyCounter(t *testing.T) {
	c := New()
	measure := func(src uint32) uint32 {
		t.Helper()
		c.Write32(0x40010000+0xa0, src)
		if st := c.Read32(0x40010000 + 0xa4); st&(1<<4) == 0 {
			t.Fatalf("source %d: FC0 DONE did not set (status %#x)", src, st)
		}
		return c.Read32(0x40010000+0xa8) >> 5
	}
	if got := measure(1); got != clkPLLSysKHz {
		t.Fatalf("PLL_SYS = %d kHz, want %d", got, clkPLLSysKHz)
	}
	if got := measure(2); got != clkPLLUSBKHz {
		t.Fatalf("PLL_USB = %d kHz, want %d", got, clkPLLUSBKHz)
	}
	if got := measure(5); got != clkXOSCKHz {
		t.Fatalf("XOSC = %d kHz, want %d", got, clkXOSCKHz)
	}
	// Route clk_sys to PLL_SYS (aux source 0, divide by 1) and measure it.
	c.Write32(0x40010000+0x3c, 1)
	c.Write32(0x40010000+0x40, 1<<8)
	if got := measure(9); got != clkPLLSysKHz {
		t.Fatalf("CLK_SYS = %d kHz, want %d", got, clkPLLSysKHz)
	}
	// A powered-down PLL reads zero.
	c.Write32(0x40050004, 1) // PLL_SYS PWR.PD
	if got := measure(1); got != 0 {
		t.Fatalf("PLL_SYS after power-down = %d kHz, want 0", got)
	}
}
