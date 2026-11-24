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
	// active is the BUSY mask, including paused channels. enabled is kept
	// separately so clearing EN suspends a sequence without losing it.
	active  uint16
	enabled uint16
	next    uint8
	intr    uint32 // INTR, write-1-to-clear
	intf    uint32 // INTF, read/write force
	// chainPending latches a chain trigger whose target had a zero reload
	// count; software reloading TRANS_COUNT starts it.
	chainPending uint16
	abort        uint16
}

type dmaChannel struct {
	active    bool
	read      uint32
	write     uint32
	count     uint32
	reload    uint32
	ctrl      uint32
	readBase  uint32
	writeBase uint32
}

const (
	dmaRegINTR = 0x400
	dmaIRQstep = 0x10 // INTRn/INTEn/INTFn/INTSn stride

	dmaCtrlEN       = 1 << 0
	dmaCtrlINCRRead = 1 << 4
	dmaCtrlINCRWrit = 1 << 6
	dmaCtrlIRQQuiet = 1 << 23
	dmaCtrlBusy     = 1 << 26
	dmaCtrlBSWAP    = 1 << 24
	dmaCountMask    = 0x0FFFFFFF
	dmaRingSel      = 1 << 12
	dmaReadRev      = 1 << 5
	dmaWriteRev     = 1 << 7
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

// dmaAlias maps the AL1/AL2/AL3 per-channel aliases to the main register
// offset (0x00 READ_ADDR, 0x04 WRITE_ADDR, 0x08 TRANS_COUNT, 0x0C CTRL) and
// reports whether the write triggers the channel. The three alias groups use
// different register orders. RP2350 datasheet Table 1253 and
// hardware/regs/dma.h put AL2 at CTRL/COUNT/READ/WRITE_TRIG and AL3 at
// CTRL/WRITE/COUNT/READ_TRIG; this is not the RP2040 alias map.
func dmaAlias(off uint32) (ch int, reg uint32, trig bool, ok bool) {
	if off >= 0x400 {
		return 0, 0, false, false // channel window only
	}
	ch = int(off / 0x40)
	switch off % 0x40 {
	case 0x10: // AL1_CTRL
		return ch, 0x0C, false, true
	case 0x14: // AL1_READ_ADDR
		return ch, 0x00, false, true
	case 0x18: // AL1_WRITE_ADDR
		return ch, 0x04, false, true
	case 0x1C: // AL1_TRANS_COUNT_TRIG
		return ch, 0x08, true, true
	case 0x20: // AL2_CTRL
		return ch, 0x0C, false, true
	case 0x24: // AL2_TRANS_COUNT
		return ch, 0x08, false, true
	case 0x28: // AL2_READ_ADDR
		return ch, 0x00, false, true
	case 0x2C: // AL2_WRITE_ADDR_TRIG
		return ch, 0x04, true, true
	case 0x30: // AL3_CTRL
		return ch, 0x0C, false, true
	case 0x34: // AL3_WRITE_ADDR
		return ch, 0x04, false, true
	case 0x38: // AL3_TRANS_COUNT
		return ch, 0x08, false, true
	case 0x3C: // AL3_READ_ADDR_TRIG
		return ch, 0x00, true, true
	}
	return 0, 0, false, false
}

func newDMA(c *Chip) *dmaBlock {
	d := &dmaBlock{chip: c, block: newRegBlock(0x50000000, 0x1000)}
	d.block.resetFn = func(c *Chip) { d.reset() }
	d.block.clearFn = func(c *Chip, off, v uint32) {
		if irq, kind, ok := dmaIntReg(off); ok {
			switch kind {
			case 0: // INTR is write-1-to-clear
				d.intr &^= v & 0xFFFF
				d.syncIRQ()
				return
			case 3: // INTS acknowledges the enabled channels
				inte := d.block.regs[(dmaRegINTR+uint32(irq)*dmaIRQstep+4)/4]
				d.intr &^= v & inte & 0xFFFF
				d.syncIRQ()
				return
			}
		}
		d.block.write(c, off, d.block.read(c, off)&^v)
	}
	d.block.writeFn = func(c *Chip, off, v uint32) {
		if ch, reg, trig, ok := dmaAlias(off); ok {
			switch reg {
			case 0x00:
				d.block.regs[uint32(ch*0x40)/4] = v
			case 0x04:
				d.block.regs[uint32(ch*0x40)/4+1] = v
			case 0x08:
				d.setCount(ch, v)
			case 0x0C:
				d.setCtrl(ch, v)
			}
			if trig {
				d.triggerRegister(ch, v)
			}
			return
		}
		if off < 0x400 {
			switch off % 0x40 {
			case 0x08:
				d.setCount(int(off/0x40), v)
			case 0x0C:
				ch := int(off / 0x40)
				d.setCtrl(ch, v)
				d.triggerRegister(ch, v)
			}
			return
		}
		switch off {
		case 0x450: // MULTI_CHAN_TRIGGER is a self-clearing strobe.
			for ch := range 16 {
				if v&(1<<uint(ch)) != 0 {
					d.start(ch)
				}
			}
			d.block.regs[off/4] = 0
			return
		case 0x454, 0x458: // CRC sniffer is deliberately not modelled.
			c.unhandledWrite(0x50000000+off, v)
			d.block.regs[off/4] = 0
			return
		case 0x460: // FIFO_LEVELS is read-only; this engine has no internal FIFOs.
			d.block.regs[off/4] = 0
			return
		case 0x464: // CHAN_ABORT
			d.abort |= uint16(v & 0xFFFF)
			for ch := range 16 {
				if d.abort&(1<<uint(ch)) != 0 {
					d.abortChannel(ch)
				}
			}
			d.block.regs[off/4] = uint32(d.abort)
			return
		case 0x468: // N_CHANNELS is read-only.
			d.block.regs[off/4] = 0
			return
		}
		if off >= 0x800 && off < 0xC00 {
			reg := (off - 0x800) % 0x40
			if reg == 0 { // DBG_CTDREQ: any write clears the DREQ counter.
				d.block.regs[off/4] = 0
			} else if reg == 4 { // DBG_TCR is read-only.
				ch := int((off - 0x800) / 0x40)
				d.block.regs[off/4] = d.ch[ch].reload
			}
			return
		}
		if irq, kind, ok := dmaIntReg(off); ok {
			switch kind {
			case 0: // INTR write-1-to-clear
				d.intr &^= v & 0xFFFF
				d.block.regs[off/4] = d.intr
				d.syncIRQ()
			case 1: // INTE changes the masked IRQ lines
				d.syncIRQ()
			case 2: // INTF read/write force
				d.intf = v & 0xFFFF
				d.block.regs[off/4] = d.intf
				d.syncIRQ()
			case 3: // INTS write-1-to-clear the enabled raw channels
				inte := d.block.regs[(dmaRegINTR+uint32(irq)*dmaIRQstep+4)/4]
				d.intr &^= v & inte & 0xFFFF
				d.block.regs[off/4] = d.intr & inte
				d.syncIRQ()
			}
		}
	}
	d.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if irq, kind, ok := dmaIntReg(off); ok {
			switch kind {
			case 0: // INTRn
				return d.intr, true
			case 1: // INTEn
				return d.block.regs[off/4], true
			case 2: // INTFn
				return d.intf, true
			default: // INTSn
				inte := d.block.regs[(dmaRegINTR+uint32(irq)*dmaIRQstep+4)/4]
				return (d.intr | d.intf) & inte, true
			}
		}
		if ch, reg, _, ok := dmaAlias(off); ok {
			if reg == 0x08 {
				if d.ch[ch].active {
					return d.ch[ch].count, true
				}
				return 0, true
			}
			v := d.block.regs[(uint32(ch*0x40)+reg)/4]
			if reg == 0x0C {
				if d.ch[ch].active {
					v |= dmaCtrlBusy
				} else {
					v &^= dmaCtrlBusy
				}
			}
			return v, true
		}
		if off < 0x400 {
			ch := int(off / 0x40)
			switch off % 0x40 {
			case 0x08:
				if d.ch[ch].active {
					return d.ch[ch].count, true
				}
				return 0, true
			case 0x0C:
				v := d.block.regs[off/4]
				if d.ch[ch].active {
					v |= dmaCtrlBusy
				} else {
					v &^= dmaCtrlBusy
				}
				return v, true
			}
		}
		switch off {
		case 0x450:
			return 0, true
		case 0x454, 0x458:
			c.unhandledRead32(0x50000000 + off)
			return 0, true
		case 0x460:
			return 0, true
		case 0x464:
			return uint32(d.abort), true
		case 0x468:
			return 16, true
		}
		if off >= 0x800 && off < 0xC00 {
			ch := int((off - 0x800) / 0x40)
			switch (off - 0x800) % 0x40 {
			case 0:
				return 0, true // DBG_CTDREQ, no DREQ pacing in this model
			case 4:
				return d.ch[ch].reload, true
			}
		}
		return 0, false
	}
	return d
}

