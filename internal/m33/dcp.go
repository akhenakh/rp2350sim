package m33

import "math"

// RP2350 double coprocessor (DCP, coprocessor 4).
//
// The Pico SDK routes libgcc's double routines through the DCP: operands are
// loaded with MCRR, operations are issued as CDP instructions, and results are
// read back with MRC/MRRC. The canned sequences in hardware/dcp_canned.inc.S do
// their own mantissa arithmetic between those steps, but the final result is
// always one of a handful of IEEE operations, so a functional model is enough:
// keep X/Y, compute on the normalise/read step, and return 32- or 64-bit
// results. Intermediate mantissa reads return zero; their values are discarded
// by the sequence before the result is read.

// DCPFormat and DCPOp are exported for diagnostics.
type DCPFormat = dcpFormat
type DCPOp = dcpOp

type dcpFormat int

const (
	dcpUnset dcpFormat = iota
	dcpFloat
	dcpDouble
	dcpInt
	dcpUint
)

type dcpOp int

const (
	dcpNone dcpOp = iota
	dcpAdd
	dcpSub
	dcpMul
	dcpDiv
	dcpSqrt
)

type dcpConvert uint8

const (
	dcpConvertNone dcpConvert = iota
	dcpConvertTruncate
	dcpConvertRound
)

type dcpState struct {
	x, y       float64
	result     float64
	format     dcpFormat // operand format
	haveResult bool
	op         dcpOp
	convert    dcpConvert
	flags      uint32
	class      uint32
}

// execDCP handles an instruction addressed to coprocessor 4.
func (c *CPU) execDCP(hw1, hw2 uint16) {
	if hw1&0xEE00 == 0xEC00 {
		if hw1&0x0010 != 0 { // MRRC / MRRC2
			c.dcpMRRC(hw1, hw2)
		} else { // MCRR / MCRR2
			c.dcpMCRR(hw1, hw2)
		}
		return
	}
	if hw2&0x0010 != 0 { // MRC / MRC2 (bit 4 of the second halfword)
		c.dcpMRC(hw1, hw2)
		return
	}
	c.dcpCDP(hw1, hw2)
}

func (c *CPU) dcpMCRR(hw1, hw2 uint16) {
	c.dcp.convert = dcpConvertNone
	opc := (hw2 >> 4) & 0xF
	crm := hw2 & 0xF
	rt := uint32(hw2 >> 12)
	rt2 := uint32(hw1 & 0xF)
	bits := uint64(c.R[rt]) | uint64(c.R[rt2])<<32
	switch {
	case opc == 0 && crm == 0: // WXMD: restore X
		c.dcp.x = math.Float64frombits(bits)
	case opc == 0 && crm == 1: // WYMD: restore Y
		c.dcp.y = math.Float64frombits(bits)
	case opc == 0 && crm == 2: // WEFD
		c.dcp.flags = uint32(bits)
	case opc == 1 && crm == 0: // WXUP
		c.dcp.x = math.Float64frombits(bits)
		c.dcp.y = 0
		c.dcp.format = dcpDouble
	case opc == 1 && crm == 1: // WYUP
		c.dcp.y = math.Float64frombits(bits)
		c.dcp.format = dcpDouble
		c.dcp.haveResult = false
	case opc == 1 && crm == 2: // WXYU: X = float(Rt), Y = float(Rt2)
		c.dcp.x = float64(math.Float32frombits(c.R[rt]))
		c.dcp.y = float64(math.Float32frombits(c.R[rt2]))
		c.dcp.format = dcpFloat
		c.dcp.haveResult = false
	case opc == 2 && crm == 0: // WXMS (mantissa, ignored)
	case opc == 3 && crm == 0: // WXMO: double multiply operands
		c.dcp.op = dcpMul
		c.dcp.format = dcpDouble
	case opc == 4 && crm == 0: // WXDD: double divide operands
		c.dcp.op = dcpDiv
		c.dcp.format = dcpDouble
	case opc == 5 && crm == 0: // WXDQ: double square root
		c.dcp.op = dcpSqrt
		c.dcp.format = dcpDouble
	case opc == 6 && crm == 0: // WXUC
		c.dcp.x = float64(c.R[rt])
		c.dcp.y = 0
		c.dcp.format = dcpUint
		c.dcp.haveResult = false
	case opc == 7 && crm == 0: // WXIC
		c.dcp.x = float64(int32(c.R[rt]))
		c.dcp.y = 0
		c.dcp.format = dcpInt
		c.dcp.haveResult = false
	case opc == 8 && crm == 0: // WXDC: double for conversion
		c.dcp.x = math.Float64frombits(bits)
		c.dcp.y = 0
		c.dcp.format = dcpDouble
		c.dcp.haveResult = false
	case opc == 9 && crm == 2: // WXFC: float for conversion
		c.dcp.x = float64(math.Float32frombits(c.R[rt]))
		c.dcp.y = 0
		c.dcp.format = dcpFloat
		c.dcp.haveResult = false
	case opc == 10 && crm == 0: // WXFM: float multiply
		c.dcp.op = dcpMul
		c.dcp.format = dcpFloat
	case opc == 11 && crm == 0: // WXFD: float divide
		c.dcp.op = dcpDiv
		c.dcp.format = dcpFloat
	case opc == 12 && crm == 0: // WXFQ: float square root
		c.dcp.op = dcpSqrt
		c.dcp.format = dcpFloat
	default:
		c.Halt("unimplemented DCP MCRR")
	}
}

