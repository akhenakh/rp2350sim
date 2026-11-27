package m33

import (
	"fmt"
	"math"
)

// VFPv5 single-precision support (the RP2350's Cortex-M33 has an FPv5-SP FPU;
// the Pico SDK builds with -march=armv8-m.main+fp+dsp and softfp, so GCC uses
// these instructions for float arithmetic while doubles stay in libgcc).
//
// Register fields in the 32-bit Thumb encoding are split:
//   Sd = hw2[15:12]<<1 | hw1[6]
//   Sn = hw1[3:0]<<1   | hw2[7]
//   Sm = hw2[3:0]<<1   | hw2[5]
// The D/N/M bits are the low bit of each register number.

const (
	fpscrN   = 1 << 31
	fpscrZ   = 1 << 30
	fpscrC   = 1 << 29
	fpscrV   = 1 << 28
	fpscrIOC = 1 << 0
	fpscrDZC = 1 << 1
	fpscrOFC = 1 << 2
	fpscrUFC = 1 << 3
	fpscrIXC = 1 << 4
)

func (c *CPU) vfpS(i int) float32       { return math.Float32frombits(c.S[i&31]) }
func (c *CPU) vfpSetS(i int, v float32) { c.S[i&31] = math.Float32bits(v) }

func (c *CPU) vfpExecute(hw1, hw2 uint16) {
	// The first FP instruction in a handler completes a lazily reserved FP
	// frame, exactly as the hardware does.
	c.flushLazy()
	c.FPUActive = true
	// With FPCCR.ASPEN clear the processor does not track an active FP
	// context in CONTROL.FPCA, so leave the bit alone.
	if c.FPCCR&fpcASPN != 0 {
		c.CONTROL |= 4
	}
	if hw1&0xFE00 == 0xEC00 {
		// VMOV Dm,Rt,Rt2 and VMOV Rt,Rt2,Dm are EC40..EC5F (U=0, P=0,
		// W=0, D=1). Masking only 0xFF00 also matched the non-writeback
		// VSTMIA/VLDMIA forms starting at an odd single register (ECC0/
		// ECD0), which must go to vfpLoadStore.
		if hw1&0xFFE0 == 0xEC40 {
			c.vfpDoubleTransfer(hw1, hw2)
			return
		}
		c.vfpLoadStore(hw1, hw2)
		return
	}
	if hw1&0xFF00 != 0xEE00 {
		c.Halt(fmt.Sprintf("unimplemented VFP instruction (%04x %04x)", hw1, hw2))
		return
	}
	// Core register transfers have hw2 bit 4 set; VFP data processing does not.
	if hw2&0x0010 != 0 {
		c.vfpCoreTransfer(hw1, hw2)
		return
	}
	sd := int((hw2>>11)&0x1E) | int((hw1>>6)&1)
	sn := int((hw1&0xF)<<1) | int((hw2>>7)&1)
	sm := int((hw2&0xF)<<1) | int((hw2>>5)&1)

	// Three-operand arithmetic identifies itself once the register fields are
	// cleared (including the D/N/M low bits).
	h1n := hw1 &^ 0x004F
	h2n := hw2 &^ 0xF0AF
	switch {
	case h1n == 0xEE30 && h2n == 0x0A00:
		c.vfpSetS(sd, c.vfpAdd(c.vfpS(sn), c.vfpS(sm)))
		return
	case h1n == 0xEE30 && h2n == 0x0A40:
		c.vfpSetS(sd, c.vfpAdd(c.vfpS(sn), -c.vfpS(sm)))
		return
	case h1n == 0xEE20 && h2n == 0x0A00:
		c.vfpSetS(sd, c.vfpMul(c.vfpS(sn), c.vfpS(sm)))
		return
	case h1n == 0xEE80 && h2n == 0x0A00:
		c.vfpSetS(sd, c.vfpDiv(c.vfpS(sn), c.vfpS(sm)))
		return
	case h1n == 0xEE00 && h2n == 0x0A00:
		c.vfpSetS(sd, c.vfpFMA(c.vfpS(sd), c.vfpS(sn), c.vfpS(sm), false))
		return
	case h1n == 0xEE00 && h2n == 0x0A40:
		c.vfpSetS(sd, c.vfpFMA(c.vfpS(sd), c.vfpS(sn), c.vfpS(sm), true))
		return
	case h1n == 0xEEA0 && h2n == 0x0A00: // VFMA
		c.vfpSetS(sd, c.vfpFMA(c.vfpS(sd), c.vfpS(sn), c.vfpS(sm), false))
		return
	case h1n == 0xEEA0 && h2n == 0x0A40: // VFMS
		c.vfpSetS(sd, c.vfpFMA(c.vfpS(sd), c.vfpS(sn), c.vfpS(sm), true))
		return
	case h1n == 0xEE90 && h2n == 0x0A40: // VFNMA
		c.vfpSetS(sd, -c.vfpFMA(c.vfpS(sd), c.vfpS(sn), c.vfpS(sm), false))
		return
	case h1n == 0xEE90 && h2n == 0x0A00: // VFNMS
		c.vfpSetS(sd, -c.vfpFMA(c.vfpS(sd), c.vfpS(sn), c.vfpS(sm), true))
		return
	}

	// Two-operand group: the low nibble of hw1 and bits 7..6 of hw2 select the
	// operation. The source is Sm.
	a := hw1 & 0xFF80 // bits 15..7; bit 6 is Sd[0] (D)
	b := hw2 & 0x0FC0
	op := hw1 & 0xF
	switch {
	case a == 0xEE80 && b == 0x0B40 && op == 0:
		// VMOV.F64 Dd, Dm needs the double-precision extension. The M33's
		// FPv5-SP has no FP64 state, so this encoding is UNDEFINED. Halting
		// also keeps D16-D31 from indexing past the S register file.
		c.Halt("undefined VFP double register")
	case a == 0xEE80 && b == 0x0A40 && op == 0:
		c.vfpSetS(sd, c.vfpS(sm)) // VMOV Sd, Sm
	case a == 0xEE80 && b == 0x0AC0 && op == 0:
		c.vfpSetS(sd, float32(math.Abs(float64(c.vfpS(sm)))))
	case a == 0xEE80 && b == 0x0A40 && op == 1:
		c.vfpSetS(sd, -c.vfpS(sm))
	case a == 0xEE80 && b == 0x0AC0 && op == 1:
		c.vfpSetS(sd, c.vfpSqrt(c.vfpS(sm)))
	case a == 0xEE80 && b == 0x0A40 && op == 4:
		c.vfpCompare(c.vfpS(sd), c.vfpS(sm))
	case a == 0xEE80 && b == 0x0AC0 && op == 4:
		c.vfpCompare(c.vfpS(sd), c.vfpS(sm))
	case a == 0xEE80 && b == 0x0A40 && op == 5:
		c.vfpCompare(c.vfpS(sd), 0)
	case hw1&0xFF80 == 0xEE80 && hw2&0x0FF0 == 0x0A00:
		// VMOV.F32 Sd, #imm8: imm8 = hw1[3:0]<<4 | hw2[3:0].
		imm := uint32(hw1&0xF)<<4 | uint32(hw2&0xF)
		c.vfpSetS(sd, vfpExpandImm(imm))
	case a == 0xEE80 && b == 0x0AC0 && op == 8:
		v := int32(c.S[sm])
		r := float32(v)
		if float64(r) != float64(v) {
			c.FPSCR |= fpscrIXC
		}
		c.vfpSetS(sd, r) // VCVT.F32.S32
	case a == 0xEE80 && b == 0x0A40 && op == 8:
		v := c.S[sm]
		r := float32(v)
		if float64(r) != float64(v) {
			c.FPSCR |= fpscrIXC
		}
		c.vfpSetS(sd, r) // VCVT.F32.U32
	case a == 0xEE80 && b == 0x0AC0 && op == 0xD:
		c.S[sd&31] = c.vfpToInt32(c.vfpS(sm)) // VCVT.S32.F32 (truncate)
	case a == 0xEE80 && b == 0x0AC0 && op == 0xC:
		c.S[sd&31] = c.vfpToUint32(c.vfpS(sm)) // VCVT.U32.F32
	default:
		c.Halt(fmt.Sprintf("unimplemented VFP instruction (%04x %04x)", hw1, hw2))
	}
}

