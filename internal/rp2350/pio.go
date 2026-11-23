package rp2350

import (
	"log/slog"
	"math/bits"
)

// pioBlock models one RP2350 PIO block: 32 shared instruction words, four state
// machines with their own config, scratch registers and FIFOs, and eight shared
// IRQ flags. Nothing runs as a goroutine; the chip's per-cycle tick advances
// each enabled state machine according to its clock divider.
type pioBlock struct {
	chip  *Chip
	block *regBlock
	id    int

	mem [32]uint16
	sm  [4]pioSM
	irq uint32

	// Pin latch and direction mask maintained by OUT/SET/SIDESET/MOV PINS.
	outVal uint64
	outOE  uint64

	fdebug uint32

	// active is a mask of enabled state machines, kept in step with CTRL.
	active uint8
}

type pioSM struct {
	p   *pioBlock
	idx int

	enabled  bool
	pc       uint8
	x, y     uint32
	isr      uint32
	osr      uint32
	inCount  int
	outCount int
	delay    int
	acc      uint32

	clkdiv    uint32
	execctrl  uint32
	shiftctrl uint32
	pinctrl   uint32

	tx  [8]uint32
	txN int
	rx  [8]uint32
	rxN int

	waitingPush bool // autopush pending (RX FIFO was full)
	pcSet       bool

	// irq wait: the instruction raises the target flag first, then stalls
	// until software (or another state machine) clears it.
	irqWaiting   bool
	irqWaitBlock *pioBlock
	irqWaitFlag  int
}

const (
	pioBase0 = 0x50200000
	pioBase1 = 0x50300000
	pioBase2 = 0x50400000

	pioSMBase   = 0xC8
	pioSMStride = 0x18

	// Block-level interrupt routing registers.
	pioIRQ0INTE = 0x170
	pioIRQ0INTF = 0x174
	pioIRQ0INTS = 0x178
	pioIRQ1INTE = 0x17C
	pioIRQ1INTF = 0x180
	pioIRQ1INTS = 0x184
)

// PIO reset values for the per-SM configuration registers.
const (
	pioClkdivReset    = 0x00010000
	pioExecctrlReset  = 0x0001F000
	pioShiftctrlReset = 0x000C0000
	pioPinctrlReset   = 0x14000000
)

func pioFuncSel(id int) uint8 { return uint8(6 + id) }

func newPIO(c *Chip, base uint32, id int) *pioBlock {
	p := &pioBlock{chip: c, block: newRegBlock(base, 0x1000), id: id}
	p.block.resetFn = func(c *Chip) { p.reset() }
	p.block.clearFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0x30: // IRQ is write-1-to-clear
			p.irq &^= v & 0xFF
			p.block.regs[0x30/4] = p.irq
			p.updateIRQ()
		case 0x08: // FDEBUG is write-1-to-clear
			p.fdebug &^= v
			p.block.regs[0x08/4] = p.fdebug
		default:
			p.block.write(c, off, p.block.read(c, off)&^v)
		}
	}
	for i := range p.sm {
		s := &p.sm[i]
		s.p = p
		s.idx = i
		s.clkdiv = pioClkdivReset
		s.execctrl = pioExecctrlReset
		s.shiftctrl = pioShiftctrlReset
		s.pinctrl = pioPinctrlReset
		s.outCount = 32 // OSR starts empty; PULL ifempty then loads it
		base := uint32(pioSMBase + i*pioSMStride)
		p.block.regs[base/4] = s.clkdiv
		p.block.regs[(base+4)/4] = s.execctrl
		p.block.regs[(base+8)/4] = s.shiftctrl
		p.block.regs[(base+0x14)/4] = s.pinctrl
	}
	p.block.readFn = p.read
	p.block.writeFn = p.write
	return p
}