func (c *CPU) dcpMRRC(hw1, hw2 uint16) {
	opc := (hw2 >> 4) & 0xF
	crm := hw2 & 0xF
	rt := uint32(hw2 >> 12)
	rt2 := uint32(hw1 & 0xF)
	switch crm {
	case 8: // PXMD: read the live X
		put64(c, rt, rt2, math.Float64bits(c.dcp.x))
	case 9: // PYMD: read the live Y
		put64(c, rt, rt2, math.Float64bits(c.dcp.y))
	case 10: // PEFD: engaged/result word
		put64(c, rt, rt2, uint64(c.dcp.flags))
	case 0: // RDDA/RDDS/RDDM/RDDD/RDDQ/RDDG
		c.finish()
		put64(c, rt, rt2, math.Float64bits(c.dcp.result))
	case 1, 2, 4, 5: // mantissa/intermediate reads used by canned sequences
		put64(c, rt, rt2, 0)
	case 3, 6, 7:
		put64(c, rt, rt2, 0)
	default:
		_ = opc
		c.Halt("unimplemented DCP MRRC")
	}
}

func (c *CPU) dcpMRC(hw1, hw2 uint16) {
	opc2 := (hw2 >> 5) & 7
	crm := hw2 & 0xF
	rt := uint32(hw2 >> 12)
	switch {
	case crm == 0 && opc2 == 1: // RCMP
		c.dcpCompare()
		if rt == 15 {
			c.APSR = (c.APSR &^ 0xF8000000) | (c.dcp.flags & 0xF8000000)
			return
		}
		c.R[rt] = c.dcp.flags
	case crm == 0 && opc2 == 0: // RXVD: classify float operand
		c.dcpClassify()
		if rt != 15 {
			c.R[rt] = c.dcp.class
		}
	case crm == 2: // RDFA/RDFS/RDFM/RDFD/RDFQ/RDFG
		c.finish()
		if rt != 15 {
			c.R[rt] = math.Float32bits(float32(c.dcp.result))
		}
	case crm == 3: // RDIC/RDUC
		c.finish()
		switch c.dcp.convert {
		case dcpConvertTruncate:
			c.dcp.result = math.Trunc(c.dcp.result)
		case dcpConvertRound:
			c.dcp.result = math.Round(c.dcp.result)
		}
		if rt != 15 {
			if opc2 == 0 {
				c.R[rt] = dcpToInt32(c.dcp.result)
			} else {
				c.R[rt] = dcpToUint32(c.dcp.result)
			}
		}
	default:
		c.Halt("unimplemented DCP MRC")
	}
}

