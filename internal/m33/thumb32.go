package m33

// exec32 decodes and executes a 32-bit Thumb-2 instruction.
func (c *CPU) exec32(pc uint32, hw1 uint16) {
	hw2 := c.Bus.Read16(pc + 2)
	c.R[15] = pc + 4

	switch {
	// ---- Load/store multiple, dual, exclusive, table branch ----
	case hw1&0xFE00 == 0xE800:
		c.exec32E8(hw1, hw2)

	// ---- Data processing (shifted register) ----
	case hw1&0xFE00 == 0xEA00:
		c.exec32DataProc(hw1, hw2, false)

	// ---- MOVW / MOVT / ADDW / SUBW (must precede the generic F0 group) ----
	case hw1&0xFBF0 == 0xF240, hw1&0xFBF0 == 0xF2C0,
		hw1&0xFBF0 == 0xF200, hw1&0xFBF0 == 0xF2A0:
		c.exec32ImmData(hw1, hw2)

	// ---- MSR/MRS/barriers/bitfield ----
	case hw1&0xFF00 == 0xF300:
		c.exec32System(hw1, hw2)

	// ---- Modified immediate / branches ----
	case hw1&0xFA00 == 0xF000 && hw2&0x8000 == 0:
		c.exec32ImmData(hw1, hw2)
	case hw1&0xF800 == 0xF000 && hw2&0x8000 != 0:
		c.exec32Branch(hw1, hw2, pc)

	// ---- Load/store single ----
	case hw1&0xFE00 == 0xF800:
		c.exec32LoadStore(hw1, hw2)

	// ---- Unary data processing (CLZ, REV, RBIT, extend) ----
	case hw1&0xFF00 == 0xFA00:
		c.exec32Unary(hw1, hw2)

	// ---- Multiply / divide / long multiply ----
	case hw1&0xFF00 == 0xFB00:
		c.exec32MulDiv(hw1, hw2)

	default:
		if c.Unimplemented != nil && c.Unimplemented(pc, hw1, hw2) {
			return
		}
		c.Halt("unimplemented 32-bit instruction")
	}
}

// ---- E8 group ----

