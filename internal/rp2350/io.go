package rp2350

// PinState holds the externally visible state of the 48 GPIO pins.
type PinState struct {
	Out, OE  uint64 // output latch and output enable, per pin
	Func     [48]uint8
	PullUp   [48]bool
	PullDown [48]bool

	// Bit-parallel summaries used by PIO's always-on input synchronizers.
	pullUpMask uint64
	pwmPins    uint64

	// ExtLevel is a level driven by an external device (or the UI); ExtValid
	// marks which pins are actually driven externally.
	ExtLevel uint64
	ExtValid uint64

	// Analog holds externally applied voltages (0..3.3 V) per pin;
	// AnalogValid marks them. A valid analog source overrides the digital
	// level when the chip is not driving the pin.
	Analog      [48]float32
	AnalogValid [48]bool

	// OnChange is called when a pin's resolved level changes.
	OnChange func(pin int, level bool)

	// LevelOverride, when it returns ok, supplies a pin's level from a
	// peripheral that drives it between microsecond ticks (the PWM slices).
	LevelOverride func(pin int) (level bool, ok bool)
}

func NewPinState() *PinState {
	p := &PinState{}
	p.reset()
	return p
}

func (p *PinState) reset() {
	p.Out = 0
	p.OE = 0
	p.ExtLevel = 0
	p.ExtValid = 0
	p.AnalogValid = [48]bool{}
	p.PullUp = [48]bool{}
	p.PullDown = [48]bool{}
	p.pullUpMask = 0
	p.pwmPins = 0
	for i := range p.PullDown {
		p.PullDown[i] = true
	}
	for i := range p.Func {
		p.Func[i] = 0x1F // FUNCSEL_NULL
	}
}

// Level returns the resolved level of a pin.
func (p *PinState) Level(pin int) bool {
	if pin < 0 || pin > 47 {
		return false
	}
	if p.LevelOverride != nil {
		if level, ok := p.LevelOverride(pin); ok {
			return level
		}
	}
	return p.rawLevel(pin)
}

func (p *PinState) rawLevel(pin int) bool {
	bit := uint64(1) << uint(pin)
	if p.OE&bit != 0 {
		return p.Out&bit != 0
	}
	if p.ExtValid&bit != 0 {
		return p.ExtLevel&bit != 0
	}
	return p.PullUp[pin]
}

// Voltage returns the pin voltage for the scope: the chip's driven level when
// it owns the pin, otherwise the external analog value, otherwise the resolved
// digital level as 0 or 3.3 V.
func (p *PinState) Voltage(pin int) float32 {
	if pin < 0 || pin > 47 {
		return 0
	}
	bit := uint64(1) << uint(pin)
	if p.LevelOverride != nil {
		if level, ok := p.LevelOverride(pin); ok {
			if level {
				return 3.3
			}
			return 0
		}
	}
	if p.OE&bit != 0 {
		if p.Out&bit != 0 {
			return 3.3
		}
		return 0
	}
	if p.AnalogValid[pin] {
		return p.Analog[pin]
	}
	if p.Level(pin) {
		return 3.3
	}
	return 0
}

// SetAnalog applies an external voltage to a pin (0..3.3 V).
func (p *PinState) SetAnalog(pin int, volts float32) {
	if pin < 0 || pin > 47 {
		return
	}
	if volts < 0 {
		volts = 0
	}
	if volts > 3.3 {
		volts = 3.3
	}
	p.Analog[pin] = volts
	p.AnalogValid[pin] = true
}

// ClearAnalog removes an external analog source.
func (p *PinState) ClearAnalog(pin int) {
	if pin >= 0 && pin < 48 {
		p.AnalogValid[pin] = false
	}
}

// SetExternal drives a pin from the board model.
func (p *PinState) SetExternal(pin int, level bool) {
	if pin < 0 || pin > 47 {
		return
	}
	before := p.Level(pin)
	bit := uint64(1) << uint(pin)
	p.ExtValid |= bit
	if level {
		p.ExtLevel |= bit
	} else {
		p.ExtLevel &^= bit
	}
	p.notifyChange(pin, before)
}

// ReleaseExternal stops driving a pin from the board model.
func (p *PinState) ReleaseExternal(pin int) {
	if pin < 0 || pin > 47 {
		return
	}
	before := p.Level(pin)
	bit := uint64(1) << uint(pin)
	p.ExtValid &^= bit
	p.notifyChange(pin, before)
}

func (p *PinState) notifyChange(pin int, before bool) {
	if p.OnChange != nil {
		if after := p.Level(pin); after != before {
			p.OnChange(pin, after)
		}
	}
}

