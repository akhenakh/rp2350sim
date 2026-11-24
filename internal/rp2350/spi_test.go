package rp2350

import "testing"

func TestSPIInterruptMapAndFIFOStatus(t *testing.T) {
	c := New()
	const base = 0x40080000
	c.OnSPI0 = func(tx byte) byte { return tx + 1 }

	if got := c.Read32(base + 0x0c); got != 0x3 {
		t.Fatalf("SSPSR reset = %#x, want TFE|TNF", got)
	}
	if got := c.Read32(base + 0x18); got != 1<<3 {
		t.Fatalf("SSPRIS reset = %#x, want TXRIS", got)
	}
	for range 3 {
		c.Write32(base+0x08, 0x10)
	}
	if got := c.Read32(base + 0x18); got&(1<<2) != 0 {
		t.Fatalf("RXRIS asserted below half full: %#x", got)
	}
	c.Write32(base+0x08, 0x10)
	if got := c.Read32(base + 0x18); got&(1<<2) == 0 {
		t.Fatalf("RXRIS did not assert at half full: %#x", got)
	}
	c.Write32(base+0x14, 1<<2)
	if got := c.Read32(base + 0x1c); got&(1<<2) == 0 {
		t.Fatalf("SSPMIS did not reflect SSPIMSC: %#x", got)
	}
	if got := c.Read32(base + 0x14); got != 1<<2 {
		t.Fatalf("SSPIMSC readback = %#x", got)
	}
	for range 4 {
		c.Write32(base+0x08, 0x20)
	}
	if got := c.Read32(base + 0x0c); got&(1<<3) == 0 {
		t.Fatalf("SSPSR.RFF not set for eight entries: %#x", got)
	}
	c.Write32(base+0x08, 0x30)
	if got := c.Read32(base + 0x18); got&1 == 0 {
		t.Fatalf("SSPRIS.RORRIS not set at 0x18: %#x", got)
	}
	c.Write32(base+0x14, 1)
	if got := c.Read32(base + 0x1c); got&1 == 0 {
		t.Fatalf("SSPMIS.RORMIS not set: %#x", got)
	}
	c.Write32(base+0x20, 1)
	if got := c.Read32(base + 0x18); got&1 != 0 {
		t.Fatalf("RORIC did not clear overrun: %#x", got)
	}
}

func TestSPIIdentificationRegisters(t *testing.T) {
	c := New()
	for _, base := range []uint32{0x40080000, 0x40088000} {
		for _, tc := range []struct {
			off  uint32
			want uint32
		}{
			{0xfe0, 0x22},
			{0xfe4, 0x10},
			{0xfe8, 0x34},
			{0xfec, 0x00},
			{0xff0, 0x0d},
			{0xff4, 0xf0},
			{0xff8, 0x05},
			{0xffc, 0xb1},
		} {
			if got := c.Read32(base + tc.off); got != tc.want {
				t.Errorf("SPI %#x ID %#x = %#x, want %#x", base, tc.off, got, tc.want)
			}
		}
	}
}