func vfpSignalingNaN(f float32) bool {
	bits := math.Float32bits(f)
	return bits&0x7F800000 == 0x7F800000 && bits&0x007FFFFF != 0 && bits&0x00400000 == 0
}

func (c *CPU) vfpRecord(a, b, result float32, exact float64, invalid, divideByZero bool) {
	if invalid || vfpSignalingNaN(a) || vfpSignalingNaN(b) {
		c.FPSCR |= fpscrIOC
	}
	if divideByZero {
		c.FPSCR |= fpscrDZC
	}
	if math.IsNaN(float64(result)) {
		return
	}
	if math.IsInf(float64(result), 0) {
		if !math.IsInf(exact, 0) {
			c.FPSCR |= fpscrOFC | fpscrIXC
		}
		return
	}
	if exact != float64(result) {
		c.FPSCR |= fpscrIXC
		if result == 0 || math.Abs(float64(result)) < math.SmallestNonzeroFloat32 {
			c.FPSCR |= fpscrUFC
		}
	}
}

func (c *CPU) vfpAdd(a, b float32) float32 {
	result := a + b
	invalid := math.IsInf(float64(a), 0) && math.IsInf(float64(b), 0) && math.Signbit(float64(a)) != math.Signbit(float64(b))
	c.vfpRecord(a, b, result, float64(a)+float64(b), invalid, false)
	return result
}