func (p *pioBlock) reset() {
	p.mem = [32]uint16{}
	p.irq = 0
	p.outVal = 0
	p.outOE = 0
	p.fdebug = 0
	p.active = 0
	p.chip.pioActive &^= 1 << uint(p.id)
	for i := range p.sm {
		s := &p.sm[i]
		s.enabled = false
		s.pc = 0
		s.x, s.y = 0, 0
		s.isr, s.osr = 0, 0
		s.inCount, s.outCount = 0, 32
		s.delay = 0
		s.acc = 0
		s.tx = [8]uint32{}
		s.txN = 0
		s.rx = [8]uint32{}
		s.rxN = 0
		s.waitingPush = false
		s.pcSet = false
		s.irqWaiting = false
		s.irqWaitBlock = nil
		s.irqWaitFlag = 0
		s.clkdiv = pioClkdivReset
		s.execctrl = pioExecctrlReset
		s.shiftctrl = pioShiftctrlReset
		s.pinctrl = pioPinctrlReset
		base := uint32(pioSMBase + i*pioSMStride)
		p.block.regs[base/4] = s.clkdiv
		p.block.regs[(base+4)/4] = s.execctrl
		p.block.regs[(base+8)/4] = s.shiftctrl
		p.block.regs[(base+0x14)/4] = s.pinctrl
	}
	p.updateIRQ()
}

// ---- block registers ----

func (p *pioBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case 0x004: // FSTAT
		return p.fstat(), true
	case 0x008: // FDEBUG
		return p.fdebug, true
	case 0x00C: // FLEVEL
		return p.flevel(), true
	case 0x030: // IRQ
		return p.irq, true
	case pioIRQ0INTE, pioIRQ0INTF, pioIRQ1INTE, pioIRQ1INTF:
		return p.block.regs[off/4], true
	case pioIRQ0INTS:
		return (p.irq | p.block.regs[pioIRQ0INTF/4]) & p.block.regs[pioIRQ0INTE/4], true
	case pioIRQ1INTS:
		return (p.irq | p.block.regs[pioIRQ1INTF/4]) & p.block.regs[pioIRQ1INTE/4], true
	case 0x044: // DBG_CFGINFO: 4-deep FIFOs, 32 instructions, PIO v1
		return 4 | 32<<8 | 1<<16, true
	}
	if off >= 0x010 && off < 0x020 { // TXF0..3 (write-only)
		return 0, true
	}
	if off >= 0x020 && off < 0x030 { // RXF0..3
		return p.sm[(off-0x020)/4].popRx(), true
	}
	if off >= 0x048 && off < 0x048+32*4 {
		return uint32(p.mem[(off-0x048)/4]), true
	}
	if off >= pioSMBase && off < pioSMBase+4*pioSMStride {
		i := (off - pioSMBase) / pioSMStride
		switch (off - pioSMBase) % pioSMStride {
		case 0x0C: // ADDR
			return uint32(p.sm[i].pc), true
		case 0x10: // INSTR
			return 0, true
		}
	}
	return 0, false
}

