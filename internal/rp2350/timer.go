package rp2350

// timerBlock models one of the two 64-bit RP2350 system timers.
type timerBlock struct {
	block *regBlock
	chip  *Chip
	idx   int

	alarm    [4]uint32
	armed    uint32
	intR     uint32
	intE     uint32
	intF     uint32
	pause    uint32
	dbgPause uint32
	locked   uint32
	source   uint32
}

func (t *timerBlock) init(c *Chip, idx int) {
	t.chip = c
	t.idx = idx
	base := uint32(0x400B0000)
	if idx == 1 {
		base = 0x400B8000
	}
	t.block = newRegBlock(base, 0x1000)
	t.block.readFn = t.read
	t.block.writeFn = t.write
}

func (t *timerBlock) reset() {
	t.alarm = [4]uint32{}
	t.armed = 0
	t.intR = 0
	t.intE = 0
	t.intF = 0
	t.pause = 0
	t.dbgPause = 0
	t.locked = 0
	t.source = 0
}

func (t *timerBlock) read(c *Chip, off uint32) (uint32, bool) {
	now := c.NowMicros()
	switch off {
	case 0x00: // TIMEHW
		return uint32(now >> 32), true
	case 0x04: // TIMELW
		// A write to TIMELW latches the high word; reads return the latched value.
		return t.block.regs[1], true
	case 0x08: // TIMEHR
		return uint32(now >> 32), true
	case 0x0C: // TIMELR
		return uint32(now), true
	case 0x24: // TIMERAWH
		return uint32(now >> 32), true
	case 0x28: // TIMERAWH? TIMERAWL
		return uint32(now), true
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
	switch {
	case off == 0x04, off == 0x08: // TIMELW / TIMEHR: ignore time writes
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
		t.dbgPause = v
	case off == 0x30:
		t.pause = v
	case off == 0x34:
		t.locked = v
	case off == 0x38:
		t.source = v
	case off == 0x3C: // INTR write-1-to-clear
		t.intR &^= v
		t.block.regs[off/4] = t.intR
	case off == 0x40:
		t.intE = v
	case off == 0x44:
		t.intF = v
	}
}

// tick checks alarms against the current microsecond time.
func (t *timerBlock) tick(now uint64) {
	if t.armed == 0 {
		return
	}
	for i := 0; i < 4; i++ {
		bit := uint32(1) << uint(i)
		if t.armed&bit == 0 {
			continue
		}
		if int32(uint32(now)-t.alarm[i]) >= 0 {
			t.armed &^= bit
			t.intR |= bit
			t.chip.NVIC.Pend(t.idx*4 + i)
			t.chip.CPU0.Sleeping = false
			t.chip.CPU1.Sleeping = false
		}
	}
	t.block.regs[0x20/4] = t.armed
}
