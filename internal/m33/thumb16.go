package m33

// exec16 decodes and executes a 16-bit Thumb instruction. PC has already been
// advanced to the next instruction by the caller, except for writes to R15.
func (c *CPU) exec16(pc uint32, hw uint16) {
	switch hw >> 11 {
	case 0x00, 0x01, 0x02: // LSL/LSR/ASR (immediate)
		op := uint32(hw>>11) & 3
		imm := uint32(hw>>6) & 0x1F
		rm := uint32(hw>>3) & 7
		rd := uint32(hw) & 7
		v := c.R[rm]
		var res uint32
		switch op {
		case 0:
			res = shiftLSL(c, v, imm, imm != 0)
		case 1:
			if imm == 0 {
				imm = 32
			}
			res = shiftLSR(c, v, imm, true)
		case 2:
			if imm == 0 {
				imm = 32
			}
			res = shiftASR(c, v, imm, true)
		}
		c.R[rd] = res
		c.setNZ(res)

	case 0x03: // ADD/SUB (register or 3-bit immediate)
		isImm := hw&0x0400 != 0
		sub := hw&0x0200 != 0
		rn := uint32(hw>>3) & 7
		rd := uint32(hw) & 7
		var op2 uint32
		if isImm {
			op2 = uint32(hw>>6) & 7
		} else {
			op2 = c.R[(hw>>6)&7]
		}
		if sub {
			c.R[rd] = c.subFlags(c.R[rn], op2, 1)
		} else {
			c.R[rd] = c.addFlags(c.R[rn], op2, 0)
		}

	case 0x04, 0x05, 0x06, 0x07: // MOV/CMP/ADD/SUB (immediate 8)
		op := uint32(hw>>11) & 3
		rd := uint32(hw>>8) & 7
		imm := uint32(hw) & 0xFF
		switch op {
		case 0:
			c.R[rd] = imm
			c.setNZ(imm)
		case 1:
			c.cmpFlags(c.R[rd], imm)
		case 2:
			c.R[rd] = c.addFlags(c.R[rd], imm, 0)
		case 3:
			c.R[rd] = c.subFlags(c.R[rd], imm, 1)
		}

	case 0x08: // data processing (ALU) or special data processing
		if hw&0x0400 != 0 {
			op := uint32(hw>>8) & 3
			rm := uint32(hw>>3) & 0xF
			switch op {
			case 0: // ADD (hi registers, no flags)
				rd := uint32(hw&7) | uint32(hw>>4)&8
				c.setReg(rd, c.reg(rd)+c.reg(rm))
			case 1: // CMP (hi registers)
				rd := uint32(hw&7) | uint32(hw>>4)&8
				c.subFlags(c.reg(rd), c.reg(rm), 1)
			case 2: // MOV (hi registers)
				rd := uint32(hw&7) | uint32(hw>>4)&8
				c.setReg(rd, c.reg(rm))
			case 3: // BX / BLX
				target := c.reg(rm)
				if hw&0x0080 != 0 {
					c.R[14] = c.R[15] | 1
				}
				c.branchTo(target)
			}
			return
		}
		op := uint32(hw>>6) & 0xF
		rm := uint32(hw>>3) & 7
		rd := uint32(hw) & 7
		a, b := c.R[rd], c.R[rm]
		switch op {
		case 0x0: // AND
			c.R[rd] = a & b
			c.setNZ(c.R[rd])
		case 0x1: // EOR
			c.R[rd] = a ^ b
			c.setNZ(c.R[rd])
		case 0x2: // LSL (register)
			c.R[rd] = shiftLSL(c, a, b&0xFF, true)
			c.setNZ(c.R[rd])
		case 0x3: // LSR (register)
			c.R[rd] = shiftLSR(c, a, b&0xFF, true)
			c.setNZ(c.R[rd])
		case 0x4: // ASR (register)
			c.R[rd] = shiftASR(c, a, b&0xFF, true)
			c.setNZ(c.R[rd])
		case 0x5: // ADC
			c.R[rd] = c.addFlags(a, b, boolToU32(c.flag(flagC)))
		case 0x6: // SBC
			c.R[rd] = c.subFlags(a, b, boolToU32(c.flag(flagC)))
		case 0x7: // ROR
			c.R[rd] = shiftROR(c, a, b&0xFF, true)
			c.setNZ(c.R[rd])
		case 0x8: // TST
			c.setNZForced(a & b)
		case 0x9: // RSB (NEG)
			c.R[rd] = c.subFlags(0, b, 1)
		case 0xA: // CMP
			c.cmpFlags(a, b)
		case 0xB: // CMN
			c.cmnFlags(a, b)
		case 0xC: // ORR
			c.R[rd] = a | b
			c.setNZ(c.R[rd])
		case 0xD: // MUL
			c.R[rd] = a * b
			c.setNZ(c.R[rd])
		case 0xE: // BIC
			c.R[rd] = a &^ b
			c.setNZ(c.R[rd])
		case 0xF: // MVN
			c.R[rd] = ^b
			c.setNZ(c.R[rd])
		}

	case 0x09: // LDR literal
		rt := uint32(hw>>8) & 7
		imm := uint32(hw&0xFF) << 2
		addr := (c.pcRead() &^ 3) + imm
		c.R[rt] = c.Bus.Read32(addr)

	case 0x0A, 0x0B: // load/store register offset
		op := uint32(hw>>9) & 7
		rm := uint32(hw>>6) & 7
		rn := uint32(hw>>3) & 7
		rt := uint32(hw) & 7
		c.loadStoreReg(op, c.R[rn]+c.R[rm], rt)

	case 0x0C, 0x0D, 0x0E, 0x0F: // STR/LDR/STRB/LDRB immediate
		op := uint32(hw>>11) & 3
		imm := uint32(hw>>6) & 0x1F
		rn := uint32(hw>>3) & 7
		rt := uint32(hw) & 7
		addr := c.R[rn]
		switch op {
		case 0:
			c.Bus.Write32(addr+imm*4, c.R[rt])
		case 1:
			c.R[rt] = c.Bus.Read32(addr + imm*4)
		case 2:
			c.Bus.Write8(addr+imm, uint8(c.R[rt]))
		case 3:
			c.R[rt] = uint32(c.Bus.Read8(addr + imm))
		}

	case 0x10, 0x11: // STRH/LDRH immediate
		op := (hw >> 11) & 1
		imm := uint32(hw>>6) & 0x1F
		rn := uint32(hw>>3) & 7
		rt := uint32(hw) & 7
		addr := c.R[rn] + imm*2
		if op == 0 {
			c.Bus.Write16(addr, uint16(c.R[rt]))
		} else {
			c.R[rt] = uint32(c.Bus.Read16(addr))
		}

	case 0x12, 0x13: // STR/LDR SP-relative
		op := (hw >> 11) & 1
		rt := uint32(hw>>8) & 7
		addr := c.R[13] + uint32(hw&0xFF)*4
		if op == 0 {
			c.Bus.Write32(addr, c.R[rt])
		} else {
			c.R[rt] = c.Bus.Read32(addr)
		}

	case 0x14: // ADR
		rd := uint32(hw>>8) & 7
		c.R[rd] = (c.pcRead() &^ 3) + uint32(hw&0xFF)<<2

	case 0x15: // ADD Rd, SP, #imm8*4
		rd := uint32(hw>>8) & 7
		c.R[rd] = c.R[13] + uint32(hw&0xFF)<<2

	case 0x16, 0x17: // miscellaneous
		c.exec16Misc(hw)

	case 0x18: // STM
		rn := uint32(hw>>8) & 7
		list := uint32(hw & 0xFF)
		base := c.R[rn]
		for i := range uint32(8) {
			if list&(1<<i) != 0 {
				c.Bus.Write32(base, c.R[i])
				base += 4
			}
		}
		c.R[rn] = base

	case 0x19: // LDM
		rn := uint32(hw>>8) & 7
		list := uint32(hw & 0xFF)
		base := c.R[rn]
		for i := range uint32(8) {
			if list&(1<<i) != 0 {
				c.R[i] = c.Bus.Read32(base)
				base += 4
			}
		}
		if list&(1<<rn) == 0 {
			c.R[rn] = base
		}

	case 0x1A, 0x1B: // conditional branch / SVC / UDF
		cond := uint8(hw >> 8 & 0xF)
		if cond == 0xE {
			c.Halt("UDF instruction")
			return
		}
		if cond == 0xF {
			c.Halt("SVC instruction (unsupported)")
			return
		}
		if c.condTrue(cond) {
			c.branch(int32(int8(hw&0xFF))<<1 + 2)
		}

	case 0x1C: // unconditional branch
		off := int32(int16(hw<<5)) >> 5
		c.branch(off<<1 + 2)
	}
}

