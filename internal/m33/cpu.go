// Package m33 implements an ARMv8-M Mainline (Cortex-M33) interpreter.
//
// The implementation covers the instruction set used by TinyGo and the
// Raspberry Pi Pico SDK: Thumb-2, DSP multiply/divide, exclusives, exception
// entry/return with a 16-entry system exception model, external interrupts
// from the NVIC and single-precision VFP (the RP2350 double-precision
// coprocessor is modelled separately in dcp.go). MVE and TrustZone isolation
// are out of scope.
package m33

import (
	"fmt"
)

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
// attached to. The controller owns the pending bits and the priority
// arbitration; the CPU reports every entry and return so the controller can
// track the active exception state (IABR, RETTOBASE).
type Interrupts interface {
	// NextPending returns the highest-urgency pending exception allowed to
	// preempt cpu, or 0. Taking it clears its pending bit; the CPU reports the
	// entry through ExceptionEntered.
	NextPending(cpu *CPU) int
	// ExceptionEntered records exc as active.
	ExceptionEntered(exc uint32)
	// ReturnException deactivates exc when returning via EXC_RETURN.
	ReturnException(exc uint32)
	// CanTakeSync reports whether the synchronous exception exc may be taken
	// at cpu's current effective priority.
	CanTakeSync(cpu *CPU, exc uint32) bool
}

// FPCCR bits: automatic and lazy FP state preservation, and the status bit
// that marks a reserved but not yet written lazy FP frame.
const (
	fpcLSPACT uint32 = 1 << 0
	fpcLSPEN  uint32 = 1 << 30
	fpcASPN   uint32 = 1 << 31
)

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
	// FAULTMASK blocks every configurable-priority exception, including
	// faults (CPSID f).
	FAULTMASK uint32
	// BASEPRI masks interrupts at or below its priority when non-zero.
	BASEPRI uint32
	// itNoFlags suppresses flag updates from 16-bit data-processing
	// instructions executing inside an IT block (ARMv8-M rule).
	itNoFlags bool

	// VFP state: 32 single-precision registers, the status/control register,
	// and whether the FP context is live (stacked on exception entry).
	S         [32]uint32
	FPSCR     uint32
	FPUActive bool
	dcp       dcpState

	// CONTROL bit 1 selects PSP in thread mode.
	CONTROL uint32
	// MSP and PSP are the banked stack pointers.
	MSP uint32
	PSP uint32
	// MSPLIM and PSPLIM are the Armv8-M stack limit registers. They are stored
	// for the RTOS ports that save and restore them; violations are not
	// modelled.
	MSPLIM uint32
	PSPLIM uint32

	// FPCCR is the FP context control register: ASPEN/LSPEN select automatic
	// and lazy FP stacking. LSPACT marks a reserved lazy frame.
	FPCCR uint32
	// FPCAR locates the FP context area for lazy stacking. It is set on a
	// lazy exception entry and is writable by software.
	FPCAR uint32

	Bus        Bus
	Interrupts Interrupts

	// Cycles counts retired instructions (a coarse cycle model).
	Cycles uint64
	// Sleeping is set by WFE/WFI and cleared when an event or interrupt arrives.
	Sleeping bool
	// SleepOnEvent marks a WFE sleep, which SEV may wake. WFI sleeps only
	// end on an interrupt.
	SleepOnEvent bool
	// SleepOnExit mirrors SCR.SLEEPONEXIT for this core.
	SleepOnExit bool
	// Parked keeps a core stopped until it is explicitly started (core 1
	// bootrom wait loop).
	Parked bool
	// Halted is set when the core executes a terminal condition (UDF, lockup).
	Halted bool
	// HaltReason describes why the core halted.
	HaltReason string

	// Exception state.
	VTOR  uint32
	curPC uint32

	// Exclusive-access monitor (one address per core).
	monitor      uint32
	monitorValid bool
	itstate      uint8

	// Unimplemented is called for instructions the core does not decode.
	// It may return false to ignore the instruction (advance past it).
	Unimplemented func(pc uint32, hw1, hw2 uint16) bool

	// EventRegister models the SEV event register: WFE clears it if set.
	EventRegister bool

	// IRQPending mirrors the controller's pending-and-enabled state so Step
	// can skip the Interrupts call on the common path. The controller keeps
	// it up to date.
	IRQPending bool

	// A lazy FP frame has its space reserved in the exception frame but its
	// data is written when the handler first touches the FPU. lazyActive is
	// set only while the innermost extended frame is still unwritten; nested
	// entries flush it first.
	lazyActive bool
	lazySP     uint32

	// DCPLog, when set, reports each completed double-coprocessor operation.
	DCPLog func(format DCPFormat, op DCPOp, x, y, result float64)

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
	c.FAULTMASK = 0
	c.BASEPRI = 0
	c.CONTROL = 0
	c.MSP = 0
	c.PSP = 0
	c.MSPLIM = 0
	c.PSPLIM = 0
	for i := range c.S {
		c.S[i] = 0
	}
	c.FPSCR = 0
	// Cortex-M33 reset values: automatic and lazy FP preservation enabled.
	c.FPCCR = fpcASPN | fpcLSPEN
	c.FPCAR = 0
	c.FPUActive = false
	c.lazyActive = false
	c.lazySP = 0
	c.dcp = dcpState{}
	c.itstate = 0
	c.IRQPending = false
	c.Sleeping = false
	c.SleepOnEvent = false
	c.SleepOnExit = false
	c.Halted = false
	c.HaltReason = ""
	c.Cycles = 0
	c.VTOR = 0
	c.monitorValid = false
	c.EventRegister = false
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

