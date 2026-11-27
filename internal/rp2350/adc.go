package rp2350

// adcBlock models the RP2350 ADC: a 12-bit SAR with a 96-clock conversion
// (500 kS/s at the 48 MHz ADC clock), an 8-entry result FIFO, START_ONCE /
// START_MANY, round-robin sampling, FCS threshold interrupt and the ADC DREQ.
// The conversion time is scaled by DIV's integer and fractional divider.
type adcBlock struct {
	chip  *Chip
	block *regBlock
	// values holds 12-bit readings for all nine mux slots, including the
	// package-dependent temperature sensor channel.
	values [9]uint16

	result      uint16
	fifo        [8]uint16
	head, count int
	inProgress  bool
	doneAt      uint64
	irq         bool
}

const (
	adcBase        = 0x400A0000
	adcIRQ         = 35
	adcCSReady     = 0x00000100
	adcCSStartOnce = 0x00000004
	adcCSStartMany = 0x00000008
	adcCSAinsel    = 12
	adcCSRrobin    = 16
	adcCSEn        = 1
	adcCSTSEn      = 1 << 1
	// adcDefaultTemp is the datasheet's 20.1 C example reading at 3.3 V.
	adcDefaultTemp = 891
	adcFCSEn       = 1
	adcFCSShift    = 1 << 1
	adcFCSErr      = 1 << 2
	adcFCSDreqEn   = 1 << 3
	adcFCSEmpty    = 1 << 8
	adcFCSFull     = 1 << 9
	adcFCSUnder    = 1 << 10
	adcFCSOver     = 1 << 11
	adcFCSLevel    = 16
	adcFCSThresh   = 24
	adcFIFODepth   = 8
	// 96 ADC clocks at 48 MHz is 2 us; at the 150 MHz system clock that is
	// 300 cycles. DIV=0 is the 500 kS/s maximum.
	adcConversionCycles = 300
)

func newADC(c *Chip) *adcBlock {
	a := &adcBlock{chip: c, block: newRegBlock(adcBase, 0x1000)}
	a.block.resetFn = func(c *Chip) { a.reset() }
	a.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x00: // CS
			return a.cs(), true
		case 0x04: // RESULT
			return uint32(a.result), true
		case 0x08: // FCS
			return a.fcs(), true
		case 0x0C: // FIFO
			if a.count == 0 {
				a.block.regs[2] |= adcFCSUnder
				a.syncIRQ()
				return 0, true
			}
			v := a.fifo[a.head]
			a.head = (a.head + 1) % adcFIFODepth
			a.count--
			a.syncIRQ()
			if a.block.regs[2]&adcFCSShift != 0 {
				v >>= 4
			}
			return uint32(v), true
		case 0x10: // DIV
			return a.block.regs[4], true
		case 0x14: // INTR
			return a.block.regs[5], true
		case 0x18: // INTE
			return a.block.regs[6], true
		case 0x1C: // INTF
			return a.block.regs[7], true
		case 0x20: // INTS
			return (a.block.regs[5] | a.block.regs[7]) & a.block.regs[6], true
		}
		return 0, false
	}
	a.block.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0x00:
			a.writeCS(v)
		case 0x08:
			a.writeFCS(v)
		case 0x10:
			a.block.regs[4] = v
		case 0x18:
			a.block.regs[6] = v & 1
			a.syncIRQ()
		case 0x1C:
			a.block.regs[7] = v & 1
			a.syncIRQ()
		}
	}
	return a
}

func (a *adcBlock) reset() {
	a.result = 0
	a.head, a.count = 0, 0
	a.inProgress = false
	a.doneAt = 0
	a.irq = false
	a.block.regs[2] = 0
	a.block.regs[4] = 0
	a.block.regs[5] = 0
	a.block.regs[6] = 0
	a.block.regs[7] = 0
	if a.chip != nil {
		a.chip.ClearPendingIRQ(adcIRQ)
	}
}

func (a *adcBlock) cs() uint32 {
	v := a.block.regs[0]
	if a.inProgress {
		v &^= adcCSReady
	} else {
		v |= adcCSReady
	}
	return v
}

func (a *adcBlock) writeCS(v uint32) {
	cs := a.block.regs[0]
	// RROBIN and AINSEL are writable; START_ONCE self-clears; READY and ERR
	// are read-only; ERR_STICKY is write-1-to-clear.
	cs &^= (0x1FF << adcCSRrobin) | (0xF << adcCSAinsel) | 0x7
	cs |= v & (0x1FF << adcCSRrobin)
	cs |= v & (0xF << adcCSAinsel)
	cs |= v & (adcCSEn | adcCSTSEn | adcCSStartMany)
	cs &^= v & (1 << 10)
	a.block.regs[0] = cs
	if v&adcCSEn == 0 {
		a.inProgress = false
		return
	}
	if v&adcCSStartOnce != 0 && v&adcCSStartMany == 0 {
		a.start()
	}
	if v&adcCSStartMany != 0 && !a.inProgress {
		a.start()
	}
}

func (a *adcBlock) fcs() uint32 {
	v := a.block.regs[2]
	v &^= (0xF << adcFCSLevel) | adcFCSEmpty | adcFCSFull
	v |= uint32(a.count) << adcFCSLevel
	if a.count == 0 {
		v |= adcFCSEmpty
	}
	if a.count >= adcFIFODepth {
		v |= adcFCSFull
	}
	return v
}