func (c *CPU) exec32E8(hw1, hw2 uint16) {
	switch {
	case hw1&0xFFF0 == 0xE8D0 && hw2&0xFFE0 == 0xF000: // TBB/TBH
		rn := uint32(hw1 & 0xF)
		rm := uint32(hw2 & 0xF)
		base := c.R[rn]
		if hw1&0x0010 != 0 { // TBH
			off := uint32(c.Bus.Read16(base + c.R[rm]*2))
			c.R[15] = c.pcRead() + off*2
		} else {
			off := uint32(c.Bus.Read8(base + c.R[rm]))
			c.R[15] = c.pcRead() + off*2
		}

	case hw1&0xFFD0 == 0xE880: // STM
		rn := uint32(hw1 & 0xF)
		if rn == 15 || hw2&0x8000 != 0 {
			c.Halt("invalid STM")
			return
		}
		list := uint32(hw2 & 0xFFFF)
		wback := hw1&0x0020 != 0
		base := c.R[rn]
		for i := 0; i < 16; i++ {
			if list&(1<<i) != 0 {
				c.Bus.Write32(base, c.R[i])
				base += 4
			}
		}
		if wback {
			c.R[rn] = base
		}

	case hw1&0xFFD0 == 0xE900: // STMDB (push.w)
		rn := uint32(hw1 & 0xF)
		if rn == 15 || hw2&0x8000 != 0 {
			c.Halt("invalid STMDB")
			return
		}
		list := uint32(hw2 & 0xFFFF)
		wback := hw1&0x0020 != 0
		n := popcount(list)
		base := c.R[rn] - uint32(n)*4
		addr := base
		for i := 0; i < 16; i++ {
			if list&(1<<i) != 0 {
				c.Bus.Write32(addr, c.R[i])
				addr += 4
			}
		}
		if wback {
			c.R[rn] = base
		}

	case hw1&0xFFD0 == 0xE890: // LDM
		rn := uint32(hw1 & 0xF)
		list := uint32(hw2)
		wback := hw1&0x0020 != 0
		if rn == 15 {
			c.Halt("invalid LDM")
			return
		}
		base := c.R[rn]
		var pcVal uint32
		havePC := false
		for i := 0; i < 16; i++ {
			if list&(1<<i) != 0 {
				v := c.Bus.Read32(base)
				base += 4
				if i == 15 {
					pcVal = v
					havePC = true
					continue
				}
				c.R[i] = v
			}
		}
		if wback && list&(1<<rn) == 0 {
			c.R[rn] = base
		}
		if havePC {
			c.branchTo(pcVal)
		}

	case hw1&0xFF00 == 0xE900 && hw1&0x00C0 != 0: // STRD / LDRD (immediate, imm8*4)
		rn := uint32(hw1 & 0xF)
		u := hw1&0x0080 != 0
		p := hw1&0x0040 != 0
		w := hw1&0x0020 != 0
		load := hw1&0x0010 != 0
		rt := uint32(hw2 >> 12)
		rt2 := uint32(hw2 >> 8 & 0xF)
		imm := int32(hw2&0xFF) * 4
		if !u {
			imm = -imm
		}
		base := c.R[rn]
		addr := base
		if p {
			addr = uint32(int32(base) + imm)
		}
		if load {
			c.R[rt] = c.Bus.Read32(addr)
			c.R[rt2] = c.Bus.Read32(addr + 4)
		} else {
			c.Bus.Write32(addr, c.R[rt])
			c.Bus.Write32(addr+4, c.R[rt2])
		}
		if w || !p {
			c.R[rn] = uint32(int32(base) + imm)
		}

	case hw1&0xFFF0 == 0xE840: // STREX
		rn := uint32(hw1 & 0xF)
		rd := uint32(hw2 >> 8 & 0xF)
		rt := uint32(hw2 >> 12)
		imm := uint32(hw2&0xFF) << 2
		addr := c.R[rn] + imm
		if c.monitor == addr && c.monitorValid {
			c.Bus.Write32(addr, c.R[rt])
			c.R[rd] = 0
		} else {
			c.R[rd] = 1
		}
		c.monitorValid = false

	case hw1&0xFFF0 == 0xE850: // LDREX
		rn := uint32(hw1 & 0xF)
		rt := uint32(hw2 >> 12)
		imm := uint32(hw2&0xFF) << 2
		addr := c.R[rn] + imm
		c.R[rt] = c.Bus.Read32(addr)
		c.monitor = addr
		c.monitorValid = true

	case hw1&0xFFF0 == 0xE8C0: // STLEX / STL
		rn := uint32(hw1 & 0xF)
		rt := uint32(hw2 >> 12)
		addr := c.R[rn]
		if hw2&0xF0 == 0xE0 { // STLEX Rd, Rt, [Rn]
			rd := uint32(hw2 & 0xF)
			if c.monitor == addr && c.monitorValid {
				c.Bus.Write32(addr, c.R[rt])
				c.R[rd] = 0
			} else {
				c.R[rd] = 1
			}
			c.monitorValid = false
		} else { // STL
			c.Bus.Write32(addr, c.R[rt])
		}

	case hw1&0xFFF0 == 0xE8D0 && (hw2&0xFF == 0xAF || hw2&0xFF == 0xEF): // LDA / LDAEX
		rn := uint32(hw1 & 0xF)
		rt := uint32(hw2 >> 12)
		addr := c.R[rn]
		c.R[rt] = c.Bus.Read32(addr)
		if hw2&0xFF == 0xEF {
			c.monitor = addr
			c.monitorValid = true
		}

	default:
		if c.Unimplemented != nil && c.Unimplemented(c.R[15]-4, hw1, hw2) {
			return
		}
		c.Halt("unimplemented E8-group instruction")
	}
}

// ---- EA/EB group: data processing, shifted register ----