func (d *dmaBlock) reset() {
	d.ch = [16]dmaChannel{}
	d.active = 0
	d.enabled = 0
	d.next = 0
	d.intr = 0
	d.intf = 0
	d.chainPending = 0
	d.abort = 0
	d.syncIRQ()
}

func (d *dmaBlock) setCount(ch int, v uint32) {
	if ch < 0 || ch >= len(d.ch) {
		return
	}
	d.ch[ch].reload = v
	if d.ch[ch].active {
		d.block.regs[uint32(ch*0x40+0x08)/4] = d.ch[ch].count
	} else {
		d.block.regs[uint32(ch*0x40+0x08)/4] = 0
	}
	d.checkChain(ch)
}

func (d *dmaBlock) setCtrl(ch int, v uint32) {
	if ch < 0 || ch >= len(d.ch) {
		return
	}
	chp := &d.ch[ch]
	chp.ctrl = v &^ dmaCtrlBusy
	if chp.active {
		chp.ctrl |= dmaCtrlBusy
	}
	if chp.ctrl&dmaCtrlEN != 0 {
		d.enabled |= 1 << uint(ch)
	} else {
		d.enabled &^= 1 << uint(ch)
		d.chainPending &^= 1 << uint(ch)
	}
	d.block.regs[uint32(ch*0x40+0x0C)/4] = chp.ctrl
}