func (a *adcBlock) writeFCS(v uint32) {
	r := a.block.regs[2]
	mask := uint32(0xF<<adcFCSThresh | adcFCSDreqEn | adcFCSErr | adcFCSShift | adcFCSEn)
	r = (r &^ mask) | (v & mask)
	r &^= v & (adcFCSUnder | adcFCSOver)
	a.block.regs[2] = r
	a.syncIRQ()
}

func (a *adcBlock) div() float64 {
	v := a.block.regs[4]
	div := float64((v>>8)&0xFFFF) + float64(v&0xFF)/256
	if div < 0 {
		div = 0
	}
	return div
}

func (a *adcBlock) start() {
	if a.inProgress || a.block.regs[0]&adcCSEn == 0 {
		return
	}
	a.inProgress = true
	cycles := max(uint64(float64(adcConversionCycles)*(1+a.div())), 1)
	a.doneAt = a.chip.Cycles + cycles
}

// tick advances a conversion. It is called once per chip cycle.
func (a *adcBlock) tick() {
	if !a.inProgress {
		if a.block.regs[0]&adcCSStartMany != 0 {
			a.start()
		}
		return
	}
	if a.chip.Cycles < a.doneAt {
		return
	}
	ch := uint32(a.block.regs[0]>>adcCSAinsel) & 0xF
	a.result = a.sample(ch)
	if a.block.regs[2]&adcFCSEn != 0 {
		a.push(a.result)
	}
	a.inProgress = false
	a.advanceRrobin()
	if a.block.regs[0]&adcCSStartMany != 0 {
		a.start()
	}
}

func (a *adcBlock) push(v uint16) {
	if a.count >= adcFIFODepth {
		a.block.regs[2] |= adcFCSOver
		a.syncIRQ()
		return
	}
	idx := (a.head + a.count) % adcFIFODepth
	a.fifo[idx] = v
	a.count++
	a.syncIRQ()
}

func (a *adcBlock) advanceRrobin() {
	mask := (a.block.regs[0] >> adcCSRrobin) & 0x1FF
	if mask == 0 {
		return
	}
	_, channels := a.packageChannels()
	// AINSEL has channels GPIO inputs followed by the temperature channel.
	limit := channels + 1
	cur := int((a.block.regs[0] >> adcCSAinsel) & 0xF)
	for i := 1; i <= limit; i++ {
		ch := (cur + i) % limit
		if mask&(1<<uint(ch)) != 0 {
			a.block.regs[0] = (a.block.regs[0] &^ (0xF << adcCSAinsel)) | uint32(ch)<<adcCSAinsel
			return
		}
	}
}

func (a *adcBlock) sample(ch uint32) uint16 {
	_, channels := a.packageChannels()
	if int(ch) == channels {
		// The on-die sensor is the channel after the package's GPIO inputs.
		// SetADC can supply a calibrated reading; otherwise the bias must be
		// enabled and a plausible room-temperature value is returned.
		if v := a.values[ch]; v != 0 {
			return v
		}
		if a.block.regs[0]&adcCSTSEn == 0 {
			return 0
		}
		return adcDefaultTemp
	}
	if pin, ok := a.channelPin(ch); ok && a.chip.Pins.AnalogValid[pin] {
		v := a.chip.Pins.Voltage(pin)
		if v < 0 {
			v = 0
		}
		if v > 3.3 {
			v = 3.3
		}
		return uint16(float32(0x0FFF) * v / 3.3)
	}
	if ch >= uint32(len(a.values)) {
		return 0
	}
	return a.values[ch]
}

func (a *adcBlock) syncIRQ() {
	thresh := uint32((a.block.regs[2] >> adcFCSThresh) & 0xF)
	if thresh == 0 {
		thresh = 1
	}
	asserted := a.block.regs[6]&1 != 0 && uint32(a.count) >= thresh
	a.block.regs[5] = 0
	if asserted {
		a.block.regs[5] = 1
	}
	if asserted && !a.irq {
		a.irq = true
		a.chip.PendIRQ(adcIRQ)
	} else if !asserted && a.irq {
		a.irq = false
		a.chip.ClearPendingIRQ(adcIRQ)
	}
}

// dreq reports whether the ADC requests a DMA transfer. The DMA controller
// paces an ADC channel when TREQ_SEL selects DREQ_ADC (48).
func (a *adcBlock) dreq() bool {
	return a.block.regs[2]&adcFCSEn != 0 && a.block.regs[2]&adcFCSDreqEn != 0 && a.count > 0
}

func (a *adcBlock) channelPin(ch uint32) (int, bool) {
	base, channels := a.packageChannels()
	if ch >= uint32(channels) {
		return 0, false
	}
	return base + int(ch), true
}

func (a *adcBlock) packageChannels() (base, channels int) {
	if a.chip.packageSel == 0 {
		return 40, 8
	}
	return 26, 4
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

// ADCValue returns the static 12-bit reading for a channel.
func (c *Chip) ADCValue(channel int) uint16 {
	if channel < 0 || channel >= len(c.adc.values) {
		return 0
	}
	return c.adc.values[channel]
}
