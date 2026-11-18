package m33

import "fmt"

// Coprocessor space: the Cortex-M33 in the RP2350 has a single/double precision
// FPU (coprocessors 10 and 11) and a redundancy coprocessor (CP4) that the Pico
// SDK pokes during startup. Coprocessor 14 carries the debug registers.

const (
	coprocRCP = 4
)

// exec32Coproc decodes MCR/MCRR/MRC/MRRC and the VFP data-processing encodings.
func (c *CPU) exec32Coproc(hw1, hw2 uint16) {
	coproc := uint32(hw2>>8) & 0xF
	switch coproc {
	case coprocRCP:
		// The RCP is only used to seed the canary and read them back. Model it
		// as zeroed state: MRC returns 0, MCR is ignored.
		if hw1&0x10 != 0 { // MRC
			rt := uint32(hw2 >> 12)
			if rt != 15 {
				c.R[rt] = 0
			}
		}
		return
	case 10, 11:
		c.execVFP(hw1, hw2)
		return
	}
	c.Halt(fmt.Sprintf("unimplemented coprocessor %d instruction (%04x %04x)", coproc, hw1, hw2))
}

// execVFP executes a VFP instruction. Implemented in vfp.go.
func (c *CPU) execVFP(hw1, hw2 uint16) {
	c.vfpExecute(hw1, hw2)
}