func (p *pioBlock) write(c *Chip, off, v uint32) {
	switch {
	case off == 0x000: // CTRL
		p.writeCtrl(v)
		return
	case off == 0x008: // FDEBUG, write 1 to clear
		p.fdebug &^= v
		p.block.regs[off/4] = p.fdebug
		return
	case off >= 0x010 && off < 0x020: // TXF0..3
		p.sm[(off-0x010)/4].pushTx(v)
		return
	case off >= 0x020 && off < 0x030: // RXF writes are ignored
		return
	case off == 0x030: // IRQ, write 1 to clear
		p.irq &^= v & 0xFF
		p.block.regs[off/4] = p.irq
		p.updateIRQ()
		return
	case off == 0x034: // IRQ_FORCE
		p.irq |= v & 0xFF
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
		return
	case off == pioIRQ0INTE, off == pioIRQ0INTF, off == pioIRQ1INTE, off == pioIRQ1INTF:
		p.updateIRQ()
		return
	case off == pioIRQ0INTS, off == pioIRQ1INTS: // read-only status
		return
	case off >= 0x048 && off < 0x048+32*4:
		p.mem[(off-0x048)/4] = uint16(v)
		return
	}
	if off >= pioSMBase && off < pioSMBase+4*pioSMStride {
		i := (off - pioSMBase) / pioSMStride
		s := &p.sm[i]
		switch (off - pioSMBase) % pioSMStride {
		case 0x00: // CLKDIV
			s.clkdiv = v
			s.acc = 0
		case 0x04: // EXECCTRL
			s.execctrl = v
		case 0x08: // SHIFTCTRL; changing a FIFO join flushes both FIFOs
			old := s.shiftctrl
			s.shiftctrl = v
			p.chip.Logger.Debug("pio shiftctrl",
				slog.Uint64("cycle", p.chip.Cycles),
				slog.Int("block", p.id), slog.Int("sm", int(i)),
				slog.String("from", fmtHex(old)), slog.String("to", fmtHex(v)))
			if (old^v)&(1<<30|1<<31) != 0 {
				s.txN, s.rxN = 0, 0
				p.chip.Logger.Debug("pio fifo flush",
					slog.Uint64("cycle", p.chip.Cycles),
					slog.Int("block", p.id), slog.Int("sm", int(i)))
			}
		case 0x0C: // ADDR
			s.pc = uint8(v & 0x1F)
		case 0x10: // INSTR: execute immediately
			s.execNow(uint16(v))
		case 0x14: // PINCTRL
			s.pinctrl = v
		}
	}
}

// updateIRQ drives the two PIO interrupt lines from the shared flags,
// force registers and enables.
func (p *pioBlock) updateIRQ() {
	for i := range 2 {
		inte := p.block.regs[(pioIRQ0INTE+uint32(i)*0xC)/4]
		intf := p.block.regs[(pioIRQ0INTF+uint32(i)*0xC)/4]
		active := (p.irq | intf) & inte
		irq := 15 + 2*p.id + i
		if active != 0 {
			p.chip.NVIC.Pend(irq)
		} else {
			p.chip.NVIC.ClearPending(irq)
		}
	}
}

func (p *pioBlock) writeCtrl(v uint32) {
	var active uint8
	for i := range p.sm {
		s := &p.sm[i]
		s.enabled = v&(1<<uint(i)) != 0
		if s.enabled {
			active |= 1 << uint(i)
		}
		if v&(1<<uint(4+i)) != 0 {
			s.restart()
		}
		if v&(1<<uint(8+i)) != 0 {
			s.acc = 0
		}
	}
	// SM_RESTART and CLKDIV_RESTART are self-clearing strobes. Leaving them
	// set makes a later read-modify-write (SET alias) re-restart the machine
	// and jump back to the wrap target, losing the program's entry point.
	v &^= 0xFF0 // bits 4..11
	p.block.regs[0] = v
	p.active = active
	bit := uint8(1) << uint(p.id)
	if active != 0 {
		p.chip.pioActive |= bit
	} else {
		p.chip.pioActive &^= bit
	}
}

func (p *pioBlock) fstat() uint32 {
	var v uint32
	for i := range p.sm {
		s := &p.sm[i]
		if s.rxFull() {
			v |= 1 << uint(i)
		}
		if s.rxN == 0 || s.rxCap() == 0 {
			v |= 1 << uint(8+i)
		}
		if s.txCap() > 0 && s.txN >= s.txCap() {
			v |= 1 << uint(16+i)
		}
		if s.txN == 0 || s.txCap() == 0 {
			v |= 1 << uint(24+i)
		}
	}
	return v
}

func (p *pioBlock) flevel() uint32 {
	var v uint32
	for i := range p.sm {
		s := &p.sm[i]
		v |= uint32(s.txN&0xF) << uint(i*8)
		v |= uint32(s.rxN&0xF) << uint(i*8+4)
	}
	return v
}

// ---- state machine ----

func (s *pioSM) restart() {
	s.pc = uint8((s.execctrl >> 7) & 0x1F) // wrap bottom
	s.inCount, s.outCount = 0, 32
	s.delay = 0
	s.acc = 0
	s.waitingPush = false
	s.irqWaiting = false
}

