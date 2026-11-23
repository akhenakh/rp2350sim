package rp2350

import "testing"

// TestBootselDrivesQSPICSN checks the BOOTSEL wiring: the button pulls the
// QSPI chip select low, visible to firmware as SIO_GPIO_HI_IN bit 27
// (QSPI_CSN), which is how the bootrom and the TinyUSB BSP read it.
func TestBootselDrivesQSPICSN(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	const csn = 1 << 27
	if got := c.Read32(SIOBase + 0x08); got&csn == 0 {
		t.Fatalf("QSPI_CSN idle = %#x, want high", got)
	}
	c.SetBootsel(true)
	if got := c.Read32(SIOBase + 0x08); got&csn != 0 {
		t.Fatalf("QSPI_CSN with BOOTSEL held = %#x, want low", got)
	}
	// GPIO32-47 stay in the low half.
	c.SetBootsel(false)
	c.Pins.SetExternal(40, true)
	if got := c.Read32(SIOBase + 0x08); got&(1<<27) == 0 || got&(1<<8) == 0 {
		t.Fatalf("GPIO_HI_IN = %#x, want CSN high and GP40 high", got)
	}
	// A reset does not release a held button (the bootrom samples it).
	c.SetBootsel(true)
	c.Reset()
	if got := c.Read32(SIOBase + 0x08); got&csn != 0 {
		t.Fatalf("QSPI_CSN after reset with BOOTSEL held = %#x", got)
	}
}

// TestBootselAtBoot checks that the reset sample is recorded and survives the
// pin-model reset.
func TestBootselAtBoot(t *testing.T) {
	c := New()
	if c.BootselAtBoot() {
		t.Fatal("BOOTSEL reported at a cold boot")
	}
	c.SetBootsel(true)
	c.Boot()
	if !c.BootselAtBoot() {
		t.Fatal("BOOTSEL held at Boot was not recorded")
	}
	// Releasing the button does not rewrite the previous boot's sample.
	c.SetBootsel(false)
	if !c.BootselAtBoot() {
		t.Fatal("releasing BOOTSEL changed the boot sample")
	}
	c.Boot()
	if c.BootselAtBoot() {
		t.Fatal("BOOTSEL reported at a boot without it held")
	}
}
