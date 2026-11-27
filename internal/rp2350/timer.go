package rp2350

// timerBlock models one of the two 64-bit RP2350 system timers.
type timerBlock struct {
	block *regBlock
	chip  *Chip
	idx   int

	count      uint64
	lastCycles uint64

	alarm      [4]uint32
	armed      uint32
	latchHigh  uint32
	latchValid bool
	intR       uint32
	intE       uint32
	intF       uint32
	pause      uint32
	dbgPause   uint32
	locked     uint32
	source     uint32
}

func (t *timerBlock) init(c *Chip, idx int) {
	t.chip = c
	t.idx = idx
	base := uint32(0x400B0000)
	if idx == 1 {
		base = 0x400B8000
	}
	t.block = newRegBlock(base, 0x1000)
	t.block.resetFn = func(c *Chip) { t.reset() }
	t.block.readFn = t.read
	t.block.writeFn = t.write
	t.block.clearFn = func(c *Chip, off, v uint32) {
		if off == 0x3C { // INTR is write-1-to-clear; the alias must clear bits
			t.intR &^= v & 0xF
			t.block.regs[off/4] = t.intR
			t.pend()
			return
		}
		t.block.write(c, off, t.block.read(c, off)&^v)
	}
}

func (t *timerBlock) reset() {
	t.count = 0
	t.lastCycles = 0
	t.alarm = [4]uint32{}
	t.armed = 0
	t.latchHigh = 0
	t.latchValid = false
	t.intR = 0
	t.intE = 0
	t.intF = 0
	t.pause = 0
	t.dbgPause = 0x6
	t.locked = 0
	t.source = 0
	t.block.regs[0x2C/4] = t.dbgPause
	t.pend()
}

func (t *timerBlock) paused() bool {
	if t.pause&1 != 0 {
		return true
	}
	if t.chip.Debug == nil || !t.chip.Debug.Stopped {
		return false
	}
	core := t.chip.Debug.StopCore
	if core < 0 || core > 1 {
		return false
	}
	return t.dbgPause&(uint32(2)<<uint(core)) != 0
}

func (t *timerBlock) read(c *Chip, off uint32) (uint32, bool) {
	t.tick(c.Cycles)
	switch off {
	case 0x00: // TIMEHW
		return uint32(t.count >> 32), true
	case 0x04: // TIMELW
		return t.block.regs[1], true
	case 0x08: // TIMEHR
		if t.latchValid {
			t.latchValid = false
			return t.latchHigh, true
		}
		return uint32(t.count >> 32), true
	case 0x0C: // TIMELR
		t.latchHigh = uint32(t.count >> 32)
		t.latchValid = true
		return uint32(t.count), true
	case 0x20: // ARMED
		return t.armed, true
	case 0x24: // TIMERAWH
		return uint32(t.count >> 32), true
	case 0x28: // TIMERAWL
		return uint32(t.count), true
	case 0x2C: // DBGPAUSE
		return t.dbgPause, true
	case 0x30: // PAUSE
		return t.pause, true
	case 0x34: // LOCKED
		return t.locked, true
	case 0x38: // SOURCE
		return t.source, true
	case 0x3C: // INTR
		return t.intR, true
	case 0x40: // INTE
		return t.intE, true
	case 0x44: // INTF
		return t.intF, true
	case 0x48: // INTS
		return (t.intR | t.intF) & t.intE, true
	}
	if off >= 0x10 && off < 0x20 {
		return t.alarm[(off-0x10)/4], true
	}
	return 0, false
}

func (t *timerBlock) write(c *Chip, off, v uint32) {
	if off == 0x34 { // LOCKED is sticky until reset.
		t.locked |= v & 1
		t.block.regs[off/4] = t.locked
		return
	}
	if t.locked != 0 {
		return
	}
	t.tick(c.Cycles)
	switch {
	case off == 0x00, off == 0x04, off == 0x08: // time writes are not modelled
	case off == 0x0C: // TIMELR read alias
	case off >= 0x10 && off < 0x20:
		// Writing an alarm value arms it.
		i := (off - 0x10) / 4
		t.alarm[i] = v
		t.armed |= 1 << i
		t.block.regs[off/4] = v
		t.block.regs[0x20/4] = t.armed
	case off == 0x20: // ARMED: write 1 to disarm
		t.armed &^= v & 0xF
		t.block.regs[off/4] = t.armed
	case off == 0x2C:
		t.dbgPause = v & 0x6
		t.block.regs[off/4] = t.dbgPause
	case off == 0x30:
		t.pause = v & 1
		t.block.regs[off/4] = t.pause
	case off == 0x38:
		t.source = v & 1
		t.block.regs[off/4] = t.source
	case off == 0x3C: // INTR write-1-to-clear
		t.intR &^= v & 0xF
		t.block.regs[off/4] = t.intR
		t.pend()
	case off == 0x40:
		t.intE = v & 0xF
		t.block.regs[off/4] = t.intE
		t.pend()
	case off == 0x44: // INTF is read/write force state
		t.intF = v & 0xF
		t.block.regs[off/4] = t.intF
		t.pend()
	}
}

// tick advances the counter from the selected source and checks alarms. The
// tick source keeps running while the counter is paused, so resuming does not
// replay ticks that occurred during the pause.
func (t *timerBlock) tick(cycles uint64) {
	if cycles < t.lastCycles {
		t.lastCycles = cycles
		return
	}
	previous := t.lastCycles
	elapsed := cycles - previous
	t.lastCycles = cycles
	if t.paused() {
		return
	}
	if t.source&1 != 0 {
		t.count += elapsed
	} else {
		t.count += cycles/cyclesPerMicrosecond - previous/cyclesPerMicrosecond
	}
	if t.armed == 0 {
		return
	}
	fired := false
	for i := range 4 {
		bit := uint32(1) << uint(i)
		if t.armed&bit == 0 {
			continue
		}
		if int32(uint32(t.count)-t.alarm[i]) >= 0 {
			t.armed &^= bit
			t.intR |= bit
			fired = true
		}
	}
	// An armed alarm that has not expired changes nothing, so the IRQ lines
	// and the ARMED register stay as they are. Re-evaluating them every cycle
	// dominated busy firmware with a far-future alarm.
	if fired {
		t.block.regs[0x20/4] = t.armed
		t.pend()
	}
}

// pend resynchronizes every timer IRQ from the masked raw/forced state.
func (t *timerBlock) pend() {
	active := (t.intR | t.intF) & t.intE
	for i := range 4 {
		if active&(1<<uint(i)) != 0 {
			t.chip.PendIRQ(t.idx*4 + i)
		} else {
			t.chip.ClearPendingIRQ(t.idx*4 + i)
		}
	}
}