func (s *pioSM) txCap() int {
	if s.shiftctrl&(1<<30) != 0 {
		return 8
	}
	if s.shiftctrl&(1<<31) != 0 {
		return 0
	}
	return 4
}

func (s *pioSM) rxCap() int {
	if s.shiftctrl&(1<<31) != 0 {
		return 8
	}
	if s.shiftctrl&(1<<30) != 0 {
		return 0
	}
	return 4
}

func (s *pioSM) txFull() bool { return s.txN >= s.txCap() }
func (s *pioSM) rxFull() bool { return s.rxN >= s.rxCap() }

func (s *pioSM) pushTx(v uint32) {
	s.p.chip.Logger.Debug("pio tx push",
		slog.Uint64("cycle", s.p.chip.Cycles),
		slog.Int("block", s.p.id), slog.Int("sm", s.idx),
		slog.String("value", fmtHex(v)))
	if s.txFull() {
		s.p.fdebug |= 1 << uint(16+s.idx) // TXOVER
		return
	}
	s.tx[s.txN] = v
	s.txN++
}

func (s *pioSM) popTx() (uint32, bool) {
	s.p.chip.Logger.Debug("pio tx pop",
		slog.Uint64("cycle", s.p.chip.Cycles),
		slog.Int("block", s.p.id), slog.Int("sm", s.idx),
		slog.Int("depth", s.txN))
	if s.txN == 0 {
		return 0, false
	}
	v := s.tx[0]
	copy(s.tx[:], s.tx[1:s.txN])
	s.txN--
	return v, true
}

func (s *pioSM) pushRx(v uint32) {
	if s.rxFull() {
		s.p.fdebug |= 1 << uint(s.idx) // RXSTALL
		return
	}
	s.rx[s.rxN] = v
	s.rxN++
}

func (s *pioSM) popRx() uint32 {
	if s.rxN == 0 {
		s.p.fdebug |= 1 << uint(8+s.idx) // RXUNDER
		return 0
	}
	v := s.rx[0]
	copy(s.rx[:], s.rx[1:s.rxN])
	s.rxN--
	return v
}

func (s *pioSM) pushThresh() int {
	if t := int(s.shiftctrl>>20) & 0x1F; t != 0 {
		return t
	}
	return 32
}

func (s *pioSM) pullThresh() int {
	if t := int(s.shiftctrl>>25) & 0x1F; t != 0 {
		return t
	}
	return 32
}

func (s *pioSM) autopush() bool      { return s.shiftctrl&(1<<16) != 0 }
func (s *pioSM) autopull() bool      { return s.shiftctrl&(1<<17) != 0 }
func (s *pioSM) inShiftRight() bool  { return s.shiftctrl&(1<<18) != 0 }
func (s *pioSM) outShiftRight() bool { return s.shiftctrl&(1<<19) != 0 }

func (s *pioSM) tick() {
	if !s.enabled {
		return
	}
	// The SM clock is sysclk / (INT + FRAC/256); never faster than sysclk.
	thr := max((s.clkdiv>>16)<<8|(s.clkdiv>>8)&0xFF, 256)
	s.acc += 256
	if s.acc < thr {
		return
	}
	s.acc -= thr

	if s.waitingPush {
		if !s.tryPush() {
			return
		}
		s.waitingPush = false
	}
	if s.delay > 0 {
		s.delay--
		return
	}
	s.step()
}

func (s *pioSM) tryPush() bool {
	if s.rxFull() {
		s.p.fdebug |= 1 << uint(s.idx) // RXSTALL
		return false
	}
	s.pushRx(s.isr)
	s.isr = 0
	s.inCount = 0
	return true
}

