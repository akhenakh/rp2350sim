package rp2350

// adcBlock models the RP2350 ADC. Conversions complete immediately: the CS
// register always reports READY and the RESULT register returns the value
// configured for the selected channel (the board model or UI sets it).
type adcBlock struct {
	chip  *Chip
	block *regBlock
	// values holds 12-bit readings for all nine mux slots, including the
	// package-dependent temperature sensor channel.
	values [9]uint16
}

const (
	adcBase        = 0x400A0000
	adcCSReady     = 0x00000100
	adcCSStartOnce = 0x00000004
	adcCSAinsel    = 12
)

func newADC(c *Chip) *adcBlock {
	a := &adcBlock{chip: c, block: newRegBlock(adcBase, 0x1000)}
	a.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x00: // CS
			return a.block.regs[0] | adcCSReady, true
		case 0x04: // RESULT
			ch := (a.block.regs[0] >> adcCSAinsel) & 0xF
			if ch >= uint32(len(a.values)) {
				return 0, true
			}
			// An attached analog source takes precedence over the static
			// package channel value.
			if pin, ok := a.channelPin(ch); ok && c.Pins.AnalogValid[pin] {
				v := c.Pins.Voltage(pin)
				return uint32(float32(0x0FFF) * v / 3.3), true
			}
			return uint32(a.values[ch]), true
		}
		return 0, false
	}
	a.block.writeFn = func(c *Chip, off, v uint32) {
		if off == 0 {
			// Conversions complete immediately in this model, so the
			// architectural self-clearing strobe never remains set.
			a.block.regs[0] = v &^ adcCSStartOnce
		}
	}
	return a
}

func (a *adcBlock) channelPin(ch uint32) (int, bool) {
	base, channels := 26, 4
	if a.chip.packageSel == 0 {
		base, channels = 40, 8
	}
	if ch >= uint32(channels) {
		return 0, false
	}
	return base + int(ch), true
}

// SetADC sets the 12-bit reading for an ADC input channel.
func (c *Chip) SetADC(channel int, value uint16) {
	if channel < 0 || channel >= len(c.adc.values) {
		return
	}
	if value > 0x0FFF {
		value = 0x0FFF
	}
	c.adc.values[channel] = value
}
