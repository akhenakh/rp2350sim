package rp2350

// i2cBlock models the DesignWare APB I2C controllers (I2C0 at 0x40090000,
// I2C1 at 0x40098000) to the level TinyGo's polling driver needs: FIFOs,
// interrupt latches, and a device hook. With no device attached every
// transaction NACKs, matching an empty bus.
type i2cBlock struct {
	chip  *Chip
	block *regBlock
	id    int

	raw       uint32 // raw interrupt status
	enable    bool
	con       uint32
	addr      uint8
	addrPhase bool // the next wire byte is the address, not data
	idle      bool // no master transaction is active
	tx        []uint32
	rx        []byte
	// txAbrtBlocked keeps both FIFOs flushed until CLR_TX_ABRT or CLR_INTR.
	txAbrtBlocked   bool
	transactionOpen bool
	devices         map[uint8]I2CDevice
}

// I2CDevice is an external I2C target. Write receives the bytes sent before a
// repeated start; Read returns one byte per read request.
type I2CDevice interface {
	Write(b byte)
	Read() byte
}

// I2CAddressable is implemented by targets that need the latched TAR (the TCP
// bridge, which may serve several addresses through one object).
type I2CAddressable interface {
	SetAddress(addr uint8)
}

// I2CWireDevice is implemented by targets that can NAK a byte or report that
// a read has no data (a remote bridge, for example). The controller falls back
// to the legacy I2CDevice interface when this is not implemented.
type I2CWireDevice interface {
	I2CDevice
	WriteAck(b byte) bool
	ReadAck() (byte, bool)
}

// I2CTransaction is implemented by targets that need transaction boundaries
// (a register pointer that survives a repeated start, or state cleared by the
// STOP). Begin runs when the address is latched; End runs on STOP.
type I2CTransaction interface {
	BeginTransaction()
	EndTransaction()
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

	i2cFIFODepth = 16
	i2cCONReset  = 0x65 // master mode, fast speed, RESTART enabled, slave disabled
)

// Raw interrupt bits.
const (
	i2cRXUnder  = 1 << 0
	i2cRXOver   = 1 << 1
	i2cTXOver   = 1 << 3
	i2cRXFull   = 1 << 2
	i2cTXEmpty  = 1 << 4
	i2cTXAbrt   = 1 << 6
	i2cStopDet  = 1 << 9
	i2cStartDet = 1 << 10
	i2cActivity = 1 << 8

	// IC_DATA_CMD.RESTART shares bit 10 with the START_DET status bit.
	i2cRestart     = 1 << 10
	i2cRestartDet  = 1 << 12
	i2cCmdBlock    = 1 << 2
	i2cEnableAbort = 1 << 1
	i2cRXFullHold  = 1 << 9
)

// TX_ABRT_SOURCE reasons.
const (
	i2cABRT7BAddrNoAck = 1 << 0
	i2cABRTTXDataNoAck = 1 << 3
	i2cABRTUserAbrt    = 1 << 16
)

func (i *i2cBlock) reset() {
	i.raw = 0
	i.enable = false
	i.con = i2cCONReset
	i.addr = 0x55
	i.addrPhase = false
	i.idle = true
	i.tx = i.tx[:0]
	i.rx = i.rx[:0]
	i.txAbrtBlocked = false
	i.transactionOpen = false
	if i.block != nil {
		i.block.regs[i2cCON/4] = i2cCONReset
		i.block.regs[i2cTAR/4] = 0x55
		i.block.regs[0x08/4] = 0x55
		i.block.regs[i2cINTRMASK/4] = 0x7ff
		i.block.regs[0x7c/4] = 1
		i.block.regs[0xa0/4] = 7
	}
}

func newI2C(c *Chip, base uint32, id int) *i2cBlock {
	i := &i2cBlock{chip: c, block: newRegBlock(base, 0x1000), id: id}
	i.block.resetFn = func(c *Chip) { i.reset() }
	i.devices = map[uint8]I2CDevice{}
	i.reset()
	i.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case i2cCON:
			return i.con, true
		case i2cRAWINTR:
			i.refreshRaw()
			return i.raw, true
		case i2cINTRSTAT:
			i.refreshRaw()
			return i.raw & i.block.regs[i2cINTRMASK/4], true
		case i2cSTATUS:
			return i.status(), true
		case i2cTXFLR:
			return uint32(len(i.tx)), true
		case i2cRXFLR:
			return uint32(len(i.rx)), true
		case i2cTXABRTSRC:
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
				i.raw |= i2cRXUnder
			}
			i.refreshRaw()
			i.drain()
			return v, true
		case 0xa8: // CLR_RESTART_DET
			i.raw &^= i2cRestartDet
			return 0, true
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
		case i2cCON:
			if !i.enable {
				i.con = v & 0x3ff
			}
			i.block.regs[i2cCON/4] = i.con
		case i2cENABLE:
			i.writeEnable(v)
		case i2cTAR:
			i.block.regs[i2cTAR/4] = v & 0xfff
			i.addr = uint8(v & 0x7f)
			i.startTransaction()
		case i2cDATA:
			i.dataCmd(v)
		case i2cRXTL, i2cTXTL:
			i.block.regs[off/4] = min(v&0xff, i2cFIFODepth)
			i.refreshRaw()
		}
	}
	return i
}