func (s *pioSM) step() {
	instr := s.p.mem[s.pc]
	if s.p.chip.pioLogN < 120 {
		s.p.chip.pioLogN++
		s.p.chip.Logger.Debug("pio step",
			slog.Uint64("cycle", s.p.chip.Cycles),
			slog.Int("block", s.p.id), slog.Int("sm", s.idx),
			slog.Int("pc", int(s.pc)),
			slog.String("instr", fmtHex(uint32(instr))),
			slog.Int("out", s.outCount),
			slog.String("y", fmtHex(s.y)),
			slog.String("osr", fmtHex(s.osr)),
			slog.Int("tx", s.txN),
			slog.Bool("pin0", s.p.chip.Pins.Level(0)))
	}
	delay := s.applySide(instr)
	s.pcSet = false
	if !s.execute(instr) {
		return
	}
	if !s.pcSet {
		s.advance()
	}
	s.delay = delay
}

// execNow runs an instruction written to SMx_INSTR: it takes effect
// immediately and does not normally advance the program counter.
func (s *pioSM) execNow(instr uint16) {
	s.p.chip.Logger.Debug("pio exec",
		slog.Uint64("cycle", s.p.chip.Cycles),
		slog.Int("block", s.p.id), slog.Int("sm", s.idx),
		slog.String("instr", fmtHex(uint32(instr))),
		slog.String("x", fmtHex(s.x)),
		slog.String("osr", fmtHex(s.osr)),
		slog.Int("out", s.outCount), slog.Int("tx", s.txN), slog.Int("rx", s.rxN))
	s.pcSet = false
	s.execute(instr)
	s.p.chip.Logger.Debug("pio exec result",
		slog.Uint64("cycle", s.p.chip.Cycles),
		slog.Int("block", s.p.id), slog.Int("sm", s.idx),
		slog.String("x", fmtHex(s.x)),
		slog.String("osr", fmtHex(s.osr)),
		slog.Int("out", s.outCount), slog.Int("tx", s.txN), slog.Int("rx", s.rxN))
}

func (s *pioSM) advance() {
	if s.pc == uint8((s.execctrl>>12)&0x1F) {
		s.pc = uint8((s.execctrl >> 7) & 0x1F)
	} else {
		s.pc = (s.pc + 1) & 0x1F
	}
}

// applySide decodes and applies side-set, returning the instruction's delay.
func (s *pioSM) applySide(instr uint16) int {
	sc := int((s.pinctrl >> 29) & 7)
	opt := s.execctrl&(1<<30) != 0
	used := sc
	if opt {
		used++
	}
	if used == 0 {
		return int(instr>>8) & 0x1F
	}
	base := int((s.pinctrl >> 10) & 0x1F)
	pindir := s.execctrl&(1<<29) != 0
	if opt {
		if (instr>>12)&1 != 0 && sc > 0 {
			v := uint32(instr>>uint(12-sc)) & (1<<uint(sc) - 1)
			s.writePinRange(base, sc, v, pindir)
		}
	} else {
		v := uint32(instr>>uint(13-sc)) & (1<<uint(sc) - 1)
		s.writePinRange(base, sc, v, pindir)
	}
	delayBits := 5 - used
	if delayBits == 0 {
		return 0
	}
	return int(instr>>8) & (1<<uint(delayBits) - 1)
}

// ---- pin access ----

// gpioBase is the block-wide PIO GPIO base (PIO_GPIOBASE, values 0 or 16).
// On RP2350B it shifts PIO's 32-pin window to GPIO 16..47.
func (p *pioBlock) gpioBase() int { return int(p.block.regs[0x168/4] & 0x10) }

// pinAt maps a PIO pin offset to a system GPIO using GPIOBASE.
func (p *pioBlock) pinAt(offset int) int { return p.gpioBase() + (offset & 31) }

func (s *pioSM) writePinRange(base, count int, v uint32, dirs bool) {
	if count <= 0 {
		return
	}
	for i := range count {
		pin := s.p.pinAt(base + i)
		bit := uint64(1) << uint(pin)
		val := v&(1<<uint(i)) != 0
		if dirs {
			if val {
				s.p.outOE |= bit
			} else {
				s.p.outOE &^= bit
			}
		} else {
			if val {
				s.p.outVal |= bit
			} else {
				s.p.outVal &^= bit
			}
		}
		if int(s.p.chip.Pins.Func[pin]) != int(pioFuncSel(s.p.id)) {
			continue
		}
		if dirs {
			s.p.chip.Pins.setOE(pin, val)
		} else {
			s.p.chip.Pins.setOutput(pin, val)
		}
	}
}

