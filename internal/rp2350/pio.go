package rp2350

import (
	"log/slog"
	"math/bits"
)

// pioBlock models one RP2350 PIO block: 32 shared instruction words, four state
// machines with their own config, scratch registers and FIFOs, and eight shared
// IRQ flags. Nothing runs as a goroutine; the chip's per-cycle tick advances
// each enabled state machine according to its free-running clock divider.
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
	active   uint8
	clocking uint8 // SMs whose divider is not 1 must tick while disabled

	// Input synchronizers are relative to the block's 32-pin GPIO window.
	// INPUT_SYNC_BYPASS selects the raw input for individual pins.
	inputBypass  uint32
	inputSync1   uint64
	inputSync2   uint64
	inputLast    uint64
	inputPending uint8
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

	pcSet bool

	// OUT/MOV EXEC queues an instruction for the next enabled SM cycle.
	execPending bool
	execInstr   uint16

	// A stalled instruction remains at the current PC. Side-set and the main
	// instruction's first-cycle effects are not repeated while it waits.
	stalled     bool
	stallKind   uint8
	stallInstr  uint16
	stallDelay  int
	stallSource uint8
	execStalled bool

	// The source of the instruction currently being attempted.
	instructionSource uint8

	// OUT_STICKY latches the most recent main pin writes per SM. Side-set is
	// deliberately not sticky and is applied after these and the main writes.
	stickyLevelMask uint32
	stickyLevelVal  uint32
	stickyDirMask   uint32
	stickyDirVal    uint32
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

const (
	pioSourceMemory uint8 = iota
	pioSourceExec
	pioSourceForced
)

