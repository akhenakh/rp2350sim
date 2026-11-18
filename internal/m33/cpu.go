// Package m33 implements an ARMv8-M Mainline (Cortex-M33) interpreter.
//
// The implementation covers the integer instruction set used by TinyGo and the
// Raspberry Pi Pico SDK: Thumb-2, DSP multiply/divide, exclusives, exception
// entry/return with a 16-entry system exception model and external interrupts
// from the NVIC. Floating point, MVE and TrustZone isolation are out of scope.
package m33

import "fmt"

// Bus is the memory interface the CPU uses for instruction and data access.
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, v uint8)
	Write16(addr uint32, v uint16)
	Write32(addr uint32, v uint32)
}

// Interrupts is implemented by the interrupt controller (NVIC) the CPU is
// attached to. NextPending returns the exception number that should preempt the
// current execution, or 0 when nothing is pending.
type Interrupts interface {
	NextPending() int
}

const (
	flagN uint32 = 1 << 31
	flagZ uint32 = 1 << 30
	flagC uint32 = 1 << 29
	flagV uint32 = 1 << 28
	flagQ uint32 = 1 << 27

	excReturnBase uint32 = 0xFFFFFFF0
)

// CPU is a single Cortex-M33 core.
type CPU struct {
	R [16]uint32 // R13=SP, R14=LR, R15=PC
	// APSR holds N,Z,C,V,Q in their architectural bit positions.
	APSR uint32
	// IPSR is the current exception number (0 in thread mode).
	IPSR uint32
	// PRIMASK disables configurable-priority interrupts when bit 0 is set.
	PRIMASK uint32
	// itNoFlags suppresses flag updates from 16-bit data-processing
	// instructions executing inside an IT block (ARMv8-M rule).
	itNoFlags bool

	// CONTROL bit 1 selects PSP in thread mode.
	CONTROL uint32
	// MSP and PSP are the banked stack pointers.
	MSP uint32
	PSP uint32

	Bus        Bus
	Interrupts Interrupts

	// Cycles counts retired instructions (a coarse cycle model).
	Cycles uint64
	// Sleeping is set by WFE/WFI and cleared when an event or interrupt arrives.
	Sleeping bool
	// SleepOnEvent marks a WFE sleep, which SEV may wake. WFI sleeps only
	// end on an interrupt.
	SleepOnEvent bool
	// Parked keeps a core stopped until it is explicitly started (core 1
	// bootrom wait loop).
	Parked bool
	// Halted is set when the core executes a terminal condition (UDF, lockup).
	Halted bool
	// HaltReason describes why the core halted.
	HaltReason string

	// Exception state.
	VTOR  uint32
	excLR uint32
	curPC uint32

	// Exclusive-access monitor (one address per core).
	monitor      uint32
	monitorValid bool

	itstate uint8

	// Unimplemented is called for instructions the core does not decode.
	// It may return false to ignore the instruction (advance past it).
	Unimplemented func(pc uint32, hw1, hw2 uint16) bool

	// EventRegister models the SEV event register: WFE clears it if set.
	EventRegister bool

	// TracePC, when set, is called before each instruction executes.
	TracePC func(c *CPU, pc uint32, hw1 uint16)
}

// New returns a CPU with the reset vector taken from the bus.
func New(bus Bus) *CPU {
	c := &CPU{Bus: bus}
	c.Reset()
	return c
}

// Reset performs a architectural reset using the vector table at VTOR (0).
func (c *CPU) Reset() {
	for i := range c.R {
		c.R[i] = 0
	}
	c.APSR = 0
	c.IPSR = 0
	c.PRIMASK = 0
	c.CONTROL = 0
	c.MSP = 0
	c.PSP = 0
	c.itstate = 0
	c.Sleeping = false
	c.SleepOnEvent = false
	c.Halted = false
	c.Cycles = 0
}

// SetVectorTable points the core at a vector table whose first two words are
// the initial stack pointer and the reset handler.
func (c *CPU) SetVectorTable(addr uint32) {
	c.VTOR = addr
	c.MSP = c.Bus.Read32(addr)
	c.R[13] = c.MSP
	c.R[15] = c.Bus.Read32(addr+4) &^ 1
}

// Flag helpers.

func (c *CPU) flag(f uint32) bool { return c.APSR&f != 0 }

func (c *CPU) setFlag(f uint32, v bool) {
	if v {
		c.APSR |= f
	} else {
		c.APSR &^= f
	}
}

func (c *CPU) setNZ(v uint32) {
	if c.itNoFlags {
		return
	}
	c.setFlag(flagN, v&0x80000000 != 0)
	c.setFlag(flagZ, v == 0)
}