func (c *CPU) exec16Misc(hw uint16) {
	switch {
	case hw&0xFF00 == 0xB000: // ADD/SUB SP, immediate (bit7 is the sign)
		imm := uint32(hw&0x7F) << 2
		if hw&0x0080 != 0 {
			c.R[13] -= imm
		} else {
			c.R[13] += imm
		}
	case hw&0xFD00 == 0xB100: // CBZ
		rn := uint32(hw) & 7
		imm := uint32(hw>>9&1)<<6 | uint32(hw>>3&0x1F)<<1
		if c.R[rn] == 0 {
			c.branch(int32(imm) + 2)
		}
	case hw&0xFD00 == 0xB900: // CBNZ
		rn := uint32(hw) & 7
		imm := uint32(hw>>9&1)<<6 | uint32(hw>>3&0x1F)<<1
		if c.R[rn] != 0 {
			c.branch(int32(imm) + 2)
		}
	case hw&0xFF00 == 0xB200: // SXTH/SXTB/UXTH/UXTB
		op := uint32(hw>>6) & 3
		rm := uint32(hw>>3) & 7
		rd := uint32(hw) & 7
		v := c.R[rm]
		switch op {
		case 0:
			c.R[rd] = uint32(int32(int16(v)))
		case 1:
			c.R[rd] = uint32(int32(int8(v)))
		case 2:
			c.R[rd] = uint32(uint16(v))
		case 3:
			c.R[rd] = uint32(uint8(v))
		}
	case hw&0xFE00 == 0xB400: // PUSH
		list := uint32(hw & 0xFF)
		if hw&0x0100 != 0 {
			list |= 1 << 14
		}
		c.pushList(list)
	case hw&0xFFE0 == 0xB660: // CPSIE/CPSID
		if hw&0x0010 != 0 {
			c.PRIMASK |= 1
		} else {
			c.PRIMASK &^= 1
		}
	case hw&0xFF00 == 0xBA00: // REV/REV16/REVSH
		op := uint32(hw>>6) & 3
		rm := uint32(hw>>3) & 7
		rd := uint32(hw) & 7
		v := c.R[rm]
		switch op {
		case 0:
			c.R[rd] = rev32(v)
		case 1:
			c.R[rd] = rev16(v)
		case 3:
			c.R[rd] = uint32(int32(int16(rev16(v))))
		}
	case hw&0xFE00 == 0xBC00: // POP
		list := uint32(hw & 0xFF)
		if hw&0x0100 != 0 {
			list |= 1 << 15
		}
		c.popList(list)
	case hw&0xFF00 == 0xBE00:
		c.Halt("BKPT instruction")
	case hw&0xFF00 == 0xBF00: // IT / hints
		cond := uint8(hw>>4) & 0xF
		mask := uint8(hw & 0xF)
		if mask != 0 {
			if cond == 0xF {
				c.Halt("invalid IT instruction")
				return
			}
			c.itstate = cond<<4 | mask
			return
		}
		switch cond {
		case 0: // NOP
		case 1: // YIELD
		case 2: // WFE
			if c.EventRegister {
				c.EventRegister = false
			} else {
				c.Sleeping = true
				c.SleepOnEvent = true
			}
		case 3: // WFI
			c.Sleeping = true
			c.SleepOnEvent = false
		case 4: // SEV signals an event to every core in the system.
			c.EventRegister = true
			if w, ok := c.Bus.(interface{ SignalEvent() }); ok {
				w.SignalEvent()
			}
		}
	}
}