func (p *PinState) setOutput(pin int, level bool) {
	bit := uint64(1) << uint(pin)
	before := p.rawLevel(pin)
	if level {
		p.Out |= bit
	} else {
		p.Out &^= bit
	}
	if p.OnChange != nil && p.rawLevel(pin) != before {
		p.OnChange(pin, p.rawLevel(pin))
	}
}

func (p *PinState) setOE(pin int, oe bool) {
	bit := uint64(1) << uint(pin)
	before := p.rawLevel(pin)
	if oe {
		p.OE |= bit
	} else {
		p.OE &^= bit
	}
	if p.OnChange != nil && p.rawLevel(pin) != before {
		p.OnChange(pin, p.rawLevel(pin))
	}
}

// ioBank models IO_BANK0 and PADS_BANK0.
type ioBank struct {
	bank0 *regBlock
	pads  *regBlock
}

func (io *ioBank) init(c *Chip) {
	io.bank0 = newRegBlock(0x40028000, 0x1000)
	io.pads = newRegBlock(0x40038000, 0x1000)

	io.bank0.writeFn = func(c *Chip, off, v uint32) {
		if off >= 0x04 && off < 0x04+8*48 && (off-0x04)%8 == 0 {
			pin := int((off - 0x04) / 8)
			old := c.Pins.Func[pin]
			c.Pins.Func[pin] = uint8(v & 0x1F)
			bit := uint64(1) << uint(pin)
			if c.Pins.Func[pin] == pwmFuncSel {
				c.Pins.pwmPins |= bit
			} else {
				c.Pins.pwmPins &^= bit
			}
			if c.pwm != nil {
				if old == pwmFuncSel && c.Pins.Func[pin] != pwmFuncSel {
					c.Pins.setOutput(pin, false)
					c.Pins.setOE(pin, false)
				}
				if c.Pins.Func[pin] == pwmFuncSel {
					c.pwm.updatePinDrive(pinSlice(pin))
				}
			}
		}
	}
	io.pads.writeFn = func(c *Chip, off, v uint32) {
		if off >= 0x04 && off < 0x04+4*48 && (off-0x04)%4 == 0 {
			pin := int((off - 0x04) / 4)
			before := c.Pins.Level(pin)
			c.Pins.PullUp[pin] = v&(1<<3) != 0
			c.Pins.PullDown[pin] = v&(1<<2) != 0
			bit := uint64(1) << uint(pin)
			if c.Pins.PullUp[pin] {
				c.Pins.pullUpMask |= bit
			} else {
				c.Pins.pullUpMask &^= bit
			}
			c.Pins.notifyChange(pin, before)
		}
	}
	// IO_BANK0 CTRL resets to FUNCSEL=NULL (0x1F), matching PinState.Func.
	io.bank0.resetFn = func(c *Chip) {
		for pin := range 48 {
			io.bank0.regs[(0x04+pin*8)/4] = 0x1F
		}
	}
	io.pads.resetFn = func(c *Chip) {
		c.Pins.pullUpMask = 0
		for pin := range 48 {
			io.pads.regs[(0x04+pin*4)/4] = 0x116
			c.Pins.PullUp[pin] = false
			c.Pins.PullDown[pin] = true
		}
	}
}

func (io *ioBank) reset() {}

// SIO.

// SIO interrupt numbers on the RP2350.
const (
	sioIRQFIFO   = 25
	sioIRQBell   = 26
	sioFIFODepth = 4
)

type sioBlock struct {
	fifo [2][]uint32 // fifo[i] is core i's receive queue
	// fifoStWOF is set when a write finds the peer queue full; ROE on
	// overflow of the local queue. Both are write-1-to-clear.
	fifoWOF [2]bool
	fifoROE [2]bool

	// doorbellIn holds the doorbell bits set for each core. A nonzero value
	// raises SIO_IRQ_BELL on that core.
	doorbellIn [2]uint32

	spinlock uint32

	// Core 1 bootrom launch sequence state.
	launchSeq   []uint32
	core1Parked bool
	Pushes      uint64
	Launches    uint64
}

func (s *sioBlock) init(c *Chip) {
	s.core1Parked = true
	s.launchSeq = s.launchSeq[:0]
}

func (s *sioBlock) reset() {
	s.fifo[0] = s.fifo[0][:0]
	s.fifo[1] = s.fifo[1][:0]
	s.spinlock = 0
	s.launchSeq = s.launchSeq[:0]
	s.core1Parked = true
	s.fifoWOF = [2]bool{}
	s.fifoROE = [2]bool{}
	s.doorbellIn = [2]uint32{}
}

