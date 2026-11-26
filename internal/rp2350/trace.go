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
	itmBase = 0xE0000000
	itmTER  = 0xE0000E00
	itmTPR  = 0xE0000E40
	itmTCR  = 0xE0000E80
	dwtBase = 0xE0001000
	dwtCTRL = 0xE0001000
	dwtCYCC = 0xE0001004
	dwtPCSR = 0xE000101C
)

// traceRead32 services ITM/DWT registers. ok is false for other addresses.
func (c *Chip) traceRead32(addr uint32) (uint32, bool) {
	switch {
	case addr >= itmBase && addr < itmBase+0x80:
		return 1, true // stimulus FIFO is always ready
	case addr == itmTER:
		return c.itmTER, true
	case addr == itmTPR:
		return c.itmTPR, true
	case addr == itmTCR:
		return c.itmTCR, true
	case addr == dwtCTRL:
		return c.dwtCTRL, true
	case addr == dwtCYCC:
		return uint32(c.Cycles), true
	case addr == dwtPCSR:
		return c.CPU0.R[15], true
	}
	return 0, false
}

// traceReadN reads a byte or halfword from the trace registers by extracting
// the selected lanes from the containing word.
func (c *Chip) traceReadN(addr uint32, size int) (uint32, bool) {
	v, ok := c.traceRead32(addr &^ 3)
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
// the data is shifted into the containing word, and the word writer emits the
// non-zero lanes. Other ITM/DWT registers get a read-modify-write.
func (c *Chip) traceWriteN(addr uint32, v uint32, size int) bool {
	if addr >= itmBase && addr < itmBase+0x80 {
		c.traceWrite32(addr&^3, v<<((addr&3)*8))
		return true
	}
	aligned := addr &^ 3
	switch aligned {
	case itmTER, itmTPR, itmTCR, dwtCTRL:
		old, _ := c.traceRead32(aligned)
		shift := (addr & 3) * 8
		mask := uint32(0xFF) << shift
		if size == 2 {
			mask = 0xFFFF << shift
		}
		c.traceWrite32(aligned, old&^mask|(v<<shift)&mask)
		return true
	}
	return false
}

// traceWrite32 services ITM/DWT writes. ok is false for other addresses.
func (c *Chip) traceWrite32(addr, v uint32) bool {
	switch {
	case addr >= itmBase && addr < itmBase+0x80:
		port := int((addr - itmBase) / 4)
		if c.itmTCR&1 != 0 && c.itmTER&(1<<uint(port)) != 0 {
			for shift := 0; shift < 32; shift += 8 {
				b := byte(v >> uint(shift))
				if b != 0 {
					c.emitTrace(TraceEvent{Kind: "itm", Port: port, Data: uint32(b), Text: string([]byte{b}), PC: c.CPU0.R[15]})
				}
			}
		}
		return true
	case addr == itmTER:
		c.itmTER = v
		return true
	case addr == itmTPR:
		c.itmTPR = v
		return true
	case addr == itmTCR:
		c.itmTCR = v
		return true
	case addr == dwtCTRL:
		c.dwtCTRL = v
		return true
	case addr == dwtCYCC:
		return true
	}
	return false
}

func (c *Chip) emitTrace(ev TraceEvent) {
	ev.Cycle = c.Cycles
	ev.Core = c.core
	if c.OnTrace != nil {
		c.OnTrace(ev)
	}
}