const (
	pioStallNone uint8 = iota
	pioStallWait
	pioStallPush
	pioStallPull
	pioStallOutRefill
	pioStallInPush
	pioStallIRQ
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
	p.clocking = 0
	p.inputBypass = 0
	p.inputSync1 = 0
	p.inputSync2 = 0
	p.inputLast = 0
	p.inputPending = 0
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
		s.pcSet = false
		s.execPending = false
		s.execInstr = 0
		s.stalled = false
		s.stallKind = pioStallNone
		s.stallInstr = 0
		s.stallDelay = 0
		s.stallSource = pioSourceMemory
		s.execStalled = false
		s.instructionSource = pioSourceMemory
		s.stickyLevelMask = 0
		s.stickyLevelVal = 0
		s.stickyDirMask = 0
		s.stickyDirVal = 0
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
	case 0x038: // INPUT_SYNC_BYPASS
		return p.inputBypass, true
	case 0x03C: // DBG_PADOUT
		return uint32(p.outVal), true
	case 0x040: // DBG_PADOE
		return uint32(p.outOE), true
	case 0x044: // DBG_CFGINFO
		return 1<<28 | 32<<16 | 4<<8 | 4, true
	case 0x16C: // INTR
		return p.intr(), true
	case pioIRQ0INTE, pioIRQ0INTF, pioIRQ1INTE, pioIRQ1INTF:
		return p.block.regs[off/4], true
	case pioIRQ0INTS:
		return (p.intr() | p.block.regs[pioIRQ0INTF/4]) & p.block.regs[pioIRQ0INTE/4], true
	case pioIRQ1INTS:
		return (p.intr() | p.block.regs[pioIRQ1INTF/4]) & p.block.regs[pioIRQ1INTE/4], true
	}
	if off >= 0x010 && off < 0x020 { // TXF0..3 (write-only)
		return 0, true
	}
	if off >= 0x020 && off < 0x030 { // RXF0..3
		return p.sm[(off-0x020)/4].popRx(), true
	}
	if off >= 0x048 && off < 0x048+32*4 { // INSTR_MEM is write-only
		return 0, true
	}
	if off >= 0x128 && off < 0x168 { // RXF0..3_PUTGET0..3
		i := (off - 0x128) / 0x10
		entry := (off - 0x128) % 0x10 / 4
		s := &p.sm[i]
		if s.rxPut() && !s.rxGet() {
			return s.rx[entry], true
		}
		return 0, true
	}
	if off >= pioSMBase && off < pioSMBase+4*pioSMStride {
		i := (off - pioSMBase) / pioSMStride
		s := &p.sm[i]
		switch (off - pioSMBase) % pioSMStride {
		case 0x00: // CLKDIV
			return s.clkdiv, true
		case 0x04: // EXECCTRL
			v := s.execctrl &^ (uint32(1) << 31)
			if s.execStalled {
				v |= 1 << 31
			}
			return v, true
		case 0x08: // SHIFTCTRL
			return s.shiftctrl, true
		case 0x0C: // ADDR
			return uint32(s.pc), true
		case 0x10: // INSTR reflects the current program-memory address
			return uint32(p.mem[s.pc]), true
		case 0x14: // PINCTRL
			return s.pinctrl, true
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
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
		return
	case off == 0x034: // IRQ_FORCE
		p.irq |= v & 0xFF
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
		return
	case off == 0x038: // INPUT_SYNC_BYPASS
		p.inputBypass = v
		return
	case off == 0x03C || off == 0x040: // debug pad registers are read-only
		return
	case off >= 0x048 && off < 0x048+32*4:
		p.mem[(off-0x048)/4] = uint16(v)
		return
	case off >= 0x128 && off < 0x168:
		i := (off - 0x128) / 0x10
		entry := (off - 0x128) % 0x10 / 4
		s := &p.sm[i]
		if s.rxGet() && !s.rxPut() {
			s.rx[entry] = v
		}
		return
	case off == 0x168: // GPIOBASE, only bit 4 is writable
		p.block.regs[off/4] = v & 0x10
		return
	case off == pioIRQ0INTE, off == pioIRQ0INTF, off == pioIRQ1INTE, off == pioIRQ1INTF:
		p.updateIRQ()
		return
	case off == pioIRQ0INTS, off == pioIRQ1INTS: // read-only status
		return
	}
	if off >= pioSMBase && off < pioSMBase+4*pioSMStride {
		i := (off - pioSMBase) / pioSMStride
		s := &p.sm[i]
		switch (off - pioSMBase) % pioSMStride {
		case 0x00: // CLKDIV does not restart the free-running divider
			s.clkdiv = v & 0xFFFFFF00
			if s.clkdiv == pioClkdivReset {
				s.acc = 0
				p.clocking &^= 1 << uint(i)
			} else {
				p.clocking |= 1 << uint(i)
			}
		case 0x04: // EXECCTRL
			oldSticky := s.execctrl&(1<<17) != 0
			s.execctrl = v &^ (uint32(1) << 31)
			if oldSticky && s.execctrl&(1<<17) == 0 {
				s.clearSticky()
			}
		case 0x08: // SHIFTCTRL
			old := s.shiftctrl
			s.shiftctrl = normalizePIOFIFOJoin(v)
			p.chip.Logger.Debug("pio shiftctrl",
				slog.Uint64("cycle", p.chip.Cycles),
				slog.Int("block", p.id), slog.Int("sm", int(i)),
				slog.String("from", fmtHex(old)), slog.String("to", fmtHex(s.shiftctrl)))
			if (old^s.shiftctrl)&pioFIFOJoinMask != 0 {
				s.txN, s.rxN = 0, 0
				p.chip.Logger.Debug("pio fifo flush",
					slog.Uint64("cycle", p.chip.Cycles),
					slog.Int("block", p.id), slog.Int("sm", int(i)))
				p.updateIRQ()
			}
		case 0x0C: // ADDR is read-only
			return
		case 0x10: // INSTR executes immediately, including while disabled
			s.forceInstruction(uint16(v))
		case 0x14: // PINCTRL
			s.pinctrl = v
		}
	}
}

const pioFIFOJoinMask = 1<<14 | 1<<15 | 1<<30 | 1<<31

func normalizePIOFIFOJoin(v uint32) uint32 {
	v &^= 0x3FE0 // reserved bits 13:5
	if v&(1<<14|1<<15) != 0 {
		v &^= 1<<30 | 1<<31
	} else if v&(1<<30|1<<31) != 0 {
		v &^= 1<<14 | 1<<15
	}
	return v
}

// updateIRQ drives the two PIO interrupt lines from shared flags, FIFO status,
// force registers and enables.
func (p *pioBlock) updateIRQ() {
	raw := p.intr()
	for i := range 2 {
		inte := p.block.regs[(pioIRQ0INTE+uint32(i)*0xC)/4]
		intf := p.block.regs[(pioIRQ0INTF+uint32(i)*0xC)/4]
		active := (raw | intf) & inte
		irq := 15 + 2*p.id + i
		if active != 0 {
			p.chip.PendIRQ(irq)
		} else {
			p.chip.ClearPendingIRQ(irq)
		}
	}
}