// setNZForced updates N/Z even inside an IT block. CMP, CMN, TST and TEQ are
// the only 16-bit instructions whose flags are not suppressed there.
func (c *CPU) setNZForced(v uint32) {
	saved := c.itNoFlags
	c.itNoFlags = false
	c.setNZ(v)
	c.itNoFlags = saved
}

func (c *CPU) nzcv() (n, z, cy, v bool) {
	return c.flag(flagN), c.flag(flagZ), c.flag(flagC), c.flag(flagV)
}

func (c *CPU) setNZCV(n, z, cy, v bool) {
	c.setFlag(flagN, n)
	c.setFlag(flagZ, z)
	c.setFlag(flagC, cy)
	c.setFlag(flagV, v)
}

// pcRead is the value of PC seen by instructions reading R15.
func (c *CPU) pcRead() uint32 { return c.curPC + 4 }

// reg returns a register, with PC reads adjusted for the pipeline.
func (c *CPU) reg(i uint32) uint32 {
	if i == 15 {
		return c.pcRead()
	}
	return c.R[i]
}

func (c *CPU) setReg(i, v uint32) {
	if i == 15 {
		c.branchTo(v)
		return
	}
	c.R[i] = v
}

// branchTo sets PC from an interworking address, stripping the Thumb bit and
// handling exception return when the special EXC_RETURN values are used.
func (c *CPU) branchTo(addr uint32) {
	if addr&0xF0000000 == 0xF0000000 {
		// The stack pointer may have moved during this instruction (pop, for
		// example), and the exception frame is read through the banked copy.
		c.syncSP()
		c.exceptionReturn(addr)
		return
	}
	c.R[15] = addr &^ 1
}

func (c *CPU) branch(offset int32) {
	c.R[15] = uint32(int32(c.R[15]) + offset)
}

// Step executes one instruction and returns the number of cycles consumed.
func (c *CPU) Step() int {
	if c.Halted {
		return 0
	}
	if c.Sleeping {
		return 1
	}
	if c.Parked {
		return 0
	}
	if c.itstate == 0 && c.Interrupts != nil && c.IPSR == 0 {
		if exc := c.Interrupts.NextPending(); exc != 0 {
			c.Exception(uint32(exc))
			return 12
		}
	}

	pc := c.R[15]
	c.curPC = pc
	hw1 := c.Bus.Read16(pc)
	if c.TracePC != nil {
		c.TracePC(c, pc, hw1)
	}

	itBefore := c.itstate

	// Instruction condition (IT block).
	if c.itstate != 0 {
		cond := c.itstate >> 4
		if !c.condTrue(cond) {
			// Skip the instruction entirely.
			if hw1 >= 0xE800 {
				c.R[15] = pc + 4
			} else {
				c.R[15] = pc + 2
			}
			c.advanceIT()
			c.Cycles++
			return 1
		}
	}

	c.Cycles++
	if hw1 >= 0xE800 {
		c.exec32(pc, hw1)
	} else {
		c.R[15] = pc + 2
		c.itNoFlags = itBefore != 0
		c.exec16(pc, hw1)
		c.itNoFlags = false
	}
	c.syncSP()
	if itBefore != 0 {
		c.advanceIT()
	}
	return 1
}

// syncSP keeps the banked stack pointer in step with R13, which is the register
// instructions actually modify. Exception entry/exit and CONTROL writes read
// MSP/PSP directly, so those copies must not go stale.
func (c *CPU) syncSP() {
	if c.IPSR == 0 && c.CONTROL&2 != 0 {
		c.PSP = c.R[13]
	} else {
		c.MSP = c.R[13]
	}
}

func (c *CPU) advanceIT() {
	if c.itstate == 0 {
		return
	}
	if c.itstate&0x7 == 0 {
		c.itstate = 0
	} else {
		c.itstate = (c.itstate & 0xE0) | ((c.itstate << 1) & 0x1F)
	}
}

func (c *CPU) condTrue(cond uint8) bool {
	n, z, cy, v := c.nzcv()
	switch cond {
	case 0: // EQ
		return z
	case 1: // NE
		return !z
	case 2: // CS
		return cy
	case 3: // CC
		return !cy
	case 4: // MI
		return n
	case 5: // PL
		return !n
	case 6: // VS
		return v
	case 7: // VC
		return !v
	case 8: // HI
		return cy && !z
	case 9: // LS
		return !cy || z
	case 10: // GE
		return n == v
	case 11: // LT
		return n != v
	case 12: // GT
		return !z && n == v
	case 13: // LE
		return z || n != v
	case 14, 15: // AL
		return true
	}
	return false
}