func (c *CPU) exec32DataProc(hw1, hw2 uint16, _ bool) {
	op := uint32(hw1>>5) & 0xF
	s := hw1&0x0010 != 0
	rn := uint32(hw1 & 0xF)
	rd := uint32(hw2 >> 8 & 0xF)
	rm := uint32(hw2 & 0xF)
	imm3 := uint32(hw2 >> 12 & 0x7)
	imm2 := uint32(hw2 >> 6 & 0x3)
	typ := uint32(hw2 >> 4 & 0x3)
	shift := imm3<<2 | imm2

	// MOV (register-shifted): Rn == 15 and op == ORR.
	if rn == 15 && op == 0x2 && typ == 0 && shift == 0 && !s {
		c.setReg(rd, c.R[rm])
		return
	}

	carry := s && shift != 0
	var v uint32
	switch typ {
	case 0:
		v = shiftLSL(c, c.R[rm], shift, carry)
	case 1:
		v = shiftLSR(c, c.R[rm], shift, carry)
	case 2:
		v = shiftASR(c, c.R[rm], shift, carry)
	case 3:
		v = shiftROR(c, c.R[rm], shift, carry)
	}

	a := c.R[rn]
	var res uint32
	switch op {
	case 0x0: // AND
		res = a & v
	case 0x1: // BIC
		res = a &^ v
	case 0x2: // ORR (MOV when Rn == 15)
		if rn == 15 {
			res = v
		} else {
			res = a | v
		}
	case 0x3: // ORN (MVN when Rn == 15)
		if rn == 15 {
			res = ^v
		} else {
			res = a | ^v
		}
	case 0x4: // EOR
		res = a ^ v
	case 0x8: // ADD
		if s {
			res = c.addFlags(a, v, 0)
		} else {
			res = a + v
		}
	case 0xA: // ADC
		if s {
			res = c.addFlags(a, v, boolToU32(c.flag(flagC)))
		} else {
			res = a + v + boolToU32(c.flag(flagC))
		}
	case 0xB: // SBC
		if s {
			res = c.subFlags(a, v, boolToU32(c.flag(flagC)))
		} else {
			res = a - v - 1 + boolToU32(c.flag(flagC))
		}
	case 0xD: // SUB
		if s {
			res = c.subFlags(a, v, 1)
		} else {
			res = a - v
		}
	case 0xE: // RSB
		if s {
			res = c.subFlags(v, a, 1)
		} else {
			res = v - a
		}
	default:
		if c.Unimplemented != nil && c.Unimplemented(c.R[15]-4, hw1, hw2) {
			return
		}
		c.Halt("unimplemented data-processing op")
		return
	}

	if s {
		c.setNZ(res)
	}
	if rd != 15 {
		c.R[rd] = res
	}
}

// ---- F0/F1 group: modified immediate, MOVW/MOVT, ADDW/SUBW ----