func (s *pioSM) readPins(base int) uint32 {
	var v uint32
	for i := range 32 {
		if s.p.chip.Pins.Level(s.p.pinAt(base + i)) {
			v |= 1 << uint(i)
		}
	}
	return v
}

func (s *pioSM) outCountPins() int {
	// PINCTRL.OUT_COUNT is 0..32 inclusive; 0 means no pins.
	return int((s.pinctrl >> 20) & 0x3F)
}

// ---- execution ----

func (s *pioSM) execute(instr uint16) bool {
	switch instr >> 13 {
	case 0: // JMP
		s.execJmp(instr)
	case 1: // WAIT
		return s.execWait(instr)
	case 2: // IN
		s.execIn(instr)
	case 3: // OUT
		return s.execOut(instr)
	case 4: // PUSH / PULL
		return s.execPushPull(instr)
	case 5: // MOV
		s.execMov(instr)
	case 6: // IRQ
		return s.execIrq(instr)
	case 7: // SET
		s.execSet(instr)
	}
	return true
}

func (s *pioSM) execJmp(instr uint16) {
	cond := int(instr>>5) & 7
	addr := uint8(instr & 0x1F)
	jump := false
	switch cond {
	case 0:
		jump = true
	case 1:
		jump = s.x == 0
	case 2:
		if s.x != 0 {
			s.x--
			jump = true
		}
	case 3:
		jump = s.y == 0
	case 4:
		if s.y != 0 {
			s.y--
			jump = true
		}
	case 5:
		jump = s.x != s.y
	case 6:
		jump = s.p.chip.Pins.Level(s.p.pinAt(int((s.execctrl >> 24) & 0x1F)))
	case 7:
		jump = s.outCount < s.pullThresh()
	}
	if jump {
		s.pc = addr
		s.pcSet = true
	}
}

func (s *pioSM) execWait(instr uint16) bool {
	pol := instr&(1<<7) != 0
	src := int(instr>>5) & 3
	index := int(instr & 0x1F)
	switch src {
	case 0: // GPIO, relative to GPIOBASE
		return s.p.chip.Pins.Level(s.p.pinAt(index)) == pol
	case 1: // PIN, relative to IN_BASE
		pin := s.p.pinAt(int((s.pinctrl>>15)&0x1F) + index)
		return s.p.chip.Pins.Level(pin) == pol
	case 2: // IRQ
		mode := (index >> 3) & 3
		flag := index & 7
		switch mode {
		case 2: // relative to this state machine
			flag = (flag &^ 3) | ((flag + s.idx) & 3)
		case 1: // previous PIO block
			p := s.p.chip.pio[(s.p.id+2)%3]
			if p == nil {
				return false
			}
			return p.waitIrq(flag, pol)
		case 3: // next PIO block
			p := s.p.chip.pio[(s.p.id+1)%3]
			if p == nil {
				return false
			}
			return p.waitIrq(flag, pol)
		}
		if (s.p.irq&(1<<uint(flag)) != 0) != pol {
			return false
		}
		if pol {
			s.p.irq &^= 1 << uint(flag)
			s.p.block.regs[0x30/4] = s.p.irq
		}
		return true
	}
	return true
}

func (p *pioBlock) waitIrq(flag int, pol bool) bool {
	if (p.irq&(1<<uint(flag)) != 0) != pol {
		return false
	}
	if pol {
		p.irq &^= 1 << uint(flag)
		p.block.regs[0x30/4] = p.irq
	}
	return true
}

