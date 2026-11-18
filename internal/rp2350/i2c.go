package rp2350

// i2cBlock models the DesignWare APB I2C controllers (I2C0 at 0x40090000,
// I2C1 at 0x40098000) to the level TinyGo's polling driver needs: FIFOs,
// interrupt latches, and a device hook. With no device attached every
// transaction NACKs, matching an empty bus.
type i2cBlock struct {
	chip  *Chip
	block *regBlock
	id    int

	raw     uint32 // raw interrupt status
	enable  bool
	addr    uint8
	rx      []byte
	write   []byte // bytes written in the current transaction
	devices map[uint8]I2CDevice
}

// I2CDevice is an external I2C target. Write receives the bytes sent before a
// repeated start; Read returns one byte per read request.
type I2CDevice interface {
	Write(b byte)
	Read() byte
}

// I2C register offsets.
const (
	i2cCON        = 0x00
	i2cTAR        = 0x04
	i2cDATA       = 0x10
	i2cINTRSTAT   = 0x2C
	i2cINTRMASK   = 0x30
	i2cRAWINTR    = 0x34
	i2cRXTL       = 0x38
	i2cTXTL       = 0x3C
	i2cCLRSTOP    = 0x60
	i2cENABLE     = 0x6C
	i2cSTATUS     = 0x70
	i2cTXFLR      = 0x74
	i2cRXFLR      = 0x78
	i2cTXABRTSRC  = 0x80
	i2cENABLESTAT = 0x9C
	i2cCOMPPARAM  = 0xF4
	i2cCOMPVER    = 0xF8
	i2cCOMPTYPE   = 0xFC
)

// Raw interrupt bits.
const (
	i2cRXFull  = 1 << 2
	i2cTXEmpty = 1 << 4
	i2cTXAbrt  = 1 << 6
	i2cStopDet = 1 << 9
)

func newI2C(c *Chip, base uint32, id int) *i2cBlock {
	i := &i2cBlock{chip: c, block: newRegBlock(base, 0x1000), id: id}
	i.devices = map[uint8]I2CDevice{}
	i.raw = i2cTXEmpty
	i.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case i2cRAWINTR:
			return i.raw, true
		case i2cINTRSTAT:
			return i.raw & i.block.regs[i2cINTRMASK/4], true
		case i2cSTATUS:
			return i.status(), true
		case i2cTXFLR:
			return 0, true
		case i2cRXFLR:
			return uint32(len(i.rx)), true
		case i2cTXABRTSRC:
			// Read through the block so CLR_TX_ABRT can clear the reason.
			return i.block.regs[i2cTXABRTSRC/4], true
		case i2cENABLESTAT:
			if i.enable {
				return 1, true
			}
			return 0, true
		case i2cCOMPTYPE:
			return 0x44570140, true
		case i2cCOMPVER:
			return 0x3230312A, true
		case i2cCOMPPARAM:
			return 0, true
		case i2cDATA:
			var v uint32
			if len(i.rx) > 0 {
				v = uint32(i.rx[0])
				i.rx = i.rx[1:]
			} else {
				i.raw |= 1 << 0 // RX_UNDER
			}
			return v, true
		}
		if off >= 0x40 && off < 0x6C && off%4 == 0 {
			// CLR_* registers clear on read and read as zero.
			i.clearOnRead(off)
			return 0, true
		}
		return 0, false
	}
	i.block.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case i2cENABLE:
			i.enable = v&1 != 0
			if !i.enable {
				i.rx = i.rx[:0]
				i.write = i.write[:0]
			}
		case i2cTAR:
			i.addr = uint8(v & 0x7F)
			i.write = i.write[:0]
		case i2cDATA:
			i.dataCmd(v)
		case i2cINTRMASK:
			// stored by the register file
		}
	}
	return i
}

func (i *i2cBlock) status() uint32 {
	var s uint32
	if i.enable {
		s |= 1 // ACTIVITY
	}
	s |= 4 // TX FIFO empty
	if len(i.rx) > 0 {
		s |= 8 // RX FIFO not empty
	}
	return s
}

func (i *i2cBlock) clearOnRead(off uint32) {
	switch off {
	case 0x40: // CLR_INTR
		i.raw = 0
	case 0x44: // CLR_RX_UNDER
		i.raw &^= 1 << 0
	case 0x48: // CLR_RX_OVER
		i.raw &^= 1 << 1
	case 0x4C: // CLR_TX_OVER
		i.raw &^= 1 << 3
	case 0x54: // CLR_TX_ABRT
		i.raw &^= i2cTXAbrt
		i.block.regs[i2cTXABRTSRC/4] = 0
	case 0x58: // CLR_RX_DONE
		i.raw &^= 1 << 7
	case 0x5C: // CLR_ACTIVITY
		i.raw &^= 1 << 8
	case 0x60: // CLR_STOP_DET
		i.raw &^= i2cStopDet
	}
}

func (i *i2cBlock) dataCmd(v uint32) {
	read := v&(1<<8) != 0
	stop := v&(1<<9) != 0
	if read {
		i.rx = append(i.rx, i.readByte())
		if len(i.rx) >= 16 {
			i.raw |= 1 << 5 // RX_FULL
		}
	} else {
		b := byte(v)
		if dev, ok := i.devices[i.addr]; ok {
			dev.Write(b)
		} else {
			// NACK: no target.
			i.block.regs[i2cTXABRTSRC/4] = 1 << 3 // ABRT_7B_ADDR_NOACK
			i.raw |= i2cTXAbrt
		}
	}
	i.raw |= i2cTXEmpty
	if stop {
		i.raw |= i2cStopDet
	}
}

func (i *i2cBlock) readByte() byte {
	if dev, ok := i.devices[i.addr]; ok {
		return dev.Read()
	}
	// Empty bus: a read after a NACK still has to make progress.
	return 0xFF
}

// AttachI2C registers a device at a 7-bit address on one of the controllers.
func (c *Chip) AttachI2C(id int, addr uint8, dev I2CDevice) {
	if c.i2c[id] != nil {
		c.i2c[id].devices[addr] = dev
	}
}
