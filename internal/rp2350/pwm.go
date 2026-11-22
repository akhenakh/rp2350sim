package rp2350

// pwmBlock models the RP2350 PWM block: eight slices, each with a counter, a
// wrap value and two compare channels. Counters are derived from the virtual
// clock on the way in, and pins whose function select is PWM are driven from
// the resulting duty cycle.
type pwmBlock struct {
	chip    *Chip
	block   *regBlock
	lastNow uint64
}

const (
	pwmBase    = 0x400A8000
	pwmStride  = 0x14
	pwmSlices  = 12
	pwmFuncSel = 4
)

func newPWM(c *Chip) *pwmBlock {
	p := &pwmBlock{chip: c, block: newRegBlock(pwmBase, 0x1000)}
	p.block.resetFn = func(c *Chip) { p.reset() }
	p.reset()
	return p
}

// pinSlice maps a GPIO to its PWM slice. GPIO 0-15 use slices 0-7,
// GPIO 24-31 use slices 4-7, and GPIO 32-47 use slices 8-11.
func pinSlice(pin int) int {
	if pin >= 32 {
		return 8 + (pin>>1)&3
	}
	return (pin >> 1) & 7
}

// reset restores the architectural reset values. TOP resets to 0xFFFF; a
// programmed TOP of 0 is legal and must not be rewritten.
func (p *pwmBlock) reset() {
	clear(p.block.regs)
	p.lastNow = 0
	for slice := range pwmSlices {
		p.block.regs[(slice*pwmStride+0x10)/4] = 0xFFFF
	}
}

// tick updates slice counters and PWM-driven pins from the microsecond clock.
func (p *pwmBlock) tick(now uint64) {
	if now == p.lastNow {
		return // counters only need microsecond resolution
	}
	p.lastNow = now
	for slice := range pwmSlices {
		csr := p.block.regs[(slice*pwmStride+0)/4]
		if csr&1 == 0 { // CSR_EN
			continue
		}
		div := p.block.regs[(slice*pwmStride+4)/4]
		div16 := (div>>4)*16 + (div & 0xF)
		if div16 == 0 {
			div16 = 16 // DIV_INT 0 is clamped to 1 by the hardware
		}
		top := p.block.regs[(slice*pwmStride+0x10)/4] & 0xFFFF
		ctr := pwmCounter(now*(sysClkHz/1_000_000), div16, top)
		p.block.regs[(slice*pwmStride+8)/4] = uint32(ctr)

		cc := p.block.regs[(slice*pwmStride+0x0C)/4]
		for pin := range 48 {
			if int(p.chip.Pins.Func[pin]) != pwmFuncSel {
				continue
			}
			if pinSlice(pin) != slice {
				continue
			}
			var level bool
			if pin&1 == 0 {
				level = ctr < uint64(cc&0xFFFF)
			} else {
				level = ctr < uint64(cc>>16)
			}
			// A_INV/B_INV are bits 2/3: even pins are channel A, odd B.
			if csr&(0x4<<uint(pin&1)) != 0 {
				level = !level
			}
			p.chip.Pins.setOutput(pin, level)
			p.chip.Pins.setOE(pin, true)
		}
	}
}

// pwmCounter is the slice counter after cycles cycles of the 150 MHz clock.
// The microsecond tick and the per-cycle pin sampler share it so both agree.
func pwmCounter(cycles uint64, div16, top uint32) uint64 {
	// DIV holds the integer divider in the high nibble and the fractional
	// part in sixteenths in the low nibble, so one counter step takes
	// div16/16 CPU cycles.
	return ((cycles * 16) / uint64(div16)) % uint64(top+1)
}

// pinLevel resolves a PWM-driven pin at the current cycle. The microsecond
// tick still maintains the counter register and the pin latch, but sampling a
// scope trace needs the edges that fall between two ticks.
func (p *pwmBlock) pinLevel(pin int) (bool, bool) {
	if pin < 0 || pin > 47 || int(p.chip.Pins.Func[pin]) != pwmFuncSel {
		return false, false
	}
	slice := pinSlice(pin)
	csr := p.block.regs[(slice*pwmStride+0)/4]
	if csr&1 == 0 { // CSR_EN
		return false, false
	}
	div := p.block.regs[(slice*pwmStride+4)/4]
	div16 := (div>>4)*16 + (div & 0xF)
	if div16 == 0 {
		div16 = 16
	}
	top := p.block.regs[(slice*pwmStride+0x10)/4] & 0xFFFF
	ctr := pwmCounter(p.chip.Cycles, div16, top)
	cc := p.block.regs[(slice*pwmStride+0x0C)/4]
	var level bool
	if pin&1 == 0 {
		level = ctr < uint64(cc&0xFFFF)
	} else {
		level = ctr < uint64(cc>>16)
	}
	if csr&(0x4<<uint(pin&1)) != 0 {
		level = !level
	}
	return level, true
}

// PinBrightness returns the effective drive level of a pin as a 0..1 duty
// cycle. It is used for backlight outputs: a plain GPIO reads as fully on or
// off, a PWM pin as its compare/top ratio.
func (c *Chip) PinBrightness(pin int) float32 {
	if pin < 0 || pin > 47 {
		return 0
	}
	if int(c.Pins.Func[pin]) != pwmFuncSel {
		if c.Pins.Level(pin) {
			return 1
		}
		return 0
	}
	slice := pinSlice(pin)
	csr := c.pwm.block.regs[(slice*pwmStride+0)/4]
	if csr&1 == 0 {
		return 0
	}
	top := c.pwm.block.regs[(slice*pwmStride+0x10)/4] & 0xFFFF
	cc := c.pwm.block.regs[(slice*pwmStride+0x0C)/4]
	var cmp uint32
	if pin&1 == 0 {
		cmp = cc & 0xFFFF
	} else {
		cmp = cc >> 16
	}
	duty := float32(cmp) / float32(top+1)
	if csr&(0x4<<uint(pin&1)) != 0 {
		duty = 1 - duty
	}
	if duty < 0 {
		duty = 0
	}
	if duty > 1 {
		duty = 1
	}
	return duty
}