func (s *pioSM) inSource(src int) uint32 {
	switch src {
	case 0:
		return s.readPins(int((s.pinctrl >> 15) & 0x1F))
	case 1:
		return s.x
	case 2:
		return s.y
	case 3:
		return 0
	case 6:
		return s.isr
	case 7:
		return s.osr
	}
	return 0
}

func (s *pioSM) execIn(instr uint16) {
	count := int(instr & 0x1F)
	if count == 0 {
		count = 32
	}
	v := s.inSource(int(instr>>5) & 7)
	s.isr = shiftIn(s.isr, v, count, s.inShiftRight())
	s.inCount += count
	if s.inCount > 32 {
		s.inCount = 32
	}
	if s.autopush() && s.inCount >= s.pushThresh() {
		if s.rxFull() {
			s.waitingPush = true
		} else {
			s.pushRx(s.isr)
			s.isr = 0
			s.inCount = 0
		}
	}
}

func shiftIn(cur, data uint32, n int, right bool) uint32 {
	if right {
		if n >= 32 {
			return data
		}
		return cur>>uint(n) | data<<uint(32-n)
	}
	if n >= 32 {
		return data
	}
	return cur<<uint(n) | data>>uint(32-n)
}

func (s *pioSM) execOut(instr uint16) bool {
	count := int(instr & 0x1F)
	if count == 0 {
		count = 32
	}
	dest := int(instr>>5) & 7
	right := s.outShiftRight()
	sticky := s.execctrl&(1<<17) != 0
	if s.outCount >= s.pullThresh() && s.autopull() {
		if v, ok := s.popTx(); ok {
			s.osr = v
			s.outCount = 0
		} else {
			// Refill happens at the point the OSR is needed, so the
			// instruction before this one still completes (the WS2812
			// program's falling edge must not be skipped).
			s.p.fdebug |= 1 << uint(24+s.idx) // TXSTALL
			return false
		}
	} else if s.outCount >= 32 {
		// An empty OSR is refilled from the TX FIFO on OUT even when
		// autopull is disabled; this is how the HAL's SetX/SetY work.
		if v, ok := s.popTx(); ok {
			s.osr = v
			s.outCount = 0
		}
	}
	var v uint32
	if sticky && count < 32 {
		if right {
			v = s.osr & (1<<uint(count) - 1)
		} else {
			v = s.osr >> uint(32-count)
		}
	} else if right {
		if count >= 32 {
			v, s.osr = s.osr, 0
		} else {
			v = s.osr & (1<<uint(count) - 1)
			s.osr >>= uint(count)
		}
	} else {
		if count >= 32 {
			v, s.osr = s.osr, 0
		} else {
			v = s.osr >> uint(32-count)
			s.osr <<= uint(count)
		}
	}
	s.outCount += count
	if s.outCount > 32 {
		s.outCount = 32
	}

	switch dest {
	case 0: // PINS
		s.writePinRange(int(s.pinctrl&0x1F), s.outCountPins(), v, false)
	case 1:
		s.x = v
	case 2:
		s.y = v
	case 3:
	case 4: // PINDIRS
		s.writePinRange(int(s.pinctrl&0x1F), s.outCountPins(), v, true)
	case 5: // PC
		s.pc = uint8(v & 0x1F)
		s.pcSet = true
	case 6: // ISR
		s.isr = v
	case 7: // EXEC
		s.execNow(uint16(v))
	}

	return true
}

func (s *pioSM) execPushPull(instr uint16) bool {
	if instr&0x80 == 0 { // PUSH
		ifFull := instr&0x40 != 0
		block := instr&0x20 != 0
		if ifFull && s.inCount < s.pushThresh() {
			return true
		}
		if s.rxFull() {
			s.p.fdebug |= 1 << uint(s.idx) // RXSTALL
			return !block
		}
		s.pushRx(s.isr)
		s.isr = 0
		s.inCount = 0
		return true
	}
	// PULL
	ifEmpty := instr&0x40 != 0
	block := instr&0x20 != 0
	if ifEmpty && s.outCount < s.pullThresh() {
		return true
	}
	if s.txN == 0 {
		s.p.fdebug |= 1 << uint(24+s.idx) // TXSTALL
		if block {
			return false
		}
		s.osr = s.x
		s.outCount = 0
		return true
	}
	s.osr, _ = s.popTx()
	s.outCount = 0
	return true
}

