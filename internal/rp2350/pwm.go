package rp2350

import "math/bits"

// pwmBlock models the RP2350 PWM block. Counter and divider state advances
// per system clock; pin levels are resolved from that state between register
// ticks so fast outputs do not alias to the microsecond peripheral clock.
type pwmBlock struct {
	chip    *Chip
	block   *regBlock
	slices  [pwmSlices]pwmSliceState
	enabled uint16
	intr    uint32 // raw wrap flags, write-1-to-clear
}

type pwmSliceState struct {
	ctr         uint16
	cc          uint32
	top         uint16
	divCounter  uint16
	fracPhase   uint8
	phaseDown   bool
	phaseHold   bool
	phaseRetard bool
	previousB   bool
}

const (
	pwmBase     = 0x400A8000
	pwmStride   = 0x14
	pwmSlices   = 12
	pwmFuncSel  = 4
	pwmGlobalEN = 0xF0

	pwmCSREN      = 1 << 0
	pwmCSRCorrect = 1 << 1
	pwmCSRAInv    = 1 << 2
	pwmCSRBInv    = 1 << 3
	pwmCSRPhRet   = 1 << 6
	pwmCSRPhAdv   = 1 << 7

	pwmIRQ0       = 8
	pwmIRQ1       = 9
	pwmIntrOffset = 0xF4
	pwmInte0      = 0xF8
	pwmIntf0      = 0xFC
	pwmInts0      = 0x100
	pwmInte1      = 0x104
	pwmIntf1      = 0x108
	pwmInts1      = 0x10C
)

func newPWM(c *Chip) *pwmBlock {
	p := &pwmBlock{chip: c, block: newRegBlock(pwmBase, 0x1000)}
	p.block.resetFn = func(c *Chip) { p.reset() }
	p.block.readFn = p.read
	p.block.writeFn = p.write
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
	p.slices = [pwmSlices]pwmSliceState{}
	p.enabled = 0
	p.intr = 0
	for slice := range pwmSlices {
		p.block.regs[(slice*pwmStride+0x04)/4] = 0x10
		p.block.regs[(slice*pwmStride+0x10)/4] = 0xFFFF
		p.slices[slice].top = 0xFFFF
	}
}

func (p *pwmBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case pwmGlobalEN:
		return uint32(p.enabled), true
	case pwmIntrOffset:
		return p.intr, true
	case pwmInte0, pwmIntf0, pwmInte1, pwmIntf1:
		return p.block.regs[off/4], true
	case pwmInts0:
		return (p.intr | p.block.regs[pwmIntf0/4]) & p.block.regs[pwmInte0/4], true
	case pwmInts1:
		return (p.intr | p.block.regs[pwmIntf1/4]) & p.block.regs[pwmInte1/4], true
	}
	if off >= pwmGlobalEN {
		return 0, false
	}
	slice := int(off / pwmStride)
	if slice >= pwmSlices {
		return 0, false
	}
	switch off % pwmStride {
	case 0x00:
		return p.block.regs[off/4], true
	case 0x04:
		return p.block.regs[off/4] & 0xFFF, true
	case 0x08:
		return uint32(p.slices[slice].ctr), true
	case 0x0C:
		return p.block.regs[off/4], true
	case 0x10:
		return p.block.regs[off/4] & 0xFFFF, true
	}
	return 0, false
}

func (p *pwmBlock) write(c *Chip, off, v uint32) {
	if off == pwmGlobalEN {
		for slice := range pwmSlices {
			csr := p.block.regs[slice*pwmStride/4]
			csr = csr&^1 | (v>>uint(slice))&1
			p.block.regs[slice*pwmStride/4] = csr
			p.applyEnable(slice, csr&pwmCSREN != 0)
		}
		p.block.regs[off/4] = uint32(p.enabled)
		return
	}
	if off == pwmIntrOffset { // raw wrap flags are write-1-to-clear
		p.intr &^= v & 0xFFF
		p.block.regs[off/4] = p.intr
		p.syncIRQ()
		return
	}
	switch off {
	case pwmInte0, pwmIntf0, pwmInte1, pwmIntf1:
		p.block.regs[off/4] = v & 0xFFF
		p.syncIRQ()
		return
	case pwmInts0, pwmInts1:
		return // read-only masked status
	}
	if off >= pwmGlobalEN {
		return
	}
	slice := int(off / pwmStride)
	if slice >= pwmSlices {
		return
	}
	switch off % pwmStride {
	case 0x00:
		p.writeCSR(slice, v)
	case 0x04:
		p.block.regs[off/4] = v & 0xFFF
		s := &p.slices[slice]
		s.fracPhase = 0
		if p.enabled&(1<<uint(slice)) != 0 {
			s.divCounter = uint16(pwmDividerPeriod(p.block.regs[off/4], &s.fracPhase) - 1)
		}
	case 0x08:
		s := &p.slices[slice]
		s.ctr = uint16(v)
		s.phaseHold = false
		s.phaseDown = false
		p.block.regs[off/4] = uint32(s.ctr)
	case 0x0C:
		p.block.regs[off/4] = v
		if p.enabled&(1<<uint(slice)) == 0 {
			p.slices[slice].cc = v
		}
	case 0x10:
		top := uint16(v)
		p.block.regs[off/4] = uint32(top)
		if p.enabled&(1<<uint(slice)) == 0 {
			p.slices[slice].top = top
		}
	}
}

