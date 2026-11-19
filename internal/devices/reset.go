package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// resetAsserted reports whether an active-low reset pin is held low. An
// unconfigured pin is not treated as a reset: firmware may configure the pin
// after the device is attached.
func resetAsserted(c *rp2350.Chip, pin int) bool {
	if pin < 0 || pin > 47 {
		return false
	}
	bit := uint64(1) << uint(pin)
	if c.Pins.OE&bit != 0 {
		return c.Pins.Out&bit == 0
	}
	if c.Pins.ExtValid&bit != 0 {
		return c.Pins.ExtLevel&bit == 0
	}
	return false
}