func (c *CPU) exec32ImmData(hw1, hw2 uint16) {
	switch hw1 & 0xFBF0 {
	case 0xF240: // MOVW
		rd := uint32(hw2 >> 8 & 0xF)
		imm16 := uint32(hw1>>10&1)<<11 | uint32(hw1&0xF)<<12 | uint32(hw2>>12&7)<<8 | uint32(hw2&0xFF)
		c.R[rd] = imm16
		return
	case 0xF2C0: // MOVT
		rd := uint32(hw2 >> 8 & 0xF)
		imm16 := uint32(hw1>>10&1)<<11 | uint32(hw1&0xF)<<12 | uint32(hw2>>12&7)<<8 | uint32(hw2&0xFF)
		c.R[rd] = c.R[rd]&0xFFFF | imm16<<16
		return
	case 0xF200: // ADDW
		rn := uint32(hw1 & 0xF)
		rd := uint32(hw2 >> 8 & 0xF)
		imm12 := uint32(hw1>>10&1)<<11 | uint32(hw2>>12&7)<<8 | uint32(hw2&0xFF)
		c.R[rd] = c.R[rn] + imm12
		return
	case 0xF2A0: // SUBW
		rn := uint32(hw1 & 0xF)
		rd := uint32(hw2 >> 8 & 0xF)
		imm12 := uint32(hw1>>10&1)<<11 | uint32(hw2>>12&7)<<8 | uint32(hw2&0xFF)
		c.R[rd] = c.R[rn] - imm12
		return
	}

	// Modified immediate data processing.
	op := uint32(hw1>>5) & 0xF
	s := hw1&0x0010 != 0
	rn := uint32(hw1 & 0xF)
	rd := uint32(hw2 >> 8 & 0xF)
	imm := thumbExpandImm(uint32(hw1>>10&1), uint32(hw2>>12&7), uint32(hw2&0xFF))

	a := c.R[rn]
	var res uint32
	switch op {
	case 0x0: // AND
		res = a & imm
	case 0x1: // BIC
		res = a &^ imm
	case 0x2: // ORR (Rn=15 -> MOV)
		res = a | imm
		if rn == 15 {
			res = imm
		}
	case 0x3: // ORN (Rn=15 -> MVN)
		res = a | ^imm
		if rn == 15 {
			res = ^imm
		}
	case 0x4: // EOR
		res = a ^ imm
	case 0x8: // ADD
		if s {
			res = c.addFlags(a, imm, 0)
		} else {
			res = a + imm
		}
	case 0xA: // ADC
		if s {
			res = c.addFlags(a, imm, boolToU32(c.flag(flagC)))
		} else {
			res = a + imm + boolToU32(c.flag(flagC))
		}
	case 0xB: // SBC
		if s {
			res = c.subFlags(a, imm, boolToU32(c.flag(flagC)))
		} else {
			res = a - imm - 1 + boolToU32(c.flag(flagC))
		}
	case 0xD: // SUB
		if s {
			res = c.subFlags(a, imm, 1)
		} else {
			res = a - imm
		}
	case 0xE: // RSB
		if s {
			res = c.subFlags(imm, a, 1)
		} else {
			res = imm - a
		}
	default:
		if c.Unimplemented != nil && c.Unimplemented(c.R[15]-4, hw1, hw2) {
			return
		}
		c.Halt("unimplemented immediate data-processing op")
		return
	}
	if s {
		c.setNZ(res)
	}
	if rd != 15 {
		c.R[rd] = res
	}
}

// thumbExpandImm expands the 12-bit modified immediate encoding.
func thumbExpandImm(i, imm3, imm8 uint32) uint32 {
	imm12 := i<<11 | imm3<<8 | imm8
	if imm12>>10 == 0 {
		switch (imm12 >> 8) & 3 {
		case 0:
			return imm8
		case 1:
			return imm8<<16 | imm8
		case 2:
			return imm8<<24 | imm8<<8
		default:
			return imm8<<24 | imm8<<16 | imm8<<8 | imm8
		}
	}
	unrotated := 0x80 | (imm12 & 0x7F)
	rot := (imm12 >> 7) & 0x1F
	return ror32(unrotated, rot)
}

func ror32(v, n uint32) uint32 {
	n &= 31
	if n == 0 {
		return v
	}
	return v>>n | v<<(32-n)
}

// ---- Branches ----