func (p *pioBlock) intr() uint32 {
	var v uint32
	for i := range p.sm {
		s := &p.sm[i]
		if s.rxCap() > 0 && s.rxN > 0 {
			v |= 1 << uint(i) // RXNEMPTY
		}
		if s.txCap() > 0 && s.txN < s.txCap() {
			v |= 1 << uint(4+i) // TXNFULL
		}
	}
	return v | p.irq<<8
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

	next := p.chip.pio[(p.id+1)%3]
	prev := p.chip.pio[(p.id+2)%3]
	nextMask := (v >> 20) & 0xF
	prevMask := (v >> 16) & 0xF
	enable := v&(1<<24) != 0
	disable := v&(1<<25) != 0
	restartDiv := v&(1<<26) != 0
	applyNeighbour := func(q *pioBlock, mask uint32) {
		if q == nil {
			return
		}
		for i := range q.sm {
			if mask&(1<<uint(i)) == 0 {
				continue
			}
			s := &q.sm[i]
			if disable {
				s.enabled = false
			} else if enable {
				s.enabled = true
			}
			if restartDiv {
				s.acc = 0
			}
		}
		q.refreshActive()
	}
	applyNeighbour(next, nextMask)
	applyNeighbour(prev, prevMask)

	// All restart and NEXTPREV fields are self-clearing strobes. Leaving them
	// set makes a later read-modify-write repeat the operation.
	v &^= 0x07FFFFF0
	p.block.regs[0] = v
	p.active = active
	p.refreshActive()
}

func (p *pioBlock) refreshActive() {
	var active uint8
	for i := range p.sm {
		if p.sm[i].enabled {
			active |= 1 << uint(i)
		}
	}
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
	s.clearSticky()
	s.pc = uint8((s.execctrl >> 7) & 0x1F) // wrap bottom
	s.isr = 0
	s.inCount, s.outCount = 0, 32
	s.delay = 0
	s.pcSet = false
	s.execPending = false
	s.execInstr = 0
	s.stalled = false
	s.stallKind = pioStallNone
	s.stallInstr = 0
	s.stallDelay = 0
	s.execStalled = false
	// OSR, X/Y and the free-running clock divider are deliberately preserved.
}

func (s *pioSM) txCap() int {
	if s.shiftctrl&(1<<30) != 0 {
		return 8
	}
	if s.shiftctrl&(1<<31) != 0 || s.rxPut() || s.rxGet() {
		return 0
	}
	return 4
}

func (s *pioSM) rxCap() int {
	if s.shiftctrl&(1<<31) != 0 {
		return 8
	}
	if s.shiftctrl&(1<<30) != 0 || s.rxPut() || s.rxGet() {
		return 0
	}
	return 4
}

func (s *pioSM) rxPut() bool { return s.shiftctrl&(1<<15) != 0 }
func (s *pioSM) rxGet() bool { return s.shiftctrl&(1<<14) != 0 }

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
	s.p.updateIRQ()
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
	s.p.updateIRQ()
	return v, true
}

func (s *pioSM) pushRx(v uint32) {
	if s.rxFull() {
		s.p.fdebug |= 1 << uint(s.idx) // RXSTALL
		return
	}
	s.rx[s.rxN] = v
	s.rxN++
	s.p.updateIRQ()
}

