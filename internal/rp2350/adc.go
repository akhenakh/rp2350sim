package rp2350

// adcBlock models the RP2350 ADC. Conversions complete immediately: the CS
// register always reports READY and the RESULT register returns the value
// configured for the selected channel (the board model or UI sets it).
type adcBlock struct {
	chip  *Chip
	block *regBlock
	// values holds 12-bit readings for the mux channels.
	values [8]uint16
}

const (
	adcBase     = 0x400A0000
	adcCSReady  = 0x00000100
	adcCSAinsel = 12
)

func newADC(c *Chip) *adcBlock {
	a := &adcBlock{chip: c, block: newRegBlock(adcBase, 0x1000)}
	a.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x00: // CS
			return a.block.regs[0] | adcCSReady, true
		case 0x04: // RESULT
			ch := (a.block.regs[0] >> adcCSAinsel) & 7
			return uint32(a.values[ch]), true
		}
		return 0, false
	}
	return a
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