func (c *CPU) exec32Branch(hw1, hw2 uint16, pc uint32) {
	t := (hw2 >> 14) & 0x3
	s := uint32(hw1>>10) & 1
	imm10 := uint32(hw1 & 0x3FF)
	j1 := uint32(hw2>>13) & 1
	j2 := uint32(hw2>>11) & 1
	i1 := (^(j1 ^ s)) & 1
	i2 := (^(j2 ^ s)) & 1

	switch {
	case t == 2 && hw2&0x1000 == 0: // B<c>.W (T3)
		cond := uint8(hw1 >> 6 & 0xF)
		imm6 := uint32(hw1 & 0x3F)
		imm := s<<20 | j2<<19 | j1<<18 | imm6<<12 | uint32(hw2&0x7FF)<<1
		if imm&(1<<20) != 0 {
			imm |= 0xFFE00000
		}
		if c.condTrue(cond) {
			c.R[15] = pc + 4 + imm
		}
	case t == 2: // B.W (T4)
		imm := s<<23 | i1<<22 | i2<<21 | imm10<<11 | uint32(hw2&0x7FF)
		imm <<= 1
		if imm&(1<<24) != 0 {
			imm |= 0xFE000000
		}
		c.R[15] = pc + 4 + imm
	case t == 3 && hw2&0x1000 != 0: // BL
		imm := s<<23 | i1<<22 | i2<<21 | imm10<<11 | uint32(hw2&0x7FF)
		imm <<= 1
		if imm&(1<<24) != 0 {
			imm |= 0xFE000000
		}
		c.R[14] = (pc + 4) | 1
		c.R[15] = pc + 4 + imm
	case t == 3: // BLX
		imm10L := uint32(hw2>>1) & 0x3FF
		h := uint32(hw2) & 1
		imm := s<<23 | i1<<22 | i2<<21 | imm10<<11 | imm10L<<1 | h
		imm <<= 1
		if imm&(1<<24) != 0 {
			imm |= 0xFE000000
		}
		c.R[14] = (pc + 4) | 1
		c.R[15] = (pc + 4 + imm) &^ 3
	default:
		c.Halt("unimplemented branch encoding")
	}
}

// ---- F8/F9 group: load/store single ----

func (c *CPU) exec32LoadStore(hw1, hw2 uint16) {
	rn := uint32(hw1 & 0xF)
	rt := uint32(hw2 >> 12)
	op := uint32(hw1>>4) & 0x7 // ignore bit7 (U)
	u := hw1&0x0080 != 0
	signed := hw1&0x0100 != 0

	// Literal forms: Rn == 15 for word loads.
	if rn == 15 && (op == 5 || op == 1) {
		imm := uint32(hw2 & 0xFFF)
		var addr uint32
		if u {
			addr = (c.pcRead() &^ 3) + imm
		} else {
			addr = (c.pcRead() &^ 3) - imm
		}
		if op == 5 {
			c.R[rt] = c.Bus.Read32(addr)
		} else {
			c.R[rt] = uint32(c.Bus.Read8(addr))
		}
		return
	}

	if hw2&0x0800 != 0 { // T4: imm8 with P/U/W
		p := hw2&0x0400 != 0
		uu := hw2&0x0200 != 0
		w := hw2&0x0100 != 0
		imm8 := uint32(hw2 & 0xFF)
		offset := int32(imm8)
		if !uu {
			offset = -offset
		}
		base := c.R[rn]
		addr := base
		if p {
			addr = uint32(int32(base) + offset)
		}
		c.doLoadStore(op, signed, addr, rt)
		if w {
			if p {
				c.R[rn] = addr
			} else {
				c.R[rn] = uint32(int32(base) + offset)
			}
		}
		return
	}

	if u {
		// Immediate 12-bit form (T3).
		addr := c.R[rn] + uint32(hw2&0xFFF)
		c.doLoadStore(op, signed, addr, rt)
		return
	}

	// Register offset form (T2).
	rm := uint32(hw2 & 0xF)
	imm2 := uint32(hw2>>4) & 3
	addr := c.R[rn] + (c.R[rm] << imm2)
	c.doLoadStore(op, signed, addr, rt)
}

// doLoadStore performs the access for the load/store group.
// op: 0 STRB, 1 LDRB, 2 STRH, 3 LDRH, 4 STR, 5 LDR, 6 LDRSB (signed), 7 LDRSH.
func (c *CPU) doLoadStore(op uint32, signed bool, addr, rt uint32) {
	if signed {
		switch op & 3 {
		case 1: // LDRSB
			c.R[rt] = uint32(int32(int8(c.Bus.Read8(addr))))
		case 3: // LDRSH
			c.R[rt] = uint32(int32(int16(c.Bus.Read16(addr))))
		}
		return
	}
	switch op {
	case 0:
		c.Bus.Write8(addr, uint8(c.R[rt]))
	case 1:
		c.R[rt] = uint32(c.Bus.Read8(addr))
	case 2:
		c.Bus.Write16(addr, uint16(c.R[rt]))
	case 3:
		c.R[rt] = uint32(c.Bus.Read16(addr))
	case 4:
		c.Bus.Write32(addr, c.R[rt])
	case 5:
		c.R[rt] = c.Bus.Read32(addr)
	}
}