// loadStoreReg implements the 16-bit register-offset load/store group.
func (c *CPU) loadStoreReg(op, addr, rt uint32) {
	switch op {
	case 0:
		c.Bus.Write32(addr, c.R[rt])
	case 1:
		c.Bus.Write16(addr, uint16(c.R[rt]))
	case 2:
		c.Bus.Write8(addr, uint8(c.R[rt]))
	case 3:
		c.R[rt] = uint32(int32(int8(c.Bus.Read8(addr))))
	case 4:
		c.R[rt] = c.Bus.Read32(addr)
	case 5:
		c.R[rt] = uint32(c.Bus.Read16(addr))
	case 6:
		c.R[rt] = uint32(c.Bus.Read8(addr))
	case 7:
		c.R[rt] = uint32(int32(int16(c.Bus.Read16(addr))))
	}
}

// Stack helpers.

func (c *CPU) pushList(list uint32) {
	n := popcount(list)
	c.R[13] -= uint32(n) * 4
	addr := c.R[13]
	for i := range 16 {
		if list&(1<<i) != 0 {
			c.Bus.Write32(addr, c.R[i])
			addr += 4
		}
	}
}

func (c *CPU) popList(list uint32) {
	addr := c.R[13]
	var pcVal uint32
	havePC := false
	for i := range 16 {
		if list&(1<<i) != 0 {
			v := c.Bus.Read32(addr)
			addr += 4
			if i == 15 {
				pcVal = v
				havePC = true
			} else {
				c.R[i] = v
			}
		}
	}
	c.R[13] = addr
	if havePC {
		c.branchTo(pcVal)
	}
}

