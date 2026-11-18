package rp2350

// PinState holds the externally visible state of the 48 GPIO pins.
type PinState struct {
	Out, OE  uint64 // output latch and output enable, per pin
	Func     [48]uint8
	PullUp   [48]bool
	PullDown [48]bool

	// ExtLevel is a level driven by an external device (or the UI); ExtValid
	// marks which pins are actually driven externally.
	ExtLevel uint64
	ExtValid uint64

	// OnChange is called when a pin's resolved level changes.
	OnChange func(pin int, level bool)
}

func NewPinState() *PinState { return &PinState{} }

func (p *PinState) reset() {
	p.Out = 0
	p.OE = 0
	p.ExtLevel = 0
	p.ExtValid = 0
	p.PullUp = [48]bool{}
	p.PullDown = [48]bool{}
	p.Func = [48]uint8{}
}

// Level returns the resolved level of a pin.
func (p *PinState) Level(pin int) bool {
	if pin < 0 || pin > 47 {
		return false
	}
	bit := uint64(1) << uint(pin)
	if p.OE&bit != 0 {
		return p.Out&bit != 0
	}
	if p.ExtValid&bit != 0 {
		return p.ExtLevel&bit != 0
	}
	return p.PullUp[pin]
}

// SetExternal drives a pin from the board model.
func (p *PinState) SetExternal(pin int, level bool) {
	if pin < 0 || pin > 47 {
		return
	}
	bit := uint64(1) << uint(pin)
	p.ExtValid |= bit
	if level {
		p.ExtLevel |= bit
	} else {
		p.ExtLevel &^= bit
	}
}

// ReleaseExternal stops driving a pin from the board model.
func (p *PinState) ReleaseExternal(pin int) {
	if pin < 0 || pin > 47 {
		return
	}
	bit := uint64(1) << uint(pin)
	p.ExtValid &^= bit
}

func (p *PinState) setOutput(pin int, level bool) {
	bit := uint64(1) << uint(pin)
	before := p.Level(pin)
	if level {
		p.Out |= bit
	} else {
		p.Out &^= bit
	}
	if p.OnChange != nil && p.Level(pin) != before {
		p.OnChange(pin, p.Level(pin))
	}
}

func (p *PinState) setOE(pin int, oe bool) {
	bit := uint64(1) << uint(pin)
	before := p.Level(pin)
	if oe {
		p.OE |= bit
	} else {
		p.OE &^= bit
	}
	if p.OnChange != nil && p.Level(pin) != before {
		p.OnChange(pin, p.Level(pin))
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
			c.Pins.Func[pin] = uint8(v & 0x1F)
		}
	}
	io.pads.writeFn = func(c *Chip, off, v uint32) {
		if off >= 0x04 && off < 0x04+4*48 && (off-0x04)%4 == 0 {
			pin := int((off - 0x04) / 4)
			c.Pins.PullUp[pin] = v&(1<<3) != 0
			c.Pins.PullDown[pin] = v&(1<<2) != 0
		}
	}
}

func (io *ioBank) reset() {}

// SIO.

type sioBlock struct {
	fifo [2][]uint32 // fifo[i] is core i's receive queue
	// fifoStWOF is set when a write finds the peer queue full; ROE on
	// overflow of the local queue. Both are write-1-to-clear.
	fifoWOF [2]bool
	fifoROE [2]bool

	spinlock uint32

	// Core 1 bootrom launch sequence state.
	launchSeq   []uint32
	core1Parked bool
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
}

func (s *sioBlock) read(c *Chip, off uint32) uint32 {
	switch off {
	case 0x00: // CPUID
		return uint32(c.core)
	case 0x04: // GPIO_IN
		return uint32(c.Pins.inputWord(0))
	case 0x08: // GPIO_HI_IN
		return uint32(c.Pins.inputWord(32))
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
		if len(s.fifo[1-src]) < 8 {
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
		src := c.core
		if len(s.fifo[src]) == 0 {
			return 0
		}
		v := s.fifo[src][0]
		s.fifo[src] = s.fifo[src][1:]
		return v
	case 0x5C: // SPINLOCK_ST
		return s.spinlock
	}
	if off >= 0x100 && off < 0x180 { // SPINLOCK0..31
		n := (off - 0x100) / 4
		bit := uint32(1) << n
		if s.spinlock&bit == 0 {
			s.spinlock |= bit
			return bit
		}
		return 0
	}
	return 0
}

func (s *sioBlock) write(c *Chip, off, v uint32) {
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
	case 0x54: // FIFO_WR
		s.push(c, c.core, v)
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
	if s.core1Parked && from == 0 {
		// Core 1 is parked in the bootrom FIFO wait loop: echo the word and
		// watch for the multicore launch sequence.
		if len(s.fifo[0]) < 8 {
			s.fifo[0] = append(s.fifo[0], v)
		} else {
			s.fifoWOF[0] = true
		}
		s.launchSeq = append(s.launchSeq, v)
		if len(s.launchSeq) == 6 {
			vt := s.launchSeq[3]
			sp := s.launchSeq[4]
			entry := s.launchSeq[5]
			s.launchSeq = s.launchSeq[:0]
			s.core1Parked = false
			c.CPU1.Parked = false
			c.CPU1.VTOR = vt &^ 0x7F
			c.CPU1.R[13] = sp
			c.CPU1.R[15] = entry &^ 1
			c.CPU1.R[14] = 0
		}
		return
	}
	dst := 1 - from
	if len(s.fifo[dst]) >= 8 {
		s.fifoWOF[from] = true
		return
	}
	s.fifo[dst] = append(s.fifo[dst], v)
}

// inputWord builds a 32-bit GPIO input value for the given base pin.
func (p *PinState) inputWord(base int) uint32 {
	var v uint32
	for i := 0; i < 32; i++ {
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
	for pin := 0; pin < 48; pin++ {
		old := c.Pins.levelWith(oldOut, oldOE, pin)
		now := c.Pins.Level(pin)
		if old != now {
			c.Pins.OnChange(pin, now)
		}
	}
}