// start begins a new sequence from the programmed reload count. Triggers are
// ignored while BUSY or while EN is clear.
func (d *dmaBlock) start(ch int) {
	if ch < 0 || ch >= len(d.ch) || d.enabled&(1<<uint(ch)) == 0 || d.ch[ch].active || d.abort&(1<<uint(ch)) != 0 {
		return
	}
	d.chainPending &^= 1 << uint(ch)
	chp := &d.ch[ch]
	base := uint32(ch * 0x40)
	chp.read = d.block.regs[base/4]
	chp.write = d.block.regs[base/4+1]
	chp.readBase = chp.read
	chp.writeBase = chp.write
	chp.count = chp.reload & dmaCountMask
	chp.active = true
	chp.ctrl |= dmaCtrlEN | dmaCtrlBusy
	d.active |= 1 << uint(ch)
	d.block.regs[base/4+2] = chp.count
	d.block.regs[base/4+3] = chp.ctrl
	if chp.count == 0 {
		d.complete(ch)
	}
}

func (d *dmaBlock) triggerRegister(ch int, v uint32) {
	if ch < 0 || ch >= len(d.ch) {
		return
	}
	if v == 0 {
		if !d.ch[ch].active && d.ch[ch].ctrl&dmaCtrlIRQQuiet != 0 {
			d.intr |= 1 << uint(ch)
			d.syncIRQ()
		}
		return
	}
	d.start(ch)
}

// busy reports whether any channel still has work or an abort is draining.
func (d *dmaBlock) busy() bool {
	return d.active != 0 || d.abort != 0
}

// tick services one enabled channel in round-robin order.
func (d *dmaBlock) tick() {
	if d.abort != 0 {
		d.abort = 0
		d.block.regs[0x464/4] = 0
	}
	mask := d.active & d.enabled
	if mask == 0 {
		return
	}
	start := int(d.next)
	for i := range 16 {
		ch := (start + i) & 15
		if mask&(1<<uint(ch)) == 0 {
			continue
		}
		d.next = uint8((ch + 1) & 15)
		d.step(ch, &d.ch[ch])
		return
	}
}

func (d *dmaBlock) step(n int, ch *dmaChannel) {
	size := int((ch.ctrl >> 2) & 3)
	var value uint32
	switch size {
	case 0:
		value = uint32(d.chip.Read8(ch.read))
	case 1:
		value = uint32(d.chip.Read16(ch.read))
	default:
		value = d.chip.Read32(ch.read)
	}
	if ch.ctrl&dmaCtrlBSWAP != 0 {
		switch size {
		case 0:
		case 1:
			value = uint32(bits.ReverseBytes16(uint16(value)))
		default:
			value = bits.ReverseBytes32(value)
		}
	}
	switch size {
	case 0:
		d.chip.writeDMA8(ch.write, byte(value))
	case 1:
		d.chip.writeDMA16(ch.write, uint16(value))
	default:
		d.chip.Write32(ch.write, value)
	}
	ch.read = dmaAddress(ch.read, ch.readBase, ch.ctrl, true, size)
	ch.write = dmaAddress(ch.write, ch.writeBase, ch.ctrl, false, size)
	ch.count--
	base := uint32(n * 0x40)
	d.block.regs[base/4] = ch.read
	d.block.regs[base/4+1] = ch.write
	d.block.regs[base/4+2] = ch.count
	if ch.count == 0 {
		d.complete(n)
	}
}