func (p *pwmBlock) writeCSR(slice int, v uint32) {
	advance, retard := v&pwmCSRPhAdv, v&pwmCSRPhRet
	oldMode := (p.block.regs[slice*pwmStride/4] >> 4) & 3
	csr := v & 0xFF &^ (pwmCSRPhAdv | pwmCSRPhRet)
	p.block.regs[slice*pwmStride/4] = csr
	p.applyEnable(slice, csr&pwmCSREN != 0)
	if csr&pwmCSREN == 0 {
		return
	}
	if oldMode == 0 && (csr>>4)&3 != 0 {
		p.slices[slice].previousB = p.pwmBLevel(slice)
	}
	if advance != 0 && !pwmFullSpeed(p.block.regs[slice*pwmStride/4+1]) {
		p.advance(slice)
	}
	if retard != 0 {
		p.slices[slice].phaseRetard = true
		p.block.regs[slice*pwmStride/4] |= pwmCSRPhRet
	}
}

func (p *pwmBlock) applyEnable(slice int, enable bool) {
	bit := uint16(1) << uint(slice)
	wasEnabled := p.enabled&bit != 0
	if enable {
		p.enabled |= bit
		if !wasEnabled {
			s := &p.slices[slice]
			s.fracPhase = 0
			s.divCounter = uint16(pwmDividerPeriod(p.block.regs[slice*pwmStride/4+1], &s.fracPhase) - 1)
			s.previousB = p.pwmBLevel(slice)
			s.phaseRetard = false
			p.block.regs[slice*pwmStride/4] &^= pwmCSRPhRet
		}
	} else {
		p.enabled &^= bit
		p.slices[slice].phaseRetard = false
		p.block.regs[slice*pwmStride/4] &^= pwmCSRPhRet
	}
	p.updatePinDrive(slice)
}

func pwmFullSpeed(div uint32) bool {
	return div>>4 == 1 && div&0xF == 0
}

// pwmDividerPeriod advances the first-order fractional accumulator and returns
// the number of selected input clocks until the next counter enable pulse.
func pwmDividerPeriod(div uint32, phase *uint8) int {
	integer := int(div >> 4)
	if integer == 0 {
		integer = 256
	}
	sum := int(*phase) + int(div&0xF)
	*phase = uint8(sum & 0xF)
	return integer + sum/16
}

func (p *pwmBlock) busy() bool {
	return p.enabled != 0
}

// tick advances every enabled slice once for this system clock.
func (p *pwmBlock) tick() {
	mask := p.enabled
	for mask != 0 {
		slice := bits.TrailingZeros16(mask)
		mask &^= 1 << uint(slice)
		s := &p.slices[slice]
		csr := p.block.regs[slice*pwmStride/4]
		event := true
		if mode := (csr >> 4) & 3; mode != 0 {
			b := p.pwmBLevel(slice)
			switch mode {
			case 1:
				event = b
			case 2:
				event = b && !s.previousB
			case 3:
				event = !b && s.previousB
			}
			s.previousB = b
		}
		if !event {
			continue
		}
		if s.divCounter != 0 {
			s.divCounter--
			continue
		}
		div := p.block.regs[slice*pwmStride/4+1]
		s.divCounter = uint16(pwmDividerPeriod(div, &s.fracPhase) - 1)
		if s.phaseRetard {
			s.phaseRetard = false
			p.block.regs[slice*pwmStride/4] &^= pwmCSRPhRet
			continue
		}
		if p.advance(slice) {
			p.raiseWrap(slice)
		}
	}
}