// peek returns SIO state without FIFO-pop or spinlock-acquire side effects.
// Narrow writes use it for their lane merge so a write cannot consume data.
func (s *sioBlock) peek(c *Chip, off uint32) uint32 {
	switch off {
	case 0x00: // CPUID
		return uint32(c.core)
	case 0x04: // GPIO_IN
		return uint32(c.Pins.inputWord(0))
	case 0x08: // GPIO_HI_IN: GPIO32-47 plus QSPI/USB status
		v := uint32(c.Pins.inputWord(32))
		// QSPI_CSN is pulled low by the BOOTSEL button; idle is high.
		if !c.bootselHeld {
			v |= 1 << 27
		}
		return v
	case 0x10:
		return uint32(c.Pins.Out)
	case 0x14:
		return uint32(c.Pins.Out >> 32)
	case 0x30:
		return uint32(c.Pins.OE)
	case 0x34:
		return uint32(c.Pins.OE >> 32)
	case 0x50: // FIFO_ST
		src := c.core
		v := uint32(0)
		if len(s.fifo[src]) > 0 {
			v |= 1 // VLD
		}
		if len(s.fifo[1-src]) < sioFIFODepth {
			v |= 1 << 1 // RDY
		}
		if s.fifoWOF[src] {
			v |= 1 << 2
		}
		if s.fifoROE[src] {
			v |= 1 << 3
		}
		return v
	case 0x54: // FIFO_WR
		return 0
	case 0x58: // FIFO_RD
		if len(s.fifo[c.core]) == 0 {
			return 0
		}
		return s.fifo[c.core][0]
	case 0x5C: // SPINLOCK_ST
		return s.spinlock
	case 0x180, 0x184: // DOORBELL_OUT_*: status on the opposite core
		return s.doorbellIn[1-c.core]
	case 0x188, 0x18C: // DOORBELL_IN_*: status on this core
		return s.doorbellIn[c.core]
	}
	if off >= 0x100 && off < 0x180 { // SPINLOCK0..31
		n := (off - 0x100) / 4
		bit := uint32(1) << n
		if s.spinlock&bit == 0 {
			return bit
		}
	}
	return 0
}

func (s *sioBlock) read(c *Chip, off uint32) uint32 {
	v := s.peek(c, off)
	if off == 0x58 { // FIFO_RD
		src := c.core
		if len(s.fifo[src]) == 0 {
			s.fifoROE[src] = true
			s.syncFIFOIRQ(c, src)
			return 0
		}
		v = s.fifo[src][0]
		s.fifo[src] = s.fifo[src][1:]
		s.syncFIFOIRQ(c, src)
		return v
	}
	if off >= 0x100 && off < 0x180 && v != 0 { // SPINLOCK0..31
		s.spinlock |= v
	}
	return v
}

func (s *sioBlock) write(c *Chip, off, v uint32) {
	// RP2350-E2: the SIO decoder also treats writes to 0x180..0x1fc as
	// writes to the corresponding spinlock 0x80 bytes below.
	if off >= 0x180 && off < 0x200 {
		n := (off - 0x180) / 4
		s.spinlock &^= uint32(1) << n
	}
	oldOut, oldOE := c.Pins.Out, c.Pins.OE
	switch off {
	case 0x10:
		c.Pins.Out = c.Pins.Out&^0xFFFFFFFF | uint64(v)
	case 0x14:
		c.Pins.Out = c.Pins.Out&0xFFFFFFFF | uint64(v)<<32
	case 0x18: // GPIO_OUT_SET
		c.Pins.Out |= uint64(v)
	case 0x1C:
		c.Pins.Out |= uint64(v) << 32
	case 0x20: // GPIO_OUT_CLR
		c.Pins.Out &^= uint64(v)
	case 0x24:
		c.Pins.Out &^= uint64(v) << 32
	case 0x28: // GPIO_OUT_XOR
		c.Pins.Out ^= uint64(v)
	case 0x2C:
		c.Pins.Out ^= uint64(v) << 32
	case 0x30:
		c.Pins.OE = c.Pins.OE&^0xFFFFFFFF | uint64(v)
	case 0x34:
		c.Pins.OE = c.Pins.OE&0xFFFFFFFF | uint64(v)<<32
	case 0x38:
		c.Pins.OE |= uint64(v)
	case 0x3C:
		c.Pins.OE |= uint64(v) << 32
	case 0x40:
		c.Pins.OE &^= uint64(v)
	case 0x44:
		c.Pins.OE &^= uint64(v) << 32
	case 0x48:
		c.Pins.OE ^= uint64(v)
	case 0x4C:
		c.Pins.OE ^= uint64(v) << 32
	case 0x50: // FIFO_ST: write-1-to-clear
		if v&(1<<2) != 0 {
			s.fifoWOF[c.core] = false
		}
		if v&(1<<3) != 0 {
			s.fifoROE[c.core] = false
		}
		s.syncFIFOIRQ(c, c.core)
	case 0x54: // FIFO_WR
		s.push(c, c.core, v)
	case 0x180: // DOORBELL_OUT_SET: set the peer's input bits
		if v &= 0xFF; v == 0 {
			break
		}
		peer := 1 - c.core
		s.doorbellIn[peer] |= v
		c.pendCoreIRQ(peer, sioIRQBell)
	case 0x184: // DOORBELL_OUT_CLR
		v &= 0xFF
		peer := 1 - c.core
		s.doorbellIn[peer] &^= v
		if s.doorbellIn[peer] == 0 {
			c.clearCorePendingIRQ(peer, sioIRQBell)
		}
	case 0x188: // DOORBELL_IN_SET: set this core's input bits
		if v &= 0xFF; v == 0 {
			break
		}
		s.doorbellIn[c.core] |= v
		c.pendCoreIRQ(c.core, sioIRQBell)
	case 0x18C: // DOORBELL_IN_CLR
		v &= 0xFF
		s.doorbellIn[c.core] &^= v
		if s.doorbellIn[c.core] == 0 {
			c.clearCorePendingIRQ(c.core, sioIRQBell)
		}
	}
	if off >= 0x100 && off < 0x180 {
		n := (off - 0x100) / 4
		s.spinlock &^= uint32(1) << n
	}
	if c.Pins.Out != oldOut || c.Pins.OE != oldOE {
		c.pinsChanged(oldOut, oldOE)
	}
}

