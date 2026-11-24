package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// configurePin clears the pad isolation latch and enables the digital input,
// matching what firmware does through PADS_BANK0 before using a GPIO.
func configurePin(c *rp2350.Chip, pin int) {
	c.Write32(0x40038004+4*uint32(pin), 0x056)
}
