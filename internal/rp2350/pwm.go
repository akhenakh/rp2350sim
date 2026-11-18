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
	pwmSlices  = 8
	pwmFuncSel = 4
)

func newPWM(c *Chip) *pwmBlock {
	p := &pwmBlock{chip: c, block: newRegBlock(pwmBase, 0x1000)}
	return p
}

// tick updates slice counters and PWM-driven pins from the microsecond clock.
func (p *pwmBlock) tick(now uint64) {
	if now == p.lastNow {
		return // counters only need microsecond resolution
	}
	p.lastNow = now
	for slice := 0; slice < pwmSlices; slice++ {
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
		if top == 0 {
			top = 0xFFFF
		}
		// DIV holds the integer divider in the high nibble and the fractional
		// part in sixteenths in the low nibble, so one counter step takes
		// div16/16 CPU cycles.
		ctr := ((now * (sysClkHz / 1_000_000) * 16) / uint64(div16)) % uint64(top+1)
		p.block.regs[(slice*pwmStride+8)/4] = uint32(ctr)

		cc := p.block.regs[(slice*pwmStride+0x0C)/4]
		for pin := 0; pin < 48; pin++ {
			if int(p.chip.Pins.Func[pin]) != pwmFuncSel {
				continue
			}
			pinSlice := (pin >> 1) & 7
			if pinSlice != slice {
				continue
			}
			var level bool
			if pin&1 == 0 {
				level = ctr < uint64(cc&0xFFFF)
			} else {
				level = ctr < uint64(cc>>16)
			}
			// Inverted outputs flip the level.
			if csr&(0x8<<uint(pin&1)) != 0 {
				level = !level
			}
			p.chip.Pins.setOutput(pin, level)
			p.chip.Pins.setOE(pin, true)
		}
	}
}
