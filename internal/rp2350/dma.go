package rp2350

// dmaBlock models the RP2350 DMA controller. Transfers complete immediately
// when triggered: the engine copies the bytes and clears BUSY, which is enough
// for polled drivers such as TinyGo's SPI implementation.
type dmaBlock struct {
	block *regBlock
	chip  *Chip
	// readByte/writeByte let the bus route DMA traffic through the same
	// peripheral hooks as CPU access.
}

func newDMA(c *Chip) *dmaBlock {
	d := &dmaBlock{chip: c, block: newRegBlock(0x50000000, 0x1000)}
	d.block.writeFn = func(c *Chip, off, v uint32) {
		// CTRL_TRIG lives at the end of each channel's 16-byte block.
		if off%0x40 == 0x0C && v&1 != 0 {
			ch := int(off / 0x40)
			d.transfer(ch)
		}
	}
	d.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off%0x40 == 0x0C {
			// Report "not busy" regardless of the requested bits.
			return d.block.regs[off/4] &^ 2, true
		}
		return 0, false
	}
	return d
}

func (d *dmaBlock) transfer(ch int) {
	base := uint32(ch * 0x40)
	readAddr := d.block.regs[base/4+0]
	writeAddr := d.block.regs[base/4+1]
	count := d.block.regs[base/4+2]
	ctrl := d.block.regs[base/4+3]

	if count > 1<<20 {
		count = 1 << 20
	}
	incrRead := ctrl&(1<<4) != 0
	incrWrite := ctrl&(1<<6) != 0
	size := (ctrl >> 2) & 3 // 0=byte, 1=half, 2=word

	if d.chip.OnDMA != nil {
		d.chip.OnDMA(ch, readAddr, writeAddr, count, ctrl)
	}
	ra, wa := readAddr, writeAddr
	for i := uint32(0); i < count; i++ {
		switch size {
		case 0:
			b := d.chip.Read8(ra)
			d.chip.Write8(wa, b)
			if incrRead {
				ra++
			}
			if incrWrite {
				wa++
			}
		case 1:
			b := d.chip.Read16(ra)
			d.chip.Write16(wa, b)
			if incrRead {
				ra += 2
			}
			if incrWrite {
				wa += 2
			}
		default:
			b := d.chip.Read32(ra)
			d.chip.Write32(wa, b)
			if incrRead {
				ra += 4
			}
			if incrWrite {
				wa += 4
			}
		}
		if d.chip.Halted {
			break
		}
	}
	d.block.regs[base/4+2] = 0 // TRANS_COUNT
	d.block.regs[base/4+3] = ctrl &^ 1
}