func (s *pioSM) popRx() uint32 {
	if s.rxN == 0 {
		s.p.fdebug |= 1 << uint(8+s.idx) // RXUNDER
		return 0
	}
	v := s.rx[0]
	copy(s.rx[:], s.rx[1:s.rxN])
	s.rxN--
	s.p.updateIRQ()
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
func (s *pioSM) sticky() bool        { return s.execctrl&(1<<17) != 0 }

func (s *pioSM) clockDivider() uint32 {
	n := (s.clkdiv >> 16) & 0xFFFF
	if n == 0 {
		n = 65536
	}
	return n<<8 | (s.clkdiv>>8)&0xFF
}

// clockTick advances the divider on every system clock, even while disabled.
func (s *pioSM) clockTick() bool {
	thr := s.clockDivider()
	if thr == 256 {
		s.acc = 0
		return true
	}
	s.acc += 256
	if s.acc < thr {
		return false
	}
	s.acc -= thr
	return true
}

func (s *pioSM) advanceClock(cycles uint64) {
	if cycles == 0 {
		return
	}
	thr := uint64(s.clockDivider())
	if thr == 256 {
		s.acc = 0
		return
	}
	s.acc = uint32((uint64(s.acc) + cycles*256) % thr)
}

func (s *pioSM) tick() {
	if !s.clockTick() || !s.enabled {
		return
	}
	s.applySticky()
	if s.delay > 0 {
		s.autoPullBetween()
		s.delay--
		return
	}
	if s.stalled {
		if !isPIOOut(s.stallInstr) && !isPIOPull(s.stallInstr) {
			s.autoPullBetween()
		}
		if !s.resumeStall() {
			return
		}
		source, instr := s.stallSource, s.stallInstr
		s.stalled = false
		s.stallKind = pioStallNone
		s.execStalled = false
		s.finishInstruction(instr, source)
		return
	}

	instr := s.p.mem[s.pc]
	source := pioSourceMemory
	if s.execPending {
		instr = s.execInstr
		source = pioSourceExec
		s.execPending = false
	}
	s.attemptInstruction(instr, source)
}

func (s *pioSM) attemptInstruction(instr uint16, source uint8) {
	s.instructionSource = source
	s.pcSet = false
	ok := s.execute(instr)
	// Main pin writes happen in execute; side-set has priority and also
	// applies immediately when the instruction stalls.
	s.applySide(instr)
	if !ok {
		if !s.stalled {
			s.beginStall(pioStallWait, instr, source)
		}
		if source == pioSourceForced {
			s.execStalled = true
			s.execPending = false // a stalled forced instruction overwrites the EXEC latch
		}
		if !isPIOOut(instr) && !isPIOPull(instr) {
			s.autoPullBetween()
		}
		return
	}
	s.finishInstruction(instr, source)
}

func (s *pioSM) finishInstruction(instr uint16, source uint8) {
	delay := s.instructionDelay(instr)
	if source == pioSourceForced || isPIOExec(instr) {
		delay = 0
	}
	s.delay = delay
	if source == pioSourceMemory && !s.pcSet {
		s.advance()
	}
	if !isPIOOut(instr) {
		s.autoPullBetween()
	}
}

func isPIOOut(instr uint16) bool { return instr>>13 == 3 }

func isPIOPull(instr uint16) bool {
	return instr>>13 == 4 && instr&0x80 != 0 && instr&0x10 == 0
}

func isPIOExec(instr uint16) bool {
	if instr>>13 == 3 {
		return (instr>>5)&7 == 7
	}
	return instr>>13 == 5 && (instr>>5)&7 == 4
}

func (s *pioSM) beginStall(kind uint8, instr uint16, source uint8) {
	if s.stalled {
		return
	}
	s.stalled = true
	s.stallKind = kind
	s.stallInstr = instr
	s.stallDelay = s.instructionDelay(instr)
	s.stallSource = source
}

func (s *pioSM) resumeStall() bool {
	switch s.stallKind {
	case pioStallWait:
		return s.execWait(s.stallInstr)
	case pioStallIRQ:
		return s.execIrq(s.stallInstr)
	case pioStallPush:
		if s.rxFull() {
			return false
		}
		s.pushRx(s.isr)
		s.isr = 0
		s.inCount = 0
		return true
	case pioStallPull:
		if s.txN == 0 {
			s.p.fdebug |= 1 << uint(24+s.idx) // TXSTALL
			return false
		}
		s.osr, _ = s.popTx()
		s.outCount = 0
		return true
	case pioStallOutRefill:
		if s.txN == 0 {
			s.p.fdebug |= 1 << uint(24+s.idx) // TXSTALL
			return false
		}
		return s.execOut(s.stallInstr)
	case pioStallInPush:
		if s.rxFull() {
			return false
		}
		s.pushRx(s.isr)
		s.isr = 0
		s.inCount = 0
		return true
	default:
		return true
	}
}

// autoPullBetween performs the asynchronous refill allowed on non-OUT cycles.
func (s *pioSM) autoPullBetween() {
	if !s.autopull() || s.outCount < s.pullThresh() {
		return
	}
	if v, ok := s.popTx(); ok {
		s.osr = v
		s.outCount = 0
	}
}

func (s *pioSM) forceInstruction(instr uint16) {
	s.applySticky()
	if instr == 0xA042 && s.execStalled { // NOP clears a stalled forced instruction
		s.stalled = false
		s.stallKind = pioStallNone
		s.execStalled = false
		return
	}
	if s.stalled {
		s.stalled = false
		s.stallKind = pioStallNone
		s.execStalled = false
	}
	s.attemptInstruction(instr, pioSourceForced)
}

func (s *pioSM) advance() {
	if s.pc == uint8((s.execctrl>>12)&0x1F) {
		s.pc = uint8((s.execctrl >> 7) & 0x1F)
	} else {
		s.pc = (s.pc + 1) & 0x1F
	}
}

func (s *pioSM) sideSet(instr uint16) (base, count int, v uint32, dirs, enabled bool) {
	sc := int((s.pinctrl >> 29) & 7)
	opt := s.execctrl&(1<<30) != 0
	used := sc
	if opt {
		used++
	}
	if used == 0 {
		return 0, 0, 0, false, false
	}
	base = int((s.pinctrl >> 10) & 0x1F)
	dirs = s.execctrl&(1<<29) != 0
	if opt {
		enabled = (instr>>12)&1 != 0 && sc > 0
		if enabled {
			v = uint32(instr>>uint(12-sc)) & (1<<uint(sc) - 1)
		}
		count = sc
	} else {
		enabled = true
		count = sc
		v = uint32(instr>>uint(13-sc)) & (1<<uint(sc) - 1)
	}
	return base, count, v, dirs, enabled
}

func (s *pioSM) applySide(instr uint16) {
	base, count, v, dirs, enabled := s.sideSet(instr)
	if enabled {
		s.writePinRange(base, count, v, dirs)
	}
}

func (s *pioSM) instructionDelay(instr uint16) int {
	sc := int((s.pinctrl >> 29) & 7)
	if s.execctrl&(1<<30) != 0 {
		sc++
	}
	if sc >= 5 {
		return 0
	}
	return int(instr>>8) & (1<<uint(5-sc) - 1)
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
	if count >= 32 {
		s.writePinMask(base, ^uint32(0), v, dirs)
		return
	}
	s.writePinMask(base, 1<<uint(count)-1, v, dirs)
}

func (s *pioSM) writePinMask(base int, mask, v uint32, dirs bool) {
	for i := range 32 {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
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

func (s *pioSM) pinMask(base, count int) uint32 {
	if count <= 0 {
		return 0
	}
	if count >= 32 {
		return ^uint32(0)
	}
	return (1<<uint(count) - 1) << uint(base&31)
}

func (s *pioSM) writeMainRange(base, count int, v uint32, dirs bool) {
	s.writePinRange(base, count, v, dirs)
	if !s.sticky() {
		return
	}
	mask := s.pinMask(base, count)
	if dirs {
		s.stickyDirMask = mask
		s.stickyDirVal = v & mask
	} else {
		s.stickyLevelMask = mask
		s.stickyLevelVal = v & mask
	}
}

func (s *pioSM) applySticky() {
	if !s.sticky() {
		return
	}
	if s.stickyLevelMask != 0 {
		s.writePinMask(0, s.stickyLevelMask, s.stickyLevelVal, false)
	}
	if s.stickyDirMask != 0 {
		s.writePinMask(0, s.stickyDirMask, s.stickyDirVal, true)
	}
}

func (s *pioSM) clearSticky() {
	if s.stickyLevelMask != 0 {
		s.writePinMask(0, s.stickyLevelMask, 0, false)
	}
	if s.stickyDirMask != 0 {
		s.writePinMask(0, s.stickyDirMask, 0, true)
	}
	s.stickyLevelMask, s.stickyLevelVal = 0, 0
	s.stickyDirMask, s.stickyDirVal = 0, 0
}

func (s *pioSM) deassertSticky(dirs bool) {
	if dirs {
		if s.stickyDirMask != 0 {
			s.writePinMask(0, s.stickyDirMask, 0, true)
		}
		s.stickyDirMask, s.stickyDirVal = 0, 0
		return
	}
	if s.stickyLevelMask != 0 {
		s.writePinMask(0, s.stickyLevelMask, 0, false)
	}
	s.stickyLevelMask, s.stickyLevelVal = 0, 0
}

func (s *pioSM) writeOutRange(base, count int, v uint32, dirs bool) {
	if s.execctrl&(1<<18) != 0 && v&(1<<uint((s.execctrl>>19)&0x1F)) == 0 {
		if s.sticky() {
			s.deassertSticky(dirs)
		}
		return
	}
	s.writeMainRange(base, count, v, dirs)
}

func (s *pioSM) readPin(offset int) bool {
	offset &= 31
	pin := s.p.pinAt(offset)
	if s.p.inputBypass&(1<<uint(offset)) != 0 {
		return s.p.chip.Pins.PadInput(pin)
	}
	return s.p.inputSync2&(1<<uint(pin)) != 0
}

func (s *pioSM) readAbsolutePin(pin int) bool {
	if pin < 0 || pin >= 48 {
		return false
	}
	offset := pin - s.p.gpioBase()
	if offset >= 0 && offset < 32 && s.p.inputBypass&(1<<uint(offset)) != 0 {
		return s.p.chip.Pins.PadInput(pin)
	}
	return s.p.inputSync2&(1<<uint(pin)) != 0
}

func (s *pioSM) readPins(base int) uint32 {
	var v uint32
	for i := range 32 {
		if s.readPin(base + i) {
			v |= 1 << uint(i)
		}
	}
	return v
}

func (s *pioSM) inCountPins() int {
	n := int(s.shiftctrl & 0x1F)
	if n == 0 {
		return 32
	}
	return n
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
		return s.execIn(instr)
	case 3: // OUT
		return s.execOut(instr)
	case 4: // PUSH / PULL / MOV RX FIFO
		if instr&0x10 != 0 { // MOV RX FIFO (PUT and GET)
			s.execMovFIFO(instr)
			return true
		}
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
		jump = s.x != 0
		s.x--
	case 3:
		jump = s.y == 0
	case 4:
		jump = s.y != 0
		s.y--
	case 5:
		jump = s.x != s.y
	case 6:
		jump = s.readPin(int((s.execctrl >> 24) & 0x1F))
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
	var level bool
	switch src {
	case 0: // GPIO is an absolute system GPIO, not affected by GPIOBASE
		level = s.readAbsolutePin(index)
	case 1: // PIN is relative to IN_BASE and masked by IN_COUNT
		if index < s.inCountPins() {
			level = s.readPin(int((s.pinctrl>>15)&0x1F) + index)
		}
	case 2: // IRQ
		mode := (index >> 3) & 3
		flag := index & 7
		p := s.p
		switch mode {
		case 2: // relative to this state machine
			flag = (flag &^ 3) | ((flag + s.idx) & 3)
		case 1: // previous PIO block
			p = s.p.chip.pio[(s.p.id+2)%3]
		case 3: // next PIO block
			p = s.p.chip.pio[(s.p.id+1)%3]
		}
		if p == nil {
			return false
		}
		if (p.irq&(1<<uint(flag)) != 0) != pol {
			return false
		}
		if pol {
			p.irq &^= 1 << uint(flag)
			p.block.regs[0x30/4] = p.irq
			p.updateIRQ()
		}
		return true
	case 3: // JMPPIN plus an offset of 0..3, modulo 32
		level = s.readPin(int((s.execctrl>>24)&0x1F) + index)
	}
	return level == pol
}

func (s *pioSM) inSource(src int) uint32 {
	switch src {
	case 0:
		return s.readPins(int((s.pinctrl>>15)&0x1F)) & pinCountMask(s.inCountPins())
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

func pinCountMask(count int) uint32 {
	if count <= 0 {
		return 0
	}
	if count >= 32 {
		return ^uint32(0)
	}
	return 1<<uint(count) - 1
}

func (s *pioSM) execIn(instr uint16) bool {
	count := int(instr & 0x1F)
	if count == 0 {
		count = 32
	}
	v := s.inSource(int(instr>>5) & 7)
	s.isr = shiftIn(s.isr, v, count, s.inShiftRight())
	s.inCount = min(s.inCount+count, 32)
	if s.autopush() && s.inCount >= s.pushThresh() {
		if s.rxFull() {
			s.p.fdebug |= 1 << uint(s.idx) // RXSTALL
			s.beginStall(pioStallInPush, instr, s.instructionSource)
			return false
		}
		s.pushRx(s.isr)
		s.isr = 0
		s.inCount = 0
	}
	return true
}

func shiftIn(cur, data uint32, n int, right bool) uint32 {
	if n >= 32 {
		return data
	}
	data &= 1<<uint(n) - 1
	if right {
		return cur>>uint(n) | data<<uint(32-n)
	}
	return cur<<uint(n) | data
}

func (s *pioSM) execOut(instr uint16) bool {
	count := int(instr & 0x1F)
	if count == 0 {
		count = 32
	}
	dest := int(instr>>5) & 7
	right := s.outShiftRight()
	sticky := s.sticky()

	if s.autopull() && s.outCount >= s.pullThresh() {
		if v, ok := s.popTx(); ok {
			s.osr = v
			s.outCount = 0
		} else {
			s.p.fdebug |= 1 << uint(24+s.idx) // TXSTALL
		}
		// Hardware cannot fill an empty OSR and OUT it in the same cycle.
		s.beginStall(pioStallOutRefill, instr, s.instructionSource)
		return false
	}
	if !s.autopull() && s.outCount >= 32 {
		// Intentional TinyGo compatibility: SetX/SetY push a word and execute
		// OUT X/Y,32 with autopull disabled. Retain only this permissive refill.
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
	s.outCount = min(s.outCount+count, 32)

	switch dest {
	case 0: // PINS
		s.writeOutRange(int(s.pinctrl&0x1F), s.outCountPins(), v, false)
	case 1:
		s.x = v
	case 2:
		s.y = v
	case 3:
	case 4: // PINDIRS
		s.writeOutRange(int(s.pinctrl&0x1F), s.outCountPins(), v, true)
	case 5: // PC
		s.pc = uint8(v & 0x1F)
		s.pcSet = true
	case 6: // ISR also sets the input shift counter
		s.isr = v
		s.inCount = count
	case 7: // EXEC executes on the next enabled SM cycle
		s.execPending = true
		s.execInstr = uint16(v)
	}

	if s.autopull() && s.outCount >= s.pullThresh() {
		if next, ok := s.popTx(); ok {
			s.osr = next
			s.outCount = 0
		}
	}
	return true
}

func (s *pioSM) execMovFIFO(instr uint16) {
	index := uint32(instr & 7)
	if instr&(1<<3) == 0 {
		index = s.y & 7
	}
	index &= 3
	if instr&(1<<7) == 0 { // mov rxfifo[index], isr
		if !s.rxPut() {
			s.p.chip.CPU0.Halt("PIO PUT without FJOIN_RX_PUT")
			return
		}
		s.rx[index] = s.isr
		return
	}
	if !s.rxGet() {
		s.p.chip.CPU0.Halt("PIO GET without FJOIN_RX_GET")
		return
	}
	s.osr = s.rx[index]
	s.outCount = 0
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
			if block {
				s.beginStall(pioStallPush, instr, s.instructionSource)
				return false
			}
			// Nonblocking PUSH drops the data but still clears the ISR.
			s.isr = 0
			s.inCount = 0
			return true
		}
		s.pushRx(s.isr)
		s.isr = 0
		s.inCount = 0
		return true
	}

	// PULL
	ifEmpty := instr&0x40 != 0
	block := instr&0x20 != 0
	if s.autopull() && s.outCount == 0 {
		return true // barrier: the OSR is already full
	}
	if ifEmpty && s.outCount < s.pullThresh() {
		return true
	}
	if s.txN == 0 {
		if block {
			s.p.fdebug |= 1 << uint(24+s.idx) // TXSTALL
			s.beginStall(pioStallPull, instr, s.instructionSource)
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
		v = s.readPins(int((s.pinctrl>>15)&0x1F)) & pinCountMask(s.inCountPins())
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
		s.writeMainRange(int(s.pinctrl&0x1F), s.outCountPins(), v, false)
	case 1:
		s.x = v
	case 2:
		s.y = v
	case 3: // PINDIRS (RP2350)
		s.writeMainRange(int(s.pinctrl&0x1F), s.outCountPins(), v, true)
	case 4: // EXEC
		s.execPending = true
		s.execInstr = uint16(v)
	case 5: // PC
		s.pc = uint8(v & 0x1F)
		s.pcSet = true
	case 6:
		s.isr = v
		s.inCount = 0
	case 7:
		s.osr = v
		s.outCount = 0
	}
}

func (s *pioSM) movStatus() uint32 {
	sel := (s.execctrl >> 5) & 3
	n := (s.execctrl >> 0) & 0x1F
	switch sel {
	case 0: // TX FIFO level < N
		if uint32(s.txN) < n {
			return ^uint32(0)
		}
	case 1: // RX FIFO level < N
		if uint32(s.rxN) < n {
			return ^uint32(0)
		}
	case 2: // IRQ, optionally from the previous/next PIO block
		p := s.p
		flag := n & 7
		if n >= 8 && n < 16 {
			p = s.p.chip.pio[(s.p.id+2)%3]
		} else if n >= 16 && n < 24 {
			p = s.p.chip.pio[(s.p.id+1)%3]
		}
		if p != nil && p.irq&(1<<uint(flag)) != 0 {
			return ^uint32(0)
		}
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
	case 1: // set and wait for software to clear
		if s.stalled && s.stallKind == pioStallIRQ {
			return p.irq&(1<<uint(flag)) == 0
		}
		p.irq |= 1 << uint(flag)
		p.block.regs[0x30/4] = p.irq
		p.updateIRQ()
		s.beginStall(pioStallIRQ, instr, s.instructionSource)
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
		s.writeMainRange(base, count, v, false)
	case 1:
		s.x = v
	case 2:
		s.y = v
	case 4: // PINDIRS
		s.writeMainRange(base, count, v, true)
	}
}

// ---- input synchronizers and block tick ----

func (c *Chip) pioInputLevels() uint64 {
	p := c.Pins
	out, oe, pulls := p.Out, p.OE, p.pullUpMask
	pwmPins := p.pwmPins
	if !c.pioInputValid {
		// Tests and board models may initialise pin arrays directly before
		// the first PIO tick. Normal firmware writes maintain the summaries.
		pulls, pwmPins = 0, 0
		for pin := range 48 {
			bit := uint64(1) << uint(pin)
			if p.PullUp[pin] {
				pulls |= bit
			}
			if p.Func[pin] == pwmFuncSel {
				pwmPins |= bit
			}
		}
	}
	out, oe, pulls = p.padView(out, oe, pulls)
	stable := c.pioInputValid && c.pioInputOut == p.Out && c.pioInputOE == p.OE &&
		c.pioInputExtLevel == p.ExtLevel && c.pioInputExtValid == p.ExtValid &&
		c.pioInputPulls == pulls && c.pioInputPWMPins == pwmPins
	if stable && pwmPins == 0 {
		return c.pioInputCacheLevels
	}

	ext := p.ExtValid
	levels := (out & oe) | (p.ExtLevel &^ oe & ext) | (pulls &^ oe &^ ext)
	// A pad with input enable clear presents 0 to every peripheral input.
	levels &= p.padIE
	// PWM can change a driven level between microsecond ticks. Only those
	// pins need the cycle-resolution override; ordinary pins stay bit-parallel.
	if p.LevelOverride != nil {
		for pwmPins != 0 {
			pin := bits.TrailingZeros64(pwmPins)
			pwmPins &^= 1 << uint(pin)
			bit := uint64(1) << uint(pin)
			if p.isolated(pin) || p.padIE&bit == 0 || oe&bit == 0 {
				continue
			}
			if level, ok := p.LevelOverride(pin); ok {
				if level {
					levels |= bit
				} else {
					levels &^= bit
				}
			}
		}
	}
	c.pioInputValid = true
	c.pioInputOut, c.pioInputOE = p.Out, p.OE
	c.pioInputExtLevel, c.pioInputExtValid = p.ExtLevel, p.ExtValid
	c.pioInputPulls, c.pioInputPWMPins = pulls, p.pwmPins
	c.pioInputCacheLevels = levels
	return levels
}

func (p *pioBlock) tickWithInputs(raw uint64) {
	if raw != p.inputLast {
		p.inputLast = raw
		p.inputPending = 2
	}
	if p.inputPending != 0 {
		bypass := uint64(p.inputBypass) << uint(p.gpioBase())
		old1 := p.inputSync1
		p.inputSync1 = raw
		p.inputSync2 = (old1 &^ bypass) | (raw & bypass)
		p.inputPending--
	}
	for mask := p.active | p.clocking; mask != 0; {
		i := bits.TrailingZeros8(mask)
		mask &^= 1 << uint(i)
		p.sm[i].tick()
	}
}

func (p *pioBlock) advanceCycles(cycles uint64, raw uint64) {
	if cycles == 0 {
		return
	}
	for mask := p.active | p.clocking; mask != 0; {
		i := bits.TrailingZeros8(mask)
		mask &^= 1 << uint(i)
		p.sm[i].advanceClock(cycles)
	}
	if raw != p.inputLast {
		p.inputLast = raw
		p.inputPending = 2
	}
	if p.inputPending != 0 {
		if cycles == 1 {
			bypass := uint64(p.inputBypass) << uint(p.gpioBase())
			old1 := p.inputSync1
			p.inputSync1 = raw
			p.inputSync2 = (old1 &^ bypass) | (raw & bypass)
			p.inputPending--
		} else {
			p.inputSync1 = raw
			p.inputSync2 = raw
			p.inputPending = 0
		}
	}
}

// tick advances one PIO block by one CPU cycle.
func (p *pioBlock) tick() {
	p.tickWithInputs(p.chip.pioInputLevels())
}