func (c *CPU) vfpMul(a, b float32) float32 {
	result := a * b
	invalid := (a == 0 && math.IsInf(float64(b), 0)) || (b == 0 && math.IsInf(float64(a), 0))
	c.vfpRecord(a, b, result, float64(a)*float64(b), invalid, false)
	return result
}

func (c *CPU) vfpDiv(a, b float32) float32 {
	result := a / b
	invalid := a == 0 && b == 0
	divideByZero := b == 0 && a != 0 && !math.IsInf(float64(a), 0) && !math.IsNaN(float64(a))
	c.vfpRecord(a, b, result, float64(a)/float64(b), invalid, divideByZero)
	return result
}

func (c *CPU) vfpSqrt(a float32) float32 {
	exact := math.Sqrt(float64(a))
	result := float32(exact)
	c.vfpRecord(a, 0, result, exact, a < 0, false)
	return result
}

func (c *CPU) vfpFMA(a, b, addend float32, subtract bool) float32 {
	product := b * addend
	var result float32
	if subtract {
		result = a - product
	} else {
		result = a + product
	}
	exactProduct := float64(b) * float64(addend)
	exact := float64(a) + exactProduct
	if subtract {
		exact = float64(a) - exactProduct
	}
	invalid := (b == 0 && math.IsInf(float64(addend), 0)) || (addend == 0 && math.IsInf(float64(b), 0)) ||
		(math.IsInf(float64(a), 0) && math.IsInf(exactProduct, 0) && math.Signbit(float64(a)) != math.Signbit(exactProduct))
	if subtract {
		invalid = invalid || (math.IsInf(float64(a), 0) && math.IsInf(exactProduct, 0) && math.Signbit(float64(a)) == math.Signbit(exactProduct))
	}
	c.vfpRecord(a, b, result, exact, invalid, false)
	if vfpSignalingNaN(addend) {
		c.FPSCR |= fpscrIOC
	}
	return result
}

// vfpToInt32 converts with the Arm rules: NaN gives zero, out-of-range values
// saturate, and a fractional value raises IXC. Go's float-to-integer
// conversion is host-dependent for NaN and out-of-range inputs, so it cannot
// be used directly.
func (c *CPU) vfpToInt32(f float32) uint32 {
	d := float64(f)
	switch {
	case math.IsNaN(d):
		c.FPSCR |= fpscrIOC
		return 0
	case d >= 2147483648.0:
		c.FPSCR |= fpscrIOC
		return 0x7FFFFFFF
	case d < -2147483648.0:
		c.FPSCR |= fpscrIOC
		return 0x80000000
	}
	i := int32(d)
	if float64(i) != d {
		c.FPSCR |= fpscrIXC
	}
	return uint32(i)
}

// vfpToUint32 is the unsigned counterpart: NaN and negative values give zero,
// out-of-range values saturate to 0xFFFFFFFF.
func (c *CPU) vfpToUint32(f float32) uint32 {
	d := float64(f)
	switch {
	case math.IsNaN(d):
		c.FPSCR |= fpscrIOC
		return 0
	case d >= 4294967296.0:
		c.FPSCR |= fpscrIOC
		return 0xFFFFFFFF
	case d < 0:
		c.FPSCR |= fpscrIOC
		return 0
	}
	i := uint32(d)
	if float64(i) != d {
		c.FPSCR |= fpscrIXC
	}
	return i
}

func (c *CPU) vfpCompare(a, b float32) {
	flags := c.FPSCR &^ (fpscrN | fpscrZ | fpscrC | fpscrV)
	switch {
	case math.IsNaN(float64(a)) || math.IsNaN(float64(b)):
		flags |= fpscrC | fpscrV | fpscrIOC // unordered
	case a == b:
		flags |= fpscrZ | fpscrC
	case a < b:
		flags |= fpscrN
	default:
		flags |= fpscrC
	}
	c.FPSCR = flags
}

// vfpExpandImm expands the 8-bit VFP immediate encoding to a float.
func vfpExpandImm(imm8 uint32) float32 {
	bits := (imm8&0x80)<<24 | ((imm8 & 0x40) << 25) | (imm8&0x3F)<<19
	if imm8&0x40 == 0 {
		bits |= 0x40000000
	} else {
		bits |= 0x3E000000
	}
	return math.Float32frombits(bits)
}

// vfpDoubleTransfer handles VMOV Dm, Rt, Rt2 and VMOV Rt, Rt2, Dm.
func (c *CPU) vfpDoubleTransfer(hw1, hw2 uint16) {
	dm := int(hw2 & 0xF)
	rt := int(hw2 >> 12)
	rt2 := int(hw1 & 0xF)
	if hw1&0x0010 != 0 { // Dm to core
		c.R[rt] = c.S[dm*2]
		c.R[rt2] = c.S[dm*2+1]
		return
	}
	c.S[dm*2] = c.R[rt]
	c.S[dm*2+1] = c.R[rt2]
}