// ---- F3 group: MSR, MRS, barriers, bitfield ----

func (c *CPU) exec32System(hw1, hw2 uint16) {
	switch {
	case hw1&0xFFF0 == 0xF380: // MSR
		rn := uint32(hw1 & 0xF)
		sysm := uint32(hw2 & 0xFF)
		c.writeSpecialReg(sysm, c.R[rn])
	case hw1 == 0xF3EF: // MRS
		rd := uint32(hw2 >> 8 & 0xF)
		sysm := uint32(hw2 & 0xFF)
		c.R[rd] = c.readSpecialReg(sysm)
	case hw1&0xFFF0 == 0xF3B0: // barriers / CLREX
		switch (hw2 >> 4) & 0xF {
		case 0x5, 0x4, 0x6, 0x7, 0x1:
			// DMB/DSB/ISB/CLREX: no-op in a single-threaded interpreter.
		case 0x2:
			c.monitorValid = false
		}
	case hw1&0xFFF0 == 0xF360: // BFI
		rn := uint32(hw1 & 0xF)
		rd := uint32(hw2 >> 8 & 0xF)
		lsb := uint32(hw2>>12&7)<<2 | uint32(hw2>>6&3)
		msb := uint32(hw2 & 0x1F)
		if msb >= lsb {
			width := msb - lsb + 1
			mask := uint32((uint64(1) << width) - 1)
			c.R[rd] = (c.R[rd] &^ (mask << lsb)) | ((c.R[rn] & mask) << lsb)
		}
	case hw1&0xFFF0 == 0xF36F: // BFC
		rd := uint32(hw2 >> 8 & 0xF)
		lsb := uint32(hw2>>12&7)<<2 | uint32(hw2>>6&3)
		msb := uint32(hw2 & 0x1F)
		if msb >= lsb {
			width := msb - lsb + 1
			mask := uint32((uint64(1) << width) - 1)
			c.R[rd] &^= mask << lsb
		}
	case hw1&0xFFF0 == 0xF3C0: // UBFX
		rn := uint32(hw1 & 0xF)
		rd := uint32(hw2 >> 8 & 0xF)
		lsb := uint32(hw2>>12&7)<<2 | uint32(hw2>>6&3)
		width := uint32(hw2&0x1F) + 1
		c.R[rd] = (c.R[rn] >> lsb) & uint32((uint64(1)<<width)-1)
	case hw1&0xFFF0 == 0xF340: // SBFX
		rn := uint32(hw1 & 0xF)
		rd := uint32(hw2 >> 8 & 0xF)
		lsb := uint32(hw2>>12&7)<<2 | uint32(hw2>>6&3)
		width := uint32(hw2&0x1F) + 1
		v := c.R[rn] >> lsb
		shift := 32 - width
		c.R[rd] = uint32(int32(v<<shift) >> shift)
	case hw1 == 0xF3AF && hw2 == 0x8000: // NOP.W
	default:
		if c.Unimplemented != nil && c.Unimplemented(c.R[15]-4, hw1, hw2) {
			return
		}
		c.Halt("unimplemented system instruction")
	}
}

func (c *CPU) readSpecialReg(sysm uint32) uint32 {
	switch sysm {
	case 0x00: // APSR
		return c.APSR & 0xF8000000
	case 0x01: // IAPSR
		return c.APSR&0xF8000000 | c.IPSR&0x1FF
	case 0x02: // EAPSR
		return c.APSR & 0xF8000000
	case 0x03: // XPSR
		return c.APSR&0xF8000000 | c.IPSR&0x1FF
	case 0x05: // IPSR
		return c.IPSR & 0x1FF
	case 0x06: // EPSR
		return 0x01000000
	case 0x08: // MSP
		return c.MSP
	case 0x09: // PSP
		return c.PSP
	case 0x10: // PRIMASK
		return c.PRIMASK & 1
	case 0x11: // FAULTMASK
		return 0
	case 0x12: // BASEPRI
		return 0
	case 0x13: // BASEPRI_MAX
		return 0
	case 0x14: // CONTROL
		return c.CONTROL
	}
	return 0
}

