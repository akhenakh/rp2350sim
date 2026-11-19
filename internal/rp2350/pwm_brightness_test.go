package rp2350

import "testing"

// TestPinBrightness checks the backlight helper reads the PWM compare value
// and falls back to the plain output level.
func TestPinBrightness(t *testing.T) {
	c := New()
	const pin = 15 // PWM slice 7, channel B
	c.Pins.Func[pin] = pwmFuncSel
	base := uint32(pwmBase + 7*pwmStride)
	c.Write32(base+0x00, 1)       // enable
	c.Write32(base+0x10, 999)     // TOP
	c.Write32(base+0x0C, 500<<16) // CC B = 500
	if got := c.PinBrightness(pin); got < 0.49 || got > 0.51 {
		t.Fatalf("PWM brightness = %v, want ~0.5", got)
	}

	// A plain GPIO is fully on or off.
	c.Pins.Func[pin] = 0x1F
	c.Pins.OE |= 1 << pin
	c.Pins.Out |= 1 << pin
	if got := c.PinBrightness(pin); got != 1 {
		t.Fatalf("GPIO high brightness = %v, want 1", got)
	}
	c.Pins.Out &^= 1 << pin
	if got := c.PinBrightness(pin); got != 0 {
		t.Fatalf("GPIO low brightness = %v, want 0", got)
	}
}