func (c *CPU) dcpCDP(hw1, hw2 uint16) {
	opc1 := (hw1 >> 4) & 0xF
	crm := hw2 & 0xF
	opc2 := (hw2 >> 5) & 7
	switch {
	case opc1 == 0 && crm == 0 && opc2 == 0: // INIT
		c.dcp.x, c.dcp.y, c.dcp.result = 0, 0, 0
		c.dcp.op = dcpNone
		c.dcp.convert = dcpConvertNone
	case opc1 == 0 && crm == 1 && opc2 == 0: // ADD0 (prepare)
		c.dcp.op = dcpAdd
	case opc1 == 1 && crm == 1 && opc2 == 0: // ADD1
		c.dcp.op = dcpAdd
		c.finish()
	case opc1 == 1 && crm == 1 && opc2 == 1: // SUB1
		c.dcp.op = dcpSub
		c.finish()
	case opc1 == 2 && crm == 1 && opc2 == 0: // SQR0
		c.dcp.op = dcpSqrt
	case opc1 == 8 && crm == 0 && opc2 == 2: // NTDC
		c.finish()
		c.dcp.convert = dcpConvertTruncate
	case opc1 == 8 && crm == 0 && opc2 == 3: // NRDC
		c.finish()
		c.dcp.convert = dcpConvertRound
	case opc1 == 8: // NORM/NRDF/NRDD
		if (crm == 2 && opc2 == 1) || (crm == 0 && opc2 == 1) {
			c.finish()
		}
	default:
		c.Halt("unimplemented DCP CDP")
	}
}

// finish computes the pending operation once and stores the result.
func (c *CPU) finish() {
	switch c.dcp.op {
	case dcpAdd:
		c.dcp.result = c.dcp.x + c.dcp.y
		if c.dcp.format == dcpFloat {
			c.dcp.result = float64(float32(c.dcp.result))
		}
	case dcpSub:
		c.dcp.result = c.dcp.x - c.dcp.y
		if c.dcp.format == dcpFloat {
			c.dcp.result = float64(float32(c.dcp.result))
		}
	case dcpMul, dcpDiv, dcpSqrt:
		switch c.dcp.op {
		case dcpMul:
			c.dcp.result = c.dcp.x * c.dcp.y
		case dcpDiv:
			c.dcp.result = c.dcp.x / c.dcp.y
		case dcpSqrt:
			c.dcp.result = math.Sqrt(c.dcp.x)
		}
		if c.dcp.format == dcpFloat {
			c.dcp.result = float64(float32(c.dcp.result))
		}
	}
	if c.dcp.op == dcpNone {
		if c.dcp.haveResult {
			return
		}
		c.dcp.result = c.dcp.x
	}
	c.dcp.haveResult = true
	c.logDCP()
	c.dcp.op = dcpNone
}

func (c *CPU) dcpCompare() {
	x, y := c.dcp.x, c.dcp.y
	flags := uint32(0)
	switch {
	case math.IsNaN(x) || math.IsNaN(y):
		flags = fpscrC | fpscrV
	case x == y:
		flags = fpscrZ | fpscrC
	case x < y:
		flags = fpscrN
	default:
		flags = fpscrC
	}
	c.dcp.flags = flags
}

func (c *CPU) dcpClassify() {
	f := c.dcp.x
	if c.dcp.format == dcpFloat {
		f = float64(float32(f))
	}
	switch {
	case math.IsNaN(f):
		c.dcp.class = 1
	case math.IsInf(f, 0):
		c.dcp.class = 2
	case f == 0:
		c.dcp.class = 4
	default:
		c.dcp.class = 0
	}
}

func dcpToInt32(f float64) uint32 {
	switch {
	case math.IsNaN(f):
		return 0
	case f >= 2147483648:
		return 0x7FFFFFFF
	case f < -2147483648:
		return 0x80000000
	default:
		return uint32(int32(f))
	}
}

func dcpToUint32(f float64) uint32 {
	switch {
	case math.IsNaN(f), f <= 0:
		return 0
	case f >= 4294967296:
		return 0xFFFFFFFF
	default:
		return uint32(f)
	}
}

func (c *CPU) logDCP() {
	if c.DCPLog != nil {
		c.DCPLog(c.dcp.format, c.dcp.op, c.dcp.x, c.dcp.y, c.dcp.result)
	}
}

func put64(c *CPU, lo, hi uint32, v uint64) {
	c.R[lo] = uint32(v)
	c.R[hi] = uint32(v >> 32)
}