func (c *CPU) vfpCoreTransfer(hw1, hw2 uint16) {
	rt := int(hw2 >> 12)
	switch (hw1 >> 4) & 0xF {
	case 0x0: // VMOV Sd, Rt
		sd := int((hw1&0xF)<<1) | int((hw2>>7)&1)
		c.S[sd] = c.R[rt]
	case 0x1: // VMOV Rt, Sd
		sd := int((hw1&0xF)<<1) | int((hw2>>7)&1)
		c.R[rt] = c.S[sd]
	case 0xE: // VMSR FPSCR, Rt
		// NZCV/QC, AHP/DN/FZ/RMode and the exception flag bits are writable.
		c.FPSCR = c.R[rt] & 0xFFC0_009F
	case 0xF: // VMRS Rt, FPSCR (Rt == 15 copies NZCV to APSR)
		if rt == 15 {
			// Only FPSCR[31:28] maps to APSR; bit 27 is APSR.Q, not QC.
			c.APSR = (c.APSR &^ 0xF0000000) | (c.FPSCR & 0xF0000000)
		} else {
			c.R[rt] = c.FPSCR
		}
	default:
		c.Halt(fmt.Sprintf("unimplemented VFP transfer (%04x %04x)", hw1, hw2))
	}
}

func (c *CPU) vfpLoadStore(hw1, hw2 uint16) {
	rn := int(hw1 & 0xF)
	d := int((hw1 >> 6) & 1)
	load := hw1&0x0010 != 0
	double := hw2&0x0100 != 0
	sd := int((hw2>>12)<<1) | d
	imm8 := uint32(hw2 & 0xFF)
	if double {
		dreg := int(hw2>>12) | d<<4
		if dreg > 15 {
			// FPv5-SP only has D0-D15 (S0-S31); D16-D31 encodings are
			// UNDEFINED and must not index past the register file.
			c.Halt("undefined VFP double register")
			return
		}
		sd = dreg * 2
	}

	if hw1&0x0F20 == 0x0D00 {
		// VLDR/VSTR: P=1, W=0, imm8*4, U selects the sign. P=0 with the same
		// low bits is non-writeback VLDMIA/VSTMIA and must stay in the block
		// path below. The literal form (Rn=15) addresses Align(PC,4).
		off := int32(imm8 * 4)
		base := c.R[rn]
		if rn == 15 {
			base = c.pcRead() &^ 3
		}
		addr := base
		if hw1&0x0080 != 0 {
			addr += uint32(off)
		} else {
			addr -= uint32(off)
		}
		if load {
			c.S[sd] = c.Bus.Read32(addr)
			if double {
				c.S[sd+1] = c.Bus.Read32(addr + 4)
			}
		} else {
			c.Bus.Write32(addr, c.S[sd])
			if double {
				c.Bus.Write32(addr+4, c.S[sd+1])
			}
		}
		return
	}

	// VLDM/VSTM (including push/pop): P/U/W/L in hw1[8:4].
	u := hw1&0x0080 != 0
	w := hw1&0x0020 != 0
	count := imm8
	if double {
		count = imm8 / 2
	}
	// imm8 is the transfer size in 4-byte units for both singles and doubles.
	bytes := imm8 * 4
	addr := c.R[rn]
	if !u {
		addr -= bytes
	}
	if load {
		for i := range count {
			if double {
				d := sd + int(i)*2
				if d > 30 {
					c.Halt("undefined VFP double register")
					return
				}
				c.S[d] = c.Bus.Read32(addr + uint32(i)*8)
				c.S[d+1] = c.Bus.Read32(addr + uint32(i)*8 + 4)
			} else {
				if sd+int(i) > 31 {
					c.Halt("undefined VFP register")
					return
				}
				c.S[sd+int(i)] = c.Bus.Read32(addr + uint32(i)*4)
			}
		}
	} else {
		for i := range count {
			if double {
				d := sd + int(i)*2
				if d > 30 {
					c.Halt("undefined VFP double register")
					return
				}
				c.Bus.Write32(addr+uint32(i)*8, c.S[d])
				c.Bus.Write32(addr+uint32(i)*8+4, c.S[d+1])
			} else {
				if sd+int(i) > 31 {
					c.Halt("undefined VFP register")
					return
				}
				c.Bus.Write32(addr+uint32(i)*4, c.S[sd+int(i)])
			}
		}
	}
	if w {
		if u {
			c.R[rn] = addr + bytes
		} else {
			c.R[rn] = addr
		}
	}
}