// advance clocks one slice counter and reports whether it wrapped. In
// phase-correct mode the wrap point is the return to 0, not the top.
func (p *pwmBlock) advance(slice int) bool {
	s := &p.slices[slice]
	csr := p.block.regs[slice*pwmStride/4]
	wrapped := false
	if csr&pwmCSRCorrect == 0 {
		if s.ctr >= s.top {
			s.ctr = 0
			s.cc = p.block.regs[slice*pwmStride/4+3]
			s.top = uint16(p.block.regs[slice*pwmStride/4+4])
			wrapped = true
		} else {
			s.ctr++
		}
	} else if !s.phaseDown {
		if s.phaseHold {
			s.phaseHold = false
			s.phaseDown = true
		} else if s.ctr < s.top {
			s.ctr++
			if s.ctr == s.top {
				s.phaseHold = true
			}
		} else {
			s.phaseHold = true
		}
	} else if s.phaseHold {
		s.phaseHold = false
		s.phaseDown = false
		s.cc = p.block.regs[slice*pwmStride/4+3]
		s.top = uint16(p.block.regs[slice*pwmStride/4+4])
	} else if s.ctr > 0 {
		s.ctr--
		if s.ctr == 0 {
			s.phaseHold = true
			wrapped = true
		}
	} else {
		s.phaseHold = true
	}
	p.block.regs[slice*pwmStride/4+2] = uint32(s.ctr)
	return wrapped
}

// raiseWrap latches the slice's raw wrap flag and updates both IRQ lines.
func (p *pwmBlock) raiseWrap(slice int) {
	p.intr |= 1 << uint(slice)
	p.block.regs[pwmIntrOffset/4] = p.intr
	p.syncIRQ()
}

// syncIRQ recomputes PWM_IRQ_WRAP_0/1 from the raw/forced state.
func (p *pwmBlock) syncIRQ() {
	if (p.intr|p.block.regs[pwmIntf0/4])&p.block.regs[pwmInte0/4] != 0 {
		p.chip.PendIRQ(pwmIRQ0)
	} else {
		p.chip.ClearPendingIRQ(pwmIRQ0)
	}
	if (p.intr|p.block.regs[pwmIntf1/4])&p.block.regs[pwmInte1/4] != 0 {
		p.chip.PendIRQ(pwmIRQ1)
	} else {
		p.chip.ClearPendingIRQ(pwmIRQ1)
	}
}

func (p *pwmBlock) pwmBLevel(slice int) bool {
	for pin := 1; pin < len(p.chip.Pins.Func); pin += 2 {
		if int(p.chip.Pins.Func[pin]) == pwmFuncSel && pinSlice(pin) == slice && p.chip.Pins.rawLevel(pin) {
			return true
		}
	}
	return false
}

func (p *pwmBlock) updatePinDrive(slice int) {
	enabled := p.enabled&(1<<uint(slice)) != 0
	csr := p.block.regs[slice*pwmStride/4]
	inputB := enabled && (csr>>4)&3 != 0
	for pin := range len(p.chip.Pins.Func) {
		if int(p.chip.Pins.Func[pin]) != pwmFuncSel || pinSlice(pin) != slice {
			continue
		}
		if !enabled || inputB && pin&1 != 0 {
			p.chip.Pins.setOutput(pin, false)
			p.chip.Pins.setOE(pin, false)
			continue
		}
		level, _ := p.pinLevel(pin)
		p.chip.Pins.setOutput(pin, level)
		p.chip.Pins.setOE(pin, true)
	}
}

// pinLevel resolves a PWM-driven pin from the current cycle-accurate state.
func (p *pwmBlock) pinLevel(pin int) (bool, bool) {
	if pin < 0 || pin > 47 || int(p.chip.Pins.Func[pin]) != pwmFuncSel {
		return false, false
	}
	slice := pinSlice(pin)
	csr := p.block.regs[slice*pwmStride/4]
	if csr&pwmCSREN == 0 {
		return false, false
	}
	if (csr>>4)&3 != 0 && pin&1 != 0 {
		return false, false
	}
	s := &p.slices[slice]
	var level bool
	if pin&1 == 0 {
		level = uint32(s.ctr) < s.cc&0xFFFF
	} else {
		level = uint32(s.ctr) < s.cc>>16
	}
	if csr&(pwmCSRAInv<<uint(pin&1)) != 0 {
		level = !level
	}
	return level, true
}

// PinIsPWM reports whether a GPIO is routed to a PWM slice.
func (c *Chip) PinIsPWM(pin int) bool {
	return pin >= 0 && pin <= 47 && int(c.Pins.Func[pin]) == pwmFuncSel
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
	csr := c.pwm.block.regs[slice*pwmStride/4]
	if csr&pwmCSREN == 0 || (csr>>4)&3 != 0 && pin&1 != 0 {
		return 0
	}
	s := &c.pwm.slices[slice]
	var cmp uint32
	if pin&1 == 0 {
		cmp = s.cc & 0xFFFF
	} else {
		cmp = s.cc >> 16
	}
	duty := float32(cmp) / float32(uint32(s.top)+1)
	if csr&(pwmCSRAInv<<uint(pin&1)) != 0 {
		duty = 1 - duty
	}
	return min(max(duty, 0), 1)
}