func (c *CPU) writeSpecialReg(sysm, v uint32) {
	switch sysm {
	case 0x00, 0x01, 0x02, 0x03: // APSR variants
		c.APSR = (c.APSR & 0x07FFFFFF) | (v & 0xF8000000)
	case 0x08: // MSP
		c.MSP = v &^ 3
		if c.IPSR != 0 || c.CONTROL&2 == 0 {
			c.R[13] = c.MSP
		}
	case 0x09: // PSP
		c.PSP = v &^ 3
	case 0x10: // PRIMASK
		c.PRIMASK = v & 1
	case 0x11: // FAULTMASK
	case 0x12, 0x13: // BASEPRI
	case 0x14: // CONTROL
		c.setControl(v)
	}
}

// ---- FA group: unary operations ----

func (c *CPU) exec32Unary(hw1, hw2 uint16) {
	op := uint32(hw2>>4) & 0xF
	rd := uint32(hw2 >> 8 & 0xF)
	rm := uint32(hw1 & 0xF)

	// Register-shift forms (LSL/LSR/ASR/ROR) have zero in hw2[7:4].
	if op == 0x0 && hw1&0x00F0 != 0xF0 {
		rn := uint32(hw2 & 0xF)
		shiftType := uint32(hw1>>4) & 0xF
		v := c.R[rn]
		n := c.R[rm] & 0xFF
		switch shiftType {
		case 0x0: // LSL
			c.R[rd] = shiftLSL(c, v, n, n != 0)
		case 0x2: // LSR
			c.R[rd] = shiftLSR(c, v, n, n != 0)
		case 0x4: // ASR
			c.R[rd] = shiftASR(c, v, n, n != 0)
		case 0x6: // ROR
			c.R[rd] = shiftROR(c, v, n, n != 0)
		default:
			c.Halt("unimplemented register shift")
		}
		c.setNZ(c.R[rd])
		return
	}

	switch hw1 & 0xFFF0 {
	case 0xFA00: // SXTH
		c.R[rd] = uint32(int32(int16(c.R[rm])))
	case 0xFA10: // UXTH
		c.R[rd] = uint32(uint16(c.R[rm]))
	case 0xFA40: // SXTB
		c.R[rd] = uint32(int32(int8(c.R[rm])))
	case 0xFA50: // UXTB
		c.R[rd] = uint32(uint8(c.R[rm]))
	case 0xFA90: // REV / REV16 / RBIT / REVSH
		switch op {
		case 0x8:
			c.R[rd] = rev32(c.R[rm])
		case 0x9:
			c.R[rd] = rev16(c.R[rm])
		case 0xA:
			c.R[rd] = reverseBits(c.R[rm])
		case 0xB:
			c.R[rd] = uint32(int32(int16(rev16(c.R[rm]))))
		}
	case 0xFAA0: // SEL
		c.R[rd] = c.R[rm]
	case 0xFAB0: // CLZ
		if op == 0x8 {
			c.R[rd] = uint32(clz32(c.R[rm]))
		}
	default:
		if c.Unimplemented != nil && c.Unimplemented(c.R[15]-4, hw1, hw2) {
			return
		}
		c.Halt("unimplemented unary instruction")
	}
}

// ---- FB group: multiply, divide, long multiply ----