func (i *i2cBlock) writeEnable(v uint32) {
	i.enable = v&1 != 0
	// ABORT is self-clearing; TX_CMD_BLOCK remains under software control.
	i.block.regs[i2cENABLE/4] = v &^ i2cEnableAbort
	if !i.enable {
		i.tx = i.tx[:0]
		i.rx = i.rx[:0]
		i.idle = true
		i.transactionOpen = false
		i.raw &^= i2cActivity | i2cRXUnder | i2cRXOver | i2cTXOver
		i.refreshRaw()
		return
	}
	if v&i2cEnableAbort != 0 {
		i.abort(i2cABRTUserAbrt)
		return
	}
	i.refreshRaw()
	i.drain()
}

func (i *i2cBlock) status() uint32 {
	var s uint32
	if i.enable && !i.idle {
		s |= 1      // ACTIVITY
		s |= 1 << 5 // MST_ACTIVITY
	}
	if len(i.tx) < i2cFIFODepth {
		s |= 1 << 1 // TFNF
	}
	if len(i.tx) == 0 {
		s |= 1 << 2 // TFE
	}
	if len(i.rx) > 0 {
		s |= 1 << 3 // RFNE
	}
	if len(i.rx) == i2cFIFODepth {
		s |= 1 << 4 // RFF
	}
	return s
}

func (i *i2cBlock) rxThreshold() int {
	return int(i.block.regs[i2cRXTL/4]&0xff) + 1
}

func (i *i2cBlock) txThreshold() int {
	return int(i.block.regs[i2cTXTL/4] & 0xff)
}

func (i *i2cBlock) refreshRaw() {
	i.raw &^= i2cRXFull | i2cTXEmpty
	if !i.enable {
		return
	}
	if len(i.rx) >= i.rxThreshold() {
		i.raw |= i2cRXFull
	}
	if len(i.tx) <= i.txThreshold() {
		i.raw |= i2cTXEmpty
	}
}

func (i *i2cBlock) clearOnRead(off uint32) {
	switch off {
	case 0x40: // CLR_INTR
		i.raw = 0
		i.txAbrtBlocked = false
		i.block.regs[i2cTXABRTSRC/4] = 0
	case 0x44: // CLR_RX_UNDER
		i.raw &^= i2cRXUnder
	case 0x48: // CLR_RX_OVER
		i.raw &^= i2cRXOver
	case 0x4c: // CLR_TX_OVER
		i.raw &^= i2cTXOver
	case 0x50: // CLR_RD_REQ
		i.raw &^= 1 << 5
	case 0x54: // CLR_TX_ABRT
		i.raw &^= i2cTXAbrt
		i.txAbrtBlocked = false
		i.block.regs[i2cTXABRTSRC/4] = 0
	case 0x58: // CLR_RX_DONE
		i.raw &^= 1 << 7
	case 0x5c: // CLR_ACTIVITY
		if i.idle || !i.enable {
			i.raw &^= i2cActivity
		}
	case 0x60: // CLR_STOP_DET
		i.raw &^= i2cStopDet
	case 0x64: // CLR_START_DET
		i.raw &^= i2cStartDet
	case 0x68: // CLR_GEN_CALL
		i.raw &^= 1 << 11
	}
	i.refreshRaw()
}

// abortReason names the phase that NAKed: the address byte or a data/read
// byte. The bridge reports an address NAK as ABRT_7B_ADDR_NOACK.
func (i *i2cBlock) abortReason() uint32 {
	if i.addrPhase {
		return i2cABRT7BAddrNoAck
	}
	return i2cABRTTXDataNoAck
}

func (i *i2cBlock) startTransaction() {
	i.idle = false
	i.addrPhase = true
	i.transactionOpen = true
	i.raw |= i2cActivity | i2cStartDet
	if dev, ok := i.devices[i.addr]; ok {
		if a, ok := dev.(I2CAddressable); ok {
			a.SetAddress(i.addr)
		}
		if t, ok := dev.(I2CTransaction); ok {
			t.BeginTransaction()
		}
	}
	if i.chip.OnI2C != nil {
		i.chip.OnI2C(i.id, i.chip.Cycles, "addr", i.addr, 0, true)
	}
}