// Memory helpers with alignment rules matching the architecture.

func (c *CPU) read16(addr uint32) uint16     { return c.Bus.Read16(addr) }
func (c *CPU) read32(addr uint32) uint32     { return c.Bus.Read32(addr) }
func (c *CPU) write16(addr uint32, v uint16) { c.Bus.Write16(addr, v) }
func (c *CPU) write32(addr uint32, v uint32) { c.Bus.Write32(addr, v) }

// Exception enters handler mode for the given exception number.
func (c *CPU) Exception(n uint32) {
	usePSP := c.IPSR == 0 && c.CONTROL&2 != 0
	sp := c.MSP
	if usePSP {
		sp = c.PSP
	}
	sp -= 32
	c.Bus.Write32(sp+0, c.R[0])
	c.Bus.Write32(sp+4, c.R[1])
	c.Bus.Write32(sp+8, c.R[2])
	c.Bus.Write32(sp+12, c.R[3])
	c.Bus.Write32(sp+16, c.R[12])
	c.Bus.Write32(sp+20, c.R[14])
	c.Bus.Write32(sp+24, c.R[15])
	c.Bus.Write32(sp+28, c.APSR)
	if usePSP {
		c.PSP = sp
	} else {
		c.MSP = sp
	}
	// Handler mode always uses MSP.
	c.R[13] = c.MSP
	// EXC_RETURN: thread/handler, MSP/PSP, no FP state.
	if usePSP {
		c.R[14] = excReturnBase | 0xD
	} else {
		c.R[14] = excReturnBase | 0x9
	}
	c.IPSR = n
	c.APSR &^= flagQ
	c.itstate = 0
	// PC is loaded from the vector table, with the Thumb bit stripped.
	c.R[15] = c.Bus.Read32(c.VectorBase()+4*n) &^ 1
}

// VectorBase returns the active vector table base derived from VTOR.
func (c *CPU) VectorBase() uint32 { return c.VTOR &^ 0x7F }

func (c *CPU) exceptionReturn(excReturn uint32) {
	if c.IPSR == 0 {
		c.Halt("exception return in thread mode")
		return
	}
	toThread := excReturn&0x8 != 0
	usePSP := excReturn&0x4 != 0
	sp := c.MSP
	if usePSP {
		sp = c.PSP
	}
	c.R[0] = c.Bus.Read32(sp + 0)
	c.R[1] = c.Bus.Read32(sp + 4)
	c.R[2] = c.Bus.Read32(sp + 8)
	c.R[3] = c.Bus.Read32(sp + 12)
	c.R[12] = c.Bus.Read32(sp + 16)
	c.R[14] = c.Bus.Read32(sp + 20)
	pc := c.Bus.Read32(sp + 24)
	c.APSR = (c.APSR &^ 0xF8000000) | (c.Bus.Read32(sp+28) & 0xF8000000)
	sp += 32
	if usePSP {
		c.PSP = sp
	} else {
		c.MSP = sp
	}
	if toThread {
		c.IPSR = 0
		if usePSP {
			c.CONTROL |= 2
		} else {
			c.CONTROL &^= 2
		}
		c.R[13] = sp
	} else {
		c.R[13] = c.MSP
	}
	c.R[15] = pc &^ 1
	_ = excReturn
}

// Halt stops the core with a diagnostic reason.
func (c *CPU) Halt(reason string) {
	if c.Halted {
		return
	}
	c.Halted = true
	c.HaltReason = reason
}

// DescribeRegister returns a human-readable register dump, used by diagnostics.
func (c *CPU) DescribeRegister(i int) string {
	if i < 13 {
		return fmt.Sprintf("r%d=%#08x", i, c.R[i])
	}
	switch i {
	case 13:
		return fmt.Sprintf("sp=%#08x", c.R[13])
	case 14:
		return fmt.Sprintf("lr=%#08x", c.R[14])
	case 15:
		return fmt.Sprintf("pc=%#08x", c.R[15])
	}
	return "?"
}

// UserMode reports whether the core is in thread mode.
func (c *CPU) UserMode() bool { return c.IPSR == 0 }

// setControl updates CONTROL, banking the stack pointer when SPSEL changes in
// thread mode.
func (c *CPU) setControl(v uint32) {
	newSPSEL := v&2 != 0
	oldSPSEL := c.CONTROL&2 != 0
	if c.IPSR == 0 && newSPSEL != oldSPSEL {
		if oldSPSEL {
			c.PSP = c.R[13]
			c.R[13] = c.MSP
		} else {
			c.MSP = c.R[13]
			c.R[13] = c.PSP
		}
	}
	c.CONTROL = v & 3
}
