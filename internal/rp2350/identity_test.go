package rp2350

import "testing"

// TestChipIdentity checks the registers firmware uses to identify the chip:
// an A3 RP2350A in a QFN60 package, on the ASIC platform.
func TestChipIdentity(t *testing.T) {
	c := New()
	if got := c.Read32(0x40000000); got != 0x30004927 {
		t.Fatalf("SYSINFO.CHIP_ID = %#x, want 0x30004927", got)
	}
	if got := c.Read32(0x40000004); got != 1 {
		t.Fatalf("SYSINFO.PACKAGE_SEL = %d, want 1 (QFN60/A)", got)
	}
	if got := c.Read32(0x40000008); got != 0 {
		t.Fatalf("SYSINFO.PLATFORM = %d, want 0 (ASIC)", got)
	}
	if got := c.Read32(0x40160000); got != 1 {
		t.Fatalf("TBMAN.PLATFORM = %d, want 1 (ASIC)", got)
	}
}