func dmaAddress(addr, base, ctrl uint32, read bool, size int) uint32 {
	step := uint32(1) << uint(size)
	incr, rev := ctrl&dmaCtrlINCRRead, ctrl&dmaReadRev
	if !read {
		incr, rev = ctrl&dmaCtrlINCRWrit, ctrl&dmaWriteRev
	}
	if incr != 0 {
		if rev != 0 {
			addr -= step
		} else {
			addr += step
		}
	} else if rev != 0 {
		addr += 2 * step
	}
	ringSize := (ctrl >> 8) & 0xF
	selected := ctrl&dmaRingSel == 0
	if !read {
		selected = ctrl&dmaRingSel != 0
	}
	if ringSize != 0 && selected {
		mask := uint32(1)<<ringSize - 1
		addr = base&^mask | addr&mask
	}
	return addr
}

func (d *dmaBlock) complete(n int) {
	base := uint32(n * 0x40)
	ch := &d.ch[n]
	ch.active = false
	ch.count = 0
	d.active &^= 1 << uint(n)
	d.block.regs[base/4+2] = 0
	// Completion clears BUSY only. EN stays set so the channel can be
	// re-triggered without rewriting CTRL.
	ch.ctrl &^= dmaCtrlBusy
	d.block.regs[base/4+3] = ch.ctrl
	if d.chip.OnDMA != nil {
		d.chip.OnDMA(n, ch.read, ch.write, ch.count, ch.ctrl)
	}
	if ch.ctrl&dmaCtrlIRQQuiet == 0 {
		d.intr |= 1 << uint(n)
		d.syncIRQ()
	}
	target := int((ch.ctrl >> 13) & 0xF)
	if target == n || target >= 16 || d.enabled&(1<<uint(target)) == 0 || d.ch[target].active {
		return
	}
	if d.ch[target].reload&dmaCountMask == 0 {
		d.chainPending |= 1 << uint(target)
		return
	}
	d.start(target)
}

func (d *dmaBlock) abortChannel(ch int) {
	if ch < 0 || ch >= len(d.ch) || !d.ch[ch].active {
		return
	}
	chp := &d.ch[ch]
	chp.active = false
	chp.count = 0
	chp.ctrl &^= dmaCtrlBusy
	d.active &^= 1 << uint(ch)
	d.chainPending &^= 1 << uint(ch)
	base := uint32(ch * 0x40)
	d.block.regs[base/4+2] = 0
	d.block.regs[base/4+3] = chp.ctrl
}

// checkChain starts a channel whose chain trigger arrived before software
// reloaded its transfer count.
func (d *dmaBlock) checkChain(ch int) {
	if ch < 0 || ch >= 16 || d.chainPending&(1<<uint(ch)) == 0 {
		return
	}
	if d.ch[ch].reload&dmaCountMask == 0 {
		return
	}
	d.chainPending &^= 1 << uint(ch)
	d.start(ch)
}

// syncIRQ maps raw and forced channel interrupts through the four INTE masks.
func (d *dmaBlock) syncIRQ() {
	for i := range 4 {
		inte := d.block.regs[(dmaRegINTR+uint32(i)*dmaIRQstep+4)/4]
		if (d.intr|d.intf)&inte != 0 {
			d.chip.PendIRQ(10 + i)
		} else {
			d.chip.ClearPendingIRQ(10 + i)
		}
	}
}

// pending returns the channel bits visible in INTSn (for tests and diagnostics).
func (d *dmaBlock) pending(n int) uint32 {
	inte := d.block.regs[(dmaRegINTR+uint32(n)*dmaIRQstep+4)/4]
	return (d.intr | d.intf) & inte
}

// writeDMA8 and writeDMA16 preserve normal memory semantics while reproducing
// RP2350's replicated narrow write bus for peripheral registers. Explicitly
// byte-addressed blocks such as USB DPRAM and SHA WDATA keep true narrow writes.
func (c *Chip) dmaNarrowReplicated(addr uint32) bool {
	if addr < PeriphBase || addr&0x4000 != 0 {
		return false
	}
	if b := c.regBlockFor(addr); b != nil {
		if b.writeSizeFn != nil || c.usb != nil && b == c.usb.dpram {
			return false
		}
	}
	return true
}

func (c *Chip) writeDMA8(addr uint32, v uint8) {
	if c.dmaNarrowReplicated(addr) {
		if c.OnWrite != nil {
			c.OnWrite(addr, uint32(v), 1)
		}
		c.debugWrite(addr, 1)
		c.invalidateMonitors(addr)
		c.writeMMIO(addr&^3, uint32(v)*0x01010101)
		return
	}
	c.Write8(addr, v)
}

func (c *Chip) writeDMA16(addr uint32, v uint16) {
	if c.dmaNarrowReplicated(addr) {
		if c.OnWrite != nil {
			c.OnWrite(addr, uint32(v), 2)
		}
		c.debugWrite(addr, 2)
		c.invalidateMonitors(addr)
		c.writeMMIO(addr&^3, uint32(v)*0x00010001)
		return
	}
	c.Write16(addr, v)
}