func (c *CPU) exec32MulDiv(hw1, hw2 uint16) {
	rn := uint32(hw1 & 0xF)
	rd := uint32(hw2 >> 8 & 0xF)
	rm := uint32(hw2 & 0xF)
	ra := uint32(hw2 >> 12 & 0xF)

	switch hw1 & 0xFFF0 {
	case 0xFB00: // MUL / MLA / MLS
		switch {
		case hw2&0xF000 == 0xF000: // MUL: Ra field is 0b1111
			c.R[rd] = c.R[rn] * c.R[rm]
		case hw2&0x00F0 == 0x0000: // MLA
			c.R[rd] = c.R[rn]*c.R[rm] + c.R[ra]
		case hw2&0x00F0 == 0x0010: // MLS
			c.R[rd] = c.R[ra] - c.R[rn]*c.R[rm]
		default:
			c.Halt("unimplemented multiply variant")
		}
	case 0xFBA0: // UMULL
		lo, hi := umull(c.R[rn], c.R[rm])
		c.R[ra] = lo
		c.R[rd] = hi
	case 0xFBE0: // UMLAL
		lo, hi := umull(c.R[rn], c.R[rm])
		r := uint64(c.R[rd])<<32 | uint64(c.R[ra])
		r += uint64(lo) | uint64(hi)<<32
		c.R[ra] = uint32(r)
		c.R[rd] = uint32(r >> 32)
	case 0xFB80: // SMULL
		r := int64(int32(c.R[rn])) * int64(int32(c.R[rm]))
		c.R[ra] = uint32(r)
		c.R[rd] = uint32(uint64(r) >> 32)
	case 0xFBC0: // SMLAL
		r := int64(int32(c.R[rd]))<<32 | int64(c.R[ra])
		r += int64(int32(c.R[rn])) * int64(int32(c.R[rm]))
		c.R[ra] = uint32(r)
		c.R[rd] = uint32(uint64(r) >> 32)
	case 0xFBB0: // UDIV
		if c.R[rm] == 0 {
			c.R[rd] = 0
		} else {
			c.R[rd] = c.R[rn] / c.R[rm]
		}
	case 0xFB90: // SDIV
		if c.R[rm] == 0 {
			c.R[rd] = 0
		} else {
			c.R[rd] = uint32(int32(c.R[rn]) / int32(c.R[rm]))
		}
	case 0xFB10, 0xFB30, 0xFB50, 0xFB70: // SMULBB/SMULTT/SMULTB/SMULBT etc.
		c.smulDSP(hw1, hw2)
	default:
		if c.Unimplemented != nil && c.Unimplemented(c.R[15]-4, hw1, hw2) {
			return
		}
		c.Halt("unimplemented multiply/divide instruction")
	}
}

func (c *CPU) smulDSP(hw1, hw2 uint16) {
	rn := uint32(hw1 & 0xF)
	rm := uint32(hw2 & 0xF)
	rd := uint32(hw2 >> 8 & 0xF)
	ra := uint32(hw2 >> 12 & 0xF)
	n := c.R[rn]
	m := c.R[rm]
	pick := func(v uint32, half int) int32 {
		if half == 0 {
			return int32(int16(v))
		}
		return int32(int16(v >> 16))
	}
	op := uint32(hw1>>4) & 0xF
	var a, b int32
	switch op {
	case 0x1: // SMULBB
		a, b = pick(n, 0), pick(m, 0)
	case 0x2: // SMULBT
		a, b = pick(n, 0), pick(m, 1)
	case 0x3: // SMULTB
		a, b = pick(n, 1), pick(m, 0)
	case 0x4: // SMULTT
		a, b = pick(n, 1), pick(m, 1)
	default:
		c.Halt("unimplemented DSP multiply")
		return
	}
	r := a * b
	if hw2&0xF000 != 0 {
		r += int32(c.R[ra])
	}
	c.R[rd] = uint32(r)
}

// Utility functions.

func umull(a, b uint32) (lo, hi uint32) {
	r := uint64(a) * uint64(b)
	return uint32(r), uint32(r >> 32)
}

func reverseBits(v uint32) uint32 {
	v = (v&0x55555555)<<1 | (v>>1)&0x55555555
	v = (v&0x33333333)<<2 | (v>>2)&0x33333333
	v = (v&0x0F0F0F0F)<<4 | (v>>4)&0x0F0F0F0F
	v = (v&0x00FF00FF)<<8 | (v>>8)&0x00FF00FF
	return v<<16 | v>>16
}

func clz32(v uint32) int {
	if v == 0 {
		return 32
	}
	n := 0
	for v&0x80000000 == 0 {
		n++
		v <<= 1
	}
	return n
}
