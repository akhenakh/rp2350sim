package rp2350

import "math/bits"

// dmaBlock models the RP2350 DMA controller. Transfers are paced at one
// element per cycle so BUSY has a real duration and a channel raises its
// completion interrupt when it drains. DREQ pacing is not modelled, so a
// transfer runs as fast as the engine allows.
type dmaBlock struct {
	block *regBlock
	chip  *Chip
	ch    [16]dmaChannel
	// active is a bitmask of channels with work, so the per-cycle check does
	// not scan all sixteen channels.
	active uint16
	intr   uint32 // INTR, write-1-to-clear
}

type dmaChannel struct {
	active bool
	read   uint32
	write  uint32
	count  uint32
	ctrl   uint32
}

const (
	dmaRegINTR = 0x400
	dmaIRQstep = 0x10 // INTRn/INTEn/INTFn/INTSn stride

	dmaCtrlEN       = 1 << 0
	dmaCtrlINCRRead = 1 << 4
	dmaCtrlINCRWrit = 1 << 6
	dmaCtrlIRQQuiet = 1 << 23
	dmaCtrlBusy     = 1 << 26
)

// dmaIntReg decodes the interrupt registers: kind 0 is INTRn, 1 INTE,
// 2 INTF and 3 INTS.
func dmaIntReg(off uint32) (irq, kind int, ok bool) {
	if off < dmaRegINTR || off > dmaRegINTR+3*dmaIRQstep+0xC {
		return 0, 0, false
	}
	rel := off - dmaRegINTR
	irq = int(rel / dmaIRQstep)
	kind = int((rel % dmaIRQstep) / 4)
	if kind > 3 {
		return 0, 0, false
	}
	return irq, kind, true
}

func newDMA(c *Chip) *dmaBlock {
	d := &dmaBlock{chip: c, block: newRegBlock(0x50000000, 0x1000)}
	d.block.writeFn = func(c *Chip, off, v uint32) {
		if off%0x40 == 0x0C { // CTRL_TRIG
			d.trigger(int(off/0x40), v)
			return
		}
		if _, kind, ok := dmaIntReg(off); ok && kind == 0 {
			d.intr &^= v
			d.syncIRQ()
		}
	}
	d.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if irq, kind, ok := dmaIntReg(off); ok {
			switch kind {
			case 0: // INTRn
				return d.intr, true
			case 1: // INTEn
				return d.block.regs[off/4], true
			default: // INTFn / INTSn
				return d.intr & d.block.regs[(dmaRegINTR+uint32(irq)*dmaIRQstep+4)/4], true
			}
		}
		if off%0x40 == 0x0C {
			// CTRL_TRIG reads back with BUSY while the channel is running.
			v := d.block.regs[off/4]
			if d.ch[off/0x40].active {
				v |= dmaCtrlBusy
			} else {
				v &^= dmaCtrlBusy
			}
			return v, true
		}
		return 0, false
	}
	return d
}

func (d *dmaBlock) trigger(ch int, v uint32) {
	if ch < 0 || ch >= len(d.ch) {
		return
	}
	base := uint32(ch * 0x40)
	ctrl := v &^ dmaCtrlBusy
	chp := &d.ch[ch]
	if v&dmaCtrlEN == 0 {
		chp.active = false
		d.active &^= 1 << uint(ch)
		d.block.regs[base/4+3] = ctrl
		return
	}
	d.block.regs[base/4+3] = ctrl | dmaCtrlBusy
	chp.active = true
	d.active |= 1 << uint(ch)
	chp.read = d.block.regs[base/4]
	chp.write = d.block.regs[base/4+1]
	chp.count = d.block.regs[base/4+2]
	chp.ctrl = ctrl
	if chp.count == 0 {
		d.complete(ch)
	}
}

// busy reports whether any channel still has work, so the per-cycle tick can
// stay out of the hot path when the controller is idle.
func (d *dmaBlock) busy() bool {
	return d.active != 0
}

// tick advances every active channel by one element.
func (d *dmaBlock) tick() {
	mask := d.active
	for mask != 0 {
		n := bits.TrailingZeros16(mask)
		mask &^= 1 << uint(n)
		ch := &d.ch[n]
		if !ch.active {
			continue
		}
		d.step(n, ch)
	}
}

func (d *dmaBlock) step(n int, ch *dmaChannel) {
	switch (ch.ctrl >> 2) & 3 {
	case 0: // byte
		d.chip.Write8(ch.write, d.chip.Read8(ch.read))
		if ch.ctrl&dmaCtrlINCRRead != 0 {
			ch.read++
		}
		if ch.ctrl&dmaCtrlINCRWrit != 0 {
			ch.write++
		}
	case 1: // halfword
		d.chip.Write16(ch.write, d.chip.Read16(ch.read))
		if ch.ctrl&dmaCtrlINCRRead != 0 {
			ch.read += 2
		}
		if ch.ctrl&dmaCtrlINCRWrit != 0 {
			ch.write += 2
		}
	default: // word
		d.chip.Write32(ch.write, d.chip.Read32(ch.read))
		if ch.ctrl&dmaCtrlINCRRead != 0 {
			ch.read += 4
		}
		if ch.ctrl&dmaCtrlINCRWrit != 0 {
			ch.write += 4
		}
	}
	ch.count--
	base := uint32(n * 0x40)
	d.block.regs[base/4] = ch.read
	d.block.regs[base/4+1] = ch.write
	d.block.regs[base/4+2] = ch.count
	if ch.count == 0 || d.chip.Halted {
		d.complete(n)
	}
}

func (d *dmaBlock) complete(n int) {
	base := uint32(n * 0x40)
	ch := &d.ch[n]
	ch.active = false
	d.active &^= 1 << uint(n)
	d.block.regs[base/4+2] = 0
	d.block.regs[base/4+3] = ch.ctrl &^ (dmaCtrlEN | dmaCtrlBusy)
	if d.chip.OnDMA != nil {
		d.chip.OnDMA(n, ch.read, ch.write, ch.count, ch.ctrl)
	}
	if ch.ctrl&dmaCtrlIRQQuiet == 0 {
		d.intr |= 1 << uint(n)
		d.syncIRQ()
	}
}

// syncIRQ maps INTR through the four INTE masks onto the DMA IRQs.
func (d *dmaBlock) syncIRQ() {
	for i := range 4 {
		inte := d.block.regs[(dmaRegINTR+uint32(i)*dmaIRQstep+4)/4]
		if d.intr&inte != 0 {
			d.chip.NVIC.Pend(10 + i)
		} else {
			d.chip.NVIC.ClearPending(10 + i)
		}
	}
}

// pending returns the channel bits visible in INTSn (for tests and diagnostics).
func (d *dmaBlock) pending(n int) uint32 {
	inte := d.block.regs[(dmaRegINTR+uint32(n)*dmaIRQstep+4)/4]
	return d.intr & inte
}
