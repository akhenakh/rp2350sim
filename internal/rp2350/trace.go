package rp2350

// TraceEvent is one ITM stimulus write or DWT sample.
type TraceEvent struct {
	Cycle uint64
	Core  int
	Kind  string
	Port  int
	Data  uint32
	PC    uint32
	Text  string
}

const (
	demcr   = 0xE000EDFC
	itmBase = 0xE0000000
	itmTER  = 0xE0000E00
	itmTPR  = 0xE0000E40
	itmTCR  = 0xE0000E80
	itmLAR  = 0xE0000FB0
	itmLSR  = 0xE0000FB4
	dwtBase = 0xE0001000
	dwtCTRL = 0xE0001000
	dwtCYCC = 0xE0001004
	dwtPCSR = 0xE000101C
	dwtLAR  = 0xE0001FB0
	dwtLSR  = 0xE0001FB4
)

func traceCore(core int) int {
	if core < 0 || core > 1 {
		return 0
	}
	return core
}

// traceRead32Core services the core-local ITM/DWT registers.
func (c *Chip) traceRead32Core(coreArg int, addr uint32) (uint32, bool) {
	core := traceCore(coreArg)
	switch {
	case addr >= itmBase && addr < itmBase+0x80:
		return 1, true // stimulus FIFO is always ready
	case addr == itmTER:
		return c.itmTER[core], true
	case addr == itmTPR:
		return c.itmTPR[core], true
	case addr == itmTCR:
		return c.itmTCR[core], true
	case addr == itmLAR || addr == dwtLAR:
		return 0, true // no lock gate on the emulated trace blocks
	case addr == itmLSR || addr == dwtLSR:
		return 0, true // unlocked
	case addr == dwtCTRL:
		return c.dwtCTRL[core], true
	case addr == dwtCYCC:
		if !c.dwtCYCRun[core] {
			return c.dwtCYCVal[core], true
		}
		return uint32(c.Cycles - c.dwtCYCBase[core]), true
	case addr == demcr:
		return c.demcr[core], true
	case addr == dwtPCSR:
		if core == 1 {
			return c.CPU1.R[15], true
		}
		return c.CPU0.R[15], true
	}
	return 0, false
}

// traceReadN reads a byte or halfword from the trace registers.
func (c *Chip) traceReadN(addr uint32, size int) (uint32, bool) {
	v, ok := c.traceRead32Core(c.core, addr&^3)
	if !ok {
		return 0, false
	}
	shift := (addr & 3) * 8
	mask := uint32(0xFF) << shift
	if size == 2 {
		mask = 0xFFFF << shift
	}
	return (v & mask) >> shift, true
}

// traceWriteN writes a byte or halfword. Stimulus ports accept narrow writes;
// each accessed byte is emitted to the word's port. Other ITM/DWT registers
// get a read-modify-write.
func (c *Chip) traceWriteN(addr uint32, v uint32, size int) bool {
	if addr >= itmBase && addr < itmBase+0x80 {
		port := int((addr - itmBase) / 4)
		for i := range size {
			c.emitITM(c.core, port, byte(v>>(8*i)))
		}
		return true
	}
	aligned := addr &^ 3
	switch aligned {
	case itmTER, itmTPR, itmTCR, dwtCTRL, itmLAR, dwtLAR:
		old, _ := c.traceRead32Core(c.core, aligned)
		shift := (addr & 3) * 8
		mask := uint32(0xFF) << shift
		if size == 2 {
			mask = 0xFFFF << shift
		}
		c.traceWrite32Core(c.core, aligned, old&^mask|(v<<shift)&mask)
		return true
	}
	return false
}

// traceWrite32Core services the core-local ITM/DWT registers.
func (c *Chip) traceWrite32Core(coreArg int, addr, v uint32) bool {
	core := traceCore(coreArg)
	switch {
	case addr >= itmBase && addr < itmBase+0x80:
		port := int((addr - itmBase) / 4)
		for i := range 4 {
			c.emitITM(core, port, byte(v>>(8*i)))
		}
		return true
	case addr == itmTER:
		c.itmTER[core] = v
		return true
	case addr == itmTPR:
		c.itmTPR[core] = v
		return true
	case addr == itmTCR:
		c.itmTCR[core] = v
		return true
	case addr == itmLAR || addr == dwtLAR:
		return true
	case addr == itmLSR || addr == dwtLSR:
		return true
	case addr == demcr:
		c.demcr[core] = v
		return true
	case addr == dwtCTRL:
		old := c.dwtCTRL[core]
		enable := v&1 != 0
		if old&1 != 0 && !enable {
			c.dwtCYCVal[core] = uint32(c.Cycles - c.dwtCYCBase[core])
		} else if old&1 == 0 && enable {
			c.dwtCYCBase[core] = c.Cycles - uint64(c.dwtCYCVal[core])
		}
		c.dwtCTRL[core] = v
		c.dwtCYCRun[core] = enable
		return true
	case addr == dwtCYCC:
		c.dwtCYCBase[core] = c.Cycles
		c.dwtCYCVal[core] = 0
		return true
	}
	return false
}

func (c *Chip) emitITM(core, port int, b byte) {
	if c.itmTCR[core]&1 == 0 || c.itmTER[core]&(1<<uint(port)) == 0 {
		return
	}
	ev := TraceEvent{
		Kind: "itm", Port: port, Data: uint32(b), Text: string([]byte{b}),
		PC: c.CPU0.R[15],
	}
	if core == 1 {
		ev.PC = c.CPU1.R[15]
	}
	c.emitTraceAt(core, ev)
}

func (c *Chip) emitTraceAt(core int, ev TraceEvent) {
	ev.Cycle = c.Cycles
	ev.Core = core
	if c.OnTrace != nil {
		c.OnTrace(ev)
	}
}