// Shift helpers that maintain the carry flag.

func shiftLSL(c *CPU, v, n uint32, setCarry bool) uint32 {
	setCarry = setCarry && !c.itNoFlags
	if n == 0 {
		return v
	}
	if setCarry {
		if n < 32 {
			c.setFlag(flagC, v&(1<<(32-n)) != 0)
		} else if n == 32 {
			c.setFlag(flagC, v&1 != 0)
		} else {
			c.setFlag(flagC, false)
		}
	}
	if n >= 32 {
		return 0
	}
	return v << n
}

func shiftLSR(c *CPU, v, n uint32, setCarry bool) uint32 {
	setCarry = setCarry && !c.itNoFlags
	if n == 0 {
		return v
	}
	if setCarry {
		if n < 32 {
			c.setFlag(flagC, v&(1<<(n-1)) != 0)
		} else if n == 32 {
			c.setFlag(flagC, v&0x80000000 != 0)
		} else {
			c.setFlag(flagC, false)
		}
	}
	if n >= 32 {
		return 0
	}
	return v >> n
}

func shiftASR(c *CPU, v, n uint32, setCarry bool) uint32 {
	setCarry = setCarry && !c.itNoFlags
	if n == 0 {
		return v
	}
	if setCarry {
		if n < 32 {
			c.setFlag(flagC, v&(1<<(n-1)) != 0)
		} else {
			c.setFlag(flagC, v&0x80000000 != 0)
		}
	}
	if n >= 32 {
		n = 31
	}
	return uint32(int32(v) >> n)
}

func shiftROR(c *CPU, v, n uint32, setCarry bool) uint32 {
	setCarry = setCarry && !c.itNoFlags
	if n == 0 {
		return v
	}
	r := n & 31
	if r == 0 {
		if setCarry {
			c.setFlag(flagC, v&0x80000000 != 0)
		}
		return v
	}
	if setCarry {
		c.setFlag(flagC, v&(1<<(r-1)) != 0)
	}
	return v>>r | v<<(32-r)
}

// Arithmetic with flags.

func (c *CPU) addFlags(x, y, carryIn uint32) uint32 {
	sum := uint64(x) + uint64(y) + uint64(carryIn)
	res := uint32(sum)
	if c.itNoFlags {
		return res
	}
	c.setFlag(flagC, sum > 0xFFFFFFFF)
	c.setFlag(flagV, ^(x^y)&(x^res)&0x80000000 != 0)
	c.setNZ(res)
	return res
}

func (c *CPU) subFlags(x, y, carryIn uint32) uint32 {
	sum := uint64(x) + uint64(^y) + uint64(carryIn)
	res := uint32(sum)
	if c.itNoFlags {
		return res
	}
	c.setFlag(flagC, sum > 0xFFFFFFFF)
	c.setFlag(flagV, (x^y)&(x^res)&0x80000000 != 0)
	c.setNZ(res)
	return res
}

// cmpFlags and cmnFlags update flags even inside an IT block.
func (c *CPU) cmpFlags(x, y uint32) {
	saved := c.itNoFlags
	c.itNoFlags = false
	c.subFlags(x, y, 1)
	c.itNoFlags = saved
}

func (c *CPU) cmnFlags(x, y uint32) {
	saved := c.itNoFlags
	c.itNoFlags = false
	c.addFlags(x, y, 0)
	c.itNoFlags = saved
}

func boolToU32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func popcount(v uint32) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

func rev32(v uint32) uint32 {
	return v>>24 | (v>>8)&0xFF00 | (v<<8)&0xFF0000 | v<<24
}

func rev16(v uint32) uint32 {
	return (v>>8)&0x00FF00FF | (v<<8)&0xFF00FF00
}
