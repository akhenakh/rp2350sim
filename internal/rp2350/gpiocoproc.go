package rp2350

import (
	"log/slog"

	"github.com/akhenakh/rp2350sim/internal/m33"
)

// RP2350 GPIO coprocessor (coprocessor 0).
//
// With PICO_USE_GPIO_COPROCESSOR (the default on RP2350) the Pico SDK writes
// GPIO output and output-enable masks through CP0 instead of SIO: MCR/MCRR
// put, set, clear or toggle bits and MRC/MRRC read them back. Modelling it as
// direct pin-state updates keeps the board view and logic analyzer accurate.

// ExecGPIOCoproc services an MCR/MCRR/MRC/MRRC addressed to coprocessor 0.
func (c *Chip) ExecGPIOCoproc(cpu *m33.CPU, hw1, hw2 uint16) {
	crm := uint32(hw2 & 0xF)
	rt := uint32(hw2 >> 12)

	if hw1&0xEE00 == 0xEC00 { // MCRR / MRRC
		rt2 := uint32(hw1 & 0xF)
		opc := uint32(hw2>>4) & 0xF
		if hw1&0x0010 != 0 { // MRRC
			lo, hi := c.cpRead(crm)
			if rt != 15 {
				cpu.R[rt] = lo
			}
			if rt2 != 15 {
				cpu.R[rt2] = hi
			}
			return
		}
		switch opc {
		case 0, 1, 2, 3:
			c.cpApply(crm, opc, uint64(cpu.R[rt]))
		case 4: // bit put: Rt is the pin, Rt2 the value (1 set, 0 clear)
			c.cpBit(crm, uint(cpu.R[rt]), 3-uint32(cpu.R[rt2]&1))
		case 5, 6, 7:
			if cpu.R[rt2]&1 != 0 {
				c.cpBit(crm, uint(cpu.R[rt]), opc-4)
			}
		default:
			c.Logger.Warn("gpio coproc: unsupported MCRR", slog.Uint64("opc", uint64(opc)))
		}
		return
	}

	if hw1&0x0010 != 0 { // MRC
		lo, hi := c.cpRead(crm)
		if rt != 15 {
			switch crm {
			case 1, 5, 9:
				cpu.R[rt] = hi
			default:
				cpu.R[rt] = lo
			}
		}
		return
	}

	// MCR: opc1 is hw1[7:5].
	opc := uint32(hw1>>5) & 7
	v := cpu.R[rt]
	switch {
	case opc <= 3:
		c.cpApply(crm, opc, uint64(v))
	case opc >= 5:
		c.cpBit(crm, uint(v), opc-4)
	default:
		c.Logger.Warn("gpio coproc: unsupported MCR", slog.Uint64("opc", uint64(opc)))
	}
}

// cpSpace maps CRm to the output, output-enable or input space and whether the
// operation addresses the high pin half.
func cpSpace(crm uint32) (space int, high bool, ok bool) {
	switch crm {
	case 0:
		return 0, false, true // outputs, low
	case 1:
		return 0, true, true // outputs, high
	case 4:
		return 1, false, true // output enable, low
	case 5:
		return 1, true, true // output enable, high
	case 8:
		return 2, false, true // inputs, low
	case 9:
		return 2, true, true // inputs, high
	}
	return 0, false, false
}

// cpApply applies put/xor/set/clear to a 32-bit half of the output or OE word.
func (c *Chip) cpApply(crm uint32, opc uint32, v uint64) {
	space, high, ok := cpSpace(crm)
	if !ok || space == 2 {
		return
	}
	shift := uint(0)
	if high {
		shift = 32
	}
	oldOut, oldOE := c.Pins.Out, c.Pins.OE
	dst := &c.Pins.Out
	if space == 1 {
		dst = &c.Pins.OE
	}
	val := (v & 0xFFFFFFFF) << shift
	switch opc {
	case 0: // put: replace the addressed 32-bit half
		*dst = (*dst &^ (uint64(0xFFFFFFFF) << shift)) | val
	case 1:
		*dst ^= val
	case 2:
		*dst |= val
	case 3:
		*dst &^= val
	}
	if c.Pins.Out != oldOut || c.Pins.OE != oldOE {
		c.pinsChanged(oldOut, oldOE)
	}
}

// cpBit applies a single-pin operation: 1 xor, 2 set (or put), 3 clear.
func (c *Chip) cpBit(crm uint32, pin uint, opc uint32) {
	space, _, ok := cpSpace(crm)
	if !ok || space == 2 || pin >= 48 {
		return
	}
	mask := uint64(1) << pin
	oldOut, oldOE := c.Pins.Out, c.Pins.OE
	dst := &c.Pins.Out
	if space == 1 {
		dst = &c.Pins.OE
	}
	switch opc {
	case 1:
		*dst ^= mask
	case 2, 4: // set / put(1)
		*dst |= mask
	case 3, 5: // clear / put(0)
		*dst &^= mask
	}
	if c.Pins.Out != oldOut || c.Pins.OE != oldOE {
		c.pinsChanged(oldOut, oldOE)
	}
}

func (c *Chip) cpRead(crm uint32) (lo, hi uint32) {
	space, _, ok := cpSpace(crm)
	if !ok {
		return 0, 0
	}
	switch space {
	case 0:
		return uint32(c.Pins.Out), uint32(c.Pins.Out >> 32)
	case 1:
		return uint32(c.Pins.OE), uint32(c.Pins.OE >> 32)
	default:
		return uint32(c.Pins.inputWord(0)), uint32(c.Pins.inputWord(32))
	}
}