func (s *pioSM) execMov(instr uint16) {
	dst := int(instr>>5) & 7
	op := int(instr>>3) & 3
	src := int(instr) & 7
	var v uint32
	switch src {
	case 0:
		v = s.readPins(int((s.pinctrl >> 15) & 0x1F))
	case 1:
		v = s.x
	case 2:
		v = s.y
	case 3:
		v = 0
	case 5: // STATUS
		v = s.movStatus()
	case 6:
		v = s.isr
	case 7:
		v = s.osr
	}
	switch op {
	case 1:
		v = ^v
	case 2:
		v = bits.Reverse32(v)
	case 3:
		s.p.chip.CPU0.Halt("unsupported PIO MOV operation")
		return
	}
	switch dst {
	case 0: // PINS
		s.writePinRange(int(s.pinctrl&0x1F), s.outCountPins(), v, false)
	case 1:
		s.x = v
	case 2:
		s.y = v
	case 3: // PINDIRS (RP2350)
		s.writePinRange(int(s.pinctrl&0x1F), s.outCountPins(), v, true)
	case 4: // EXEC
		s.execNow(uint16(v))
	case 5: // PC
		s.pc = uint8(v & 0x1F)
		s.pcSet = true
	case 6:
		s.isr = v
	case 7:
		s.osr = v
		s.outCount = 0
	}
}

func (s *pioSM) movStatus() uint32 {
	sel := (s.execctrl >> 5) & 1
	n := (s.execctrl >> 0) & 0x1F
	var level int
	if sel == 0 { // TX FIFO less than N
		level = s.txN
	} else { // RX FIFO less than N
		level = s.rxN
	}
	if uint32(level) < n {
		return ^uint32(0)
	}
	return 0
}

func (s *pioSM) execIrq(instr uint16) bool {
	action := int(instr>>5) & 7
	index := int(instr & 0x1F)
	mode := (index >> 3) & 3
	flag := index & 7
	p := s.p
	switch mode {
	case 1:
		p = s.p.chip.pio[(s.p.id+2)%3]
	case 2:
		flag = (flag &^ 3) | ((flag + s.idx) & 3)
	case 3:
		p = s.p.chip.pio[(s.p.id+1)%3]
	}
	if p == nil {
		return false
	}
	switch action {
	case 0: // set
		p.irq |= 1 << uint(flag)
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
	case 1: // wait: raise the flag, then stall until it is cleared
		if s.irqWaiting {
			if s.irqWaitBlock.irq&(1<<uint(s.irqWaitFlag)) != 0 {
				return false
			}
			s.irqWaiting = false
			return true
		}
		p.irq |= 1 << uint(flag)
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
		s.irqWaiting = true
		s.irqWaitBlock = p
		s.irqWaitFlag = flag
		return false
	case 2, 3: // clear (Clear overrides Wait)
		p.irq &^= 1 << uint(flag)
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
	}
	return true
}

func (s *pioSM) execSet(instr uint16) {
	dest := int(instr>>5) & 7
	v := uint32(instr & 0x1F)
	base := int((s.pinctrl >> 5) & 0x1F) // SET_BASE
	count := int((s.pinctrl >> 26) & 7)
	switch dest {
	case 0: // PINS
		s.writePinRange(base, count, v, false)
	case 1:
		s.x = v
	case 2:
		s.y = v
	case 4: // PINDIRS
		s.writePinRange(base, count, v, true)
	}
}

// tick advances one PIO block by one CPU cycle.
func (p *pioBlock) tick() {
	mask := p.active
	for mask != 0 {
		i := bits.TrailingZeros8(mask)
		mask &^= 1 << uint(i)
		p.sm[i].tick()
	}
}
