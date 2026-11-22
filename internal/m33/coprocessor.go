package m33

import "fmt"

// Coprocessor space: the Cortex-M33 in the RP2350 has an FPU
// (coprocessors 10 and 11), the double-precision coprocessor (DCP) on CP4
// (CP5 is its non-secure instance), and the GPIO coprocessor on CP0.
// Coprocessor 14 carries the debug registers. The redundancy coprocessor is
// CP7 and is not modelled.

const (
	coprocDCP = 4
)

// exec32Coproc decodes MCR/MCRR/MRC/MRRC and the VFP data-processing encodings.
func (c *CPU) exec32Coproc(hw1, hw2 uint16) {
	coproc := uint32(hw2>>8) & 0xF
	switch coproc {
	case 0:
		if h, ok := c.Bus.(interface {
			ExecGPIOCoproc(*CPU, uint16, uint16)
		}); ok {
			h.ExecGPIOCoproc(c, hw1, hw2)
			return
		}
		c.Halt("unimplemented GPIO coprocessor instruction")
		return
	case coprocDCP:
		c.execDCP(hw1, hw2)
		return
	case 10, 11:
		c.execVFP(hw1, hw2)
		return
	}
	c.Halt(fmt.Sprintf("unimplemented coprocessor %d instruction (%04x %04x)", coproc, hw1, hw2))
}

// execVFP executes a VFP instruction (vfp.go).
func (c *CPU) execVFP(hw1, hw2 uint16) { c.vfpExecute(hw1, hw2) }