// pcRead is the value of PC seen by instructions reading R15.
func (c *CPU) pcRead() uint32 { return c.curPC + 4 }

// MonitorCovers reports whether the local exclusive monitor covers addr.
func (c *CPU) MonitorCovers(addr uint32) bool { return c.monitorValid && c.monitor == addr }

// ClearMonitor clears the local exclusive monitor without signaling an event.
// Local stores and exception entry/return use this path.
func (c *CPU) ClearMonitor() { c.monitorValid = false }

// InvalidateMonitor records an exclusive loss caused by another master and
// signals an event, which wakes a WFE sleeper but not a WFI sleeper.
func (c *CPU) InvalidateMonitor() {
	c.ClearMonitor()
	c.EventRegister = true
	if c.SleepOnEvent {
		c.Sleeping = false
		c.SleepOnEvent = false
	}
}

// execHint executes an architectural hint. kind is 0..4 for NOP, YIELD, WFE,
// WFI and SEV; 0xF0..0xFF is the DBG hint (a no-op here).
func (c *CPU) execHint(kind uint8) {
	switch kind {
	case 0: // NOP
	case 1: // YIELD
	case 2: // WFE
		if c.EventRegister {
			c.EventRegister = false
		} else if !c.IRQPending {
			// An enabled interrupt that is already pending is a wakeup
			// event even when PRIMASK keeps it from being taken.
			c.Sleeping = true
			c.SleepOnEvent = true
		}
	case 3: // WFI
		if !c.IRQPending {
			c.Sleeping = true
			c.SleepOnEvent = false
		}
	case 4: // SEV signals an event to every core in the system.
		c.EventRegister = true
		if w, ok := c.Bus.(interface{ SignalEvent() }); ok {
			w.SignalEvent()
		}
	}
}

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
	if addr >= 0xFF000000 {
		// Armv8-M exception return: the stack pointer may have moved during
		// this instruction (pop, for example), and the frame is read through
		// the banked copy.
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
	if c.itstate == 0 && c.IRQPending && c.Interrupts != nil {
		// Task-switch assembly can write PSP and CONTROL.SPSEL between
		// instructions. Synchronize the banked SP before exception entry.
		c.syncSP()
		// The controller arbitrates against the masks and the current IPSR
		// priority, so a higher-priority exception also preempts an active
		// handler and NMI still gets through PRIMASK.
		if exc := c.Interrupts.NextPending(c); exc != 0 {
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

// Exception enters handler mode for the given exception number.
func (c *CPU) Exception(n uint32) {
	vector := c.Bus.Read32(c.VectorBase() + 4*n)
	if vector&1 == 0 {
		c.Halt("INVSTATE: exception vector has Thumb bit clear")
		return
	}
	fromThread := c.IPSR == 0
	usePSP := fromThread && c.CONTROL&2 != 0
	sp := c.MSP
	if usePSP {
		sp = c.PSP
	}
	// A nested exception forces a pending lazy frame to complete: the live FP
	// state belongs to the frame being abandoned, and the nested handler may
	// use the FPU itself.
	c.flushLazy()

	asp := c.FPCCR&fpcASPN != 0
	fp := asp && c.FPUActive
	lazy := fp && fromThread && c.FPCCR&fpcLSPEN != 0

	// Stack frame layout (QEMU-verified, matches Zephyr's hw_frame_fpu):
	// the basic frame is at the lowest address and the FP area follows it.
	// A padding word, recorded in xPSR bit 9, is inserted above the frame
	// when the entry SP is not 8-byte aligned.
	top := sp
	pad := top&4 != 0
	if pad {
		top -= 4
	}
	frameSP := top - 32
	if fp {
		frameSP = top - 104 // basic frame (32) + FP area (72)
	}
	basic := frameSP
	fpBase := basic + 32
	c.Bus.Write32(basic+0, c.R[0])
	c.Bus.Write32(basic+4, c.R[1])
	c.Bus.Write32(basic+8, c.R[2])
	c.Bus.Write32(basic+12, c.R[3])
	c.Bus.Write32(basic+16, c.R[12])
	c.Bus.Write32(basic+20, c.R[14])
	c.Bus.Write32(basic+24, c.R[15])
	xpsr := c.APSR | 1<<24 | c.IPSR&0x1FF
	if c.itstate != 0 {
		// IT[7:2] lives in xPSR[15:10], IT[1:0] in xPSR[26:25].
		xpsr |= uint32(c.itstate>>2)<<10 | uint32(c.itstate&3)<<25
	}
	if pad {
		xpsr |= 1 << 9
	}
	c.Bus.Write32(basic+28, xpsr)
	if usePSP {
		c.PSP = frameSP
	} else {
		c.MSP = frameSP
	}
	if fp && !lazy {
		for i := range 16 {
			c.Bus.Write32(fpBase+uint32(i*4), c.S[i])
		}
		c.Bus.Write32(fpBase+64, c.FPSCR)
		c.Bus.Write32(fpBase+68, 0)
	}
	if lazy {
		// The space is reserved now; the data is written on the handler's
		// first FP instruction. FPCAR points at the FP area (SP+32).
		c.lazyActive = true
		c.lazySP = frameSP
		c.FPCAR = fpBase &^ 7
		c.FPCCR |= fpcLSPACT
	}
	// Handler mode always uses MSP.
	c.R[13] = c.MSP
	// EXC_RETURN: thread/handler, MSP/PSP, and the stack frame type in bit 4
	// (0 = extended frame with FP state, 1 = basic frame without it).
	excReturn := excReturnBase | 0x1
	if fromThread {
		if usePSP {
			excReturn |= 0xD
		} else {
			excReturn |= 0x9
		}
	}
	if fp {
		// An extended frame is declared even when lazy stacking defers the
		// FP data: the space is reserved and the handler must see an extended
		// frame. FPCCR.LSPACT marks the unwritten data.
		excReturn &^= 0x10
	}
	c.R[14] = excReturn
	c.IPSR = n
	c.APSR &^= flagQ
	c.itstate = 0
	c.ClearMonitor()
	// The handler starts with FPCA=0 until it executes an FP instruction.
	c.FPUActive = false
	c.CONTROL &^= 4
	if c.Interrupts != nil {
		c.Interrupts.ExceptionEntered(n)
	}
	// PC is loaded from the validated vector table, with the Thumb bit stripped.
	c.R[15] = vector &^ 1
}

// flushLazy writes the pending lazy FP frame and updates the stacked
// EXC_RETURN to describe an extended frame.
func (c *CPU) flushLazy() {
	if !c.lazyActive {
		return
	}
	base := c.lazySP + 32 // FP area follows the basic frame
	for i := range 16 {
		c.Bus.Write32(base+uint32(i*4), c.S[i])
	}
	c.Bus.Write32(base+64, c.FPSCR)
	c.Bus.Write32(base+68, 0)
	if c.R[14] >= 0xFF000000 {
		// EXC_RETURN still lives in LR: point it at the extended frame.
		c.R[14] &^= 0x10
	}
	c.lazyActive = false
	c.FPCCR &^= fpcLSPACT
}

// SetFPCAR relocates the lazy FP context area. FPCAR is 8-byte aligned and
// points at the first stacked FP register (SP+32 in the extended frame).
func (c *CPU) SetFPCAR(v uint32) {
	c.FPCAR = v &^ 7
	if c.lazyActive {
		if c.FPCAR >= 32 {
			c.lazySP = c.FPCAR - 32
		}
	}
}

// SetFPCCR updates the FP context control register. Software may clear
// LSPACT after taking over the FP save itself (Zephyr's PendSV does), which
// cancels the hardware lazy trigger while keeping the reserved stack space.
func (c *CPU) SetFPCCR(v uint32) {
	c.FPCCR = v & (fpcLSPACT | fpcASPN | fpcLSPEN | 1<<1)
	if c.lazyActive && (c.FPCCR&fpcLSPACT == 0 || c.FPCCR&fpcLSPEN == 0) {
		c.lazyActive = false
	}
}

// VectorBase returns the active vector table base derived from VTOR.
func (c *CPU) VectorBase() uint32 { return c.VTOR &^ 0x7F }

func (c *CPU) exceptionReturn(excReturn uint32) {
	if c.IPSR == 0 {
		c.Halt("exception return in thread mode")
		return
	}
	returning := c.IPSR
	if c.Interrupts != nil {
		c.Interrupts.ReturnException(c.IPSR)
	}
	toThread := excReturn&0x8 != 0
	usePSP := excReturn&0x4 != 0
	sp := c.MSP
	if usePSP {
		sp = c.PSP
	}
	// EXC_RETURN bit 4 declares the layout; lazyActive says whether the
	// innermost extended frame's FP area is still unwritten. A frame that an
	// RTOS built itself (synthetic first-task launch) simply has FP data.
	extended := excReturn&0x10 == 0
	liveFP := extended && c.lazyActive
	restoreFP := extended && !c.lazyActive
	if c.lazyActive {
		c.lazyActive = false
		c.FPCCR &^= fpcLSPACT
	}
	basic := sp
	fpBase := basic + 32
	c.R[0] = c.Bus.Read32(basic + 0)
	c.R[1] = c.Bus.Read32(basic + 4)
	c.R[2] = c.Bus.Read32(basic + 8)
	c.R[3] = c.Bus.Read32(basic + 12)
	c.R[12] = c.Bus.Read32(basic + 16)
	c.R[14] = c.Bus.Read32(basic + 20)
	pc := c.Bus.Read32(basic + 24)
	xpsr := c.Bus.Read32(basic + 28)
	c.APSR = (c.APSR &^ 0xF8000000) | (xpsr & 0xF8000000)
	it := uint8((xpsr>>10&0x3F)<<2 | (xpsr>>25)&3)
	sp = basic + 32
	if extended {
		if restoreFP {
			for i := range 16 {
				c.S[i] = c.Bus.Read32(fpBase + uint32(i*4))
			}
			c.FPSCR = c.Bus.Read32(fpBase + 64)
		}
		sp += 72
	}
	if restoreFP || liveFP {
		// liveFP: the reserved lazy frame was never materialised, so the FP
		// state in the registers is still the interrupted context's.
		c.FPUActive = true
		c.CONTROL |= 4
	} else {
		c.FPUActive = false
		c.CONTROL &^= 4
	}
	if xpsr&(1<<9) != 0 {
		sp += 4 // padding inserted to align the frame
	}
	if usePSP {
		c.PSP = sp
	} else {
		c.MSP = sp
	}
	c.IPSR = xpsr & 0x1FF
	c.itstate = it
	// FAULTMASK is cleared on exception return except when returning from NMI.
	if returning != 2 {
		c.FAULTMASK = 0
	}
	c.ClearMonitor()
	if toThread {
		if usePSP {
			c.CONTROL |= 2
		} else {
			c.CONTROL &^= 2
		}
		c.R[13] = sp
		c.Sleeping = c.SleepOnExit && !c.IRQPending
		c.SleepOnEvent = false
	} else {
		// Returning to a preempted handler: the frame is on MSP.
		c.R[13] = c.MSP
	}
	c.R[15] = pc &^ 1
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
// thread mode. FPCA stays synchronized with the live FP context.
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
	c.CONTROL = v & 7
	c.FPUActive = v&4 != 0
}