func (s *sioBlock) push(c *Chip, from int, v uint32) {
	s.Pushes++
	if s.core1Parked && from == 0 {
		// Core 1 is parked in the bootrom FIFO wait loop: echo the word and
		// watch for the multicore launch sequence.
		if len(s.fifo[0]) < sioFIFODepth {
			s.fifo[0] = append(s.fifo[0], v)
		} else {
			s.fifoWOF[0] = true
		}
		s.syncFIFOIRQ(c, 0)
		s.launchSeq = append(s.launchSeq, v)
		if len(s.launchSeq) == 6 {
			vt := s.launchSeq[3]
			sp := s.launchSeq[4]
			entry := s.launchSeq[5]
			s.launchSeq = s.launchSeq[:0]
			s.core1Parked = false
			s.Launches++
			c.CPU1.Parked = false
			c.CPU1.VTOR = vt &^ 0x7F
			c.CPU1.MSP = sp
			c.CPU1.R[13] = sp
			c.CPU1.R[15] = entry &^ 1
			c.CPU1.R[14] = 0
		}
		return
	}
	dst := 1 - from
	if len(s.fifo[dst]) >= sioFIFODepth {
		s.fifoWOF[from] = true
		s.syncFIFOIRQ(c, from)
		return
	}
	s.fifo[dst] = append(s.fifo[dst], v)
	s.syncFIFOIRQ(c, dst)
}

func (s *sioBlock) syncFIFOIRQ(c *Chip, core int) {
	if len(s.fifo[core]) != 0 || s.fifoWOF[core] || s.fifoROE[core] {
		c.pendCoreIRQ(core, sioIRQFIFO)
		return
	}
	c.clearCorePendingIRQ(core, sioIRQFIFO)
}

// inputWord builds a 32-bit GPIO input value for the given base pin.
func (p *PinState) inputWord(base int) uint32 {
	var v uint32
	for i := range 32 {
		if p.Level(base + i) {
			v |= 1 << uint(i)
		}
	}
	return v
}

// levelWith resolves a pin level as if Out/OE had the given values.
func (p *PinState) levelWith(out, oe uint64, pin int) bool {
	bit := uint64(1) << uint(pin)
	if oe&bit != 0 {
		return out&bit != 0
	}
	if p.ExtValid&bit != 0 {
		return p.ExtLevel&bit != 0
	}
	return p.PullUp[pin]
}

// pinsChanged notifies the board model about levels that changed because the
// output latch or output enable changed.
func (c *Chip) pinsChanged(oldOut, oldOE uint64) {
	if c.Pins.OnChange == nil {
		return
	}
	for pin := range 48 {
		if c.Pins.LevelOverride != nil {
			// A peripheral (PWM) owns this pin's level; an SIO write does not
			// change what is on the wire.
			if _, ok := c.Pins.LevelOverride(pin); ok {
				continue
			}
		}
		old := c.Pins.levelWith(oldOut, oldOE, pin)
		now := c.Pins.Level(pin)
		if old != now {
			c.Pins.OnChange(pin, now)
		}
	}
}
