package rp2350

// configurePadState makes every pad transparent without enabling its input.
// Tests that drive PinState directly use it in place of the PADS writes that
// firmware performs before using a pin.
func configurePadState(p *PinState) {
	p.padISO = 0
}

// configurePads clears pad isolation and enables the digital input on every
// GPIO, matching what gpio_set_function()/Pin.Configure() do before firmware
// uses a pin. Only tests that bypass PADS_BANK0 need it.
func configurePads(c *Chip) {
	for pin := range 48 {
		// IE, SCHMITT, PDE, DRIVE=4 mA; ISO and OD clear.
		c.Write32(padsBase+4+4*uint32(pin), 0x056)
	}
}