func (i *i2cBlock) endTransaction() {
	if !i.transactionOpen {
		return
	}
	i.transactionOpen = false
	if dev, ok := i.devices[i.addr]; ok {
		if t, ok := dev.(I2CTransaction); ok {
			t.EndTransaction()
		}
	}
}

func (i *i2cBlock) stop() {
	i.raw |= i2cStopDet
	i.endTransaction()
	i.idle = true
	i.addrPhase = false
	if i.chip.OnI2C != nil {
		i.chip.OnI2C(i.id, i.chip.Cycles, "stop", i.addr, 0, true)
	}
}

// abort is the single completion path for NACKs and software ABORT. It emits
// exactly one STOP, flushes both FIFOs and holds them flushed until cleared.
func (i *i2cBlock) abort(reason uint32) {
	if i.txAbrtBlocked {
		return
	}
	i.tx = i.tx[:0]
	i.rx = i.rx[:0]
	i.txAbrtBlocked = true
	i.block.regs[i2cTXABRTSRC/4] = reason
	i.raw |= i2cTXAbrt
	i.stop()
	i.refreshRaw()
}

func (i *i2cBlock) dataCmd(v uint32) {
	if !i.enable || i.txAbrtBlocked {
		return
	}
	if len(i.tx) >= i2cFIFODepth {
		i.raw |= i2cTXOver
		return
	}
	i.tx = append(i.tx, v&0x7ff)
	i.refreshRaw()
	i.drain()
}

func (i *i2cBlock) commandBlocked() bool {
	return i.block.regs[i2cENABLE/4]&i2cCmdBlock != 0
}

func (i *i2cBlock) rxHoldBlocked(cmd uint32) bool {
	return cmd&(1<<8) != 0 && len(i.rx) >= i2cFIFODepth &&
		i.con&i2cRXFullHold != 0
}

func (i *i2cBlock) drain() {
	for i.enable && !i.txAbrtBlocked && !i.commandBlocked() && len(i.tx) > 0 {
		cmd := i.tx[0]
		if i.rxHoldBlocked(cmd) {
			break
		}
		i.tx = i.tx[1:]
		i.execute(cmd)
		i.refreshRaw()
	}
	i.refreshRaw()
}

func (i *i2cBlock) receive(b byte) {
	if len(i.rx) >= i2cFIFODepth {
		i.raw |= i2cRXOver
		return
	}
	i.rx = append(i.rx, b)
}

func (i *i2cBlock) execute(cmd uint32) {
	read := cmd&(1<<8) != 0
	stop := cmd&(1<<9) != 0
	if i.idle {
		// The first command after STOP starts a fresh transaction at TAR.
		i.startTransaction()
	} else if cmd&i2cRestart != 0 {
		i.startTransaction()
	}

	if read {
		dev, found := i.devices[i.addr]
		if !found {
			if i.chip.OnI2C != nil {
				i.chip.OnI2C(i.id, i.chip.Cycles, "addr", i.addr, 0, false)
			}
			i.abort(i2cABRT7BAddrNoAck)
			return
		}
		var b byte
		var ack bool
		if wd, ok := dev.(I2CWireDevice); ok {
			b, ack = wd.ReadAck()
		} else {
			b, ack = dev.Read(), true
		}
		if i.chip.OnI2C != nil {
			i.chip.OnI2C(i.id, i.chip.Cycles, "read", i.addr, b, ack)
		}
		if !ack {
			i.abort(i.abortReason())
			return
		}
		i.receive(b)
	} else {
		b := byte(cmd)
		ok := true
		if dev, found := i.devices[i.addr]; found {
			if wd, wire := dev.(I2CWireDevice); wire {
				ok = wd.WriteAck(b)
			} else {
				dev.Write(b)
			}
		} else {
			ok = false
		}
		if i.chip.OnI2C != nil {
			i.chip.OnI2C(i.id, i.chip.Cycles, "data", i.addr, b, ok)
		}
		if !ok {
			i.abort(i.abortReason())
			return
		}
	}
	i.addrPhase = false
	if stop {
		i.stop()
	}
}

// AttachI2C registers a device at a 7-bit address on one of the controllers.
func (c *Chip) AttachI2C(id int, addr uint8, dev I2CDevice) {
	if c.i2c[id] != nil {
		c.i2c[id].devices[addr] = dev
	}
}

// HasI2CDevice reports whether an address already has a target.
func (c *Chip) HasI2CDevice(id int, addr uint8) bool {
	if id < 0 || id >= len(c.i2c) || c.i2c[id] == nil {
		return false
	}
	_, ok := c.i2c[id].devices[addr]
	return ok
}
