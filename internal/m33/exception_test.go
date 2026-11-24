package m33

import "testing"

// fakeIRQ is a minimal controller: the tests drive exception entry directly
// and only check the entry/return notifications.
type fakeIRQ struct {
	allow    bool
	next     uint32
	entered  []uint32
	returned []uint32
}

func (f *fakeIRQ) NextPending(*CPU) int { e := f.next; f.next = 0; return int(e) }
func (f *fakeIRQ) ExceptionEntered(exc uint32) {
	f.entered = append(f.entered, exc)
}
func (f *fakeIRQ) ReturnException(exc uint32) {
	f.returned = append(f.returned, exc)
}
func (f *fakeIRQ) CanTakeSync(*CPU, uint32) bool { return f.allow }

// vectorAt installs a vector table that maps exception n to handler.
func vectorAt(bus *testBus, vtor, n, handler uint32) {
	bus.Write32(vtor+4*n, handler|1)
}

// TestSVCFromThreadUsesPSP checks the Armv8-M SVC entry: the caller frame goes
// on the thread stack (PSP), the handler runs on MSP and EXC_RETURN says
// thread/PSP.
func TestSVCFromThreadUsesPSP(t *testing.T) {
	bus := codeBus()
	const (
		vtor    = 0x8000
		handler = 0x9000
		msp     = 0x7000
		psp     = 0x6000
	)
	vectorAt(bus, vtor, 11, handler)
	put16(t, bus, 0, 0xDF00) // svc #0
	put16(t, bus, handler, 0xE7FE)

	irq := &fakeIRQ{allow: true}
	c := New(bus)
	c.Interrupts = irq
	c.VTOR = vtor
	c.MSP = msp
	c.PSP = psp
	c.R[13] = psp
	c.CONTROL = 2
	c.R[0] = 0x1234
	c.R[12] = 0x5678

	stepAt(t, c, 0)

	if c.IPSR != 11 {
		t.Fatalf("IPSR = %d, want 11", c.IPSR)
	}
	if c.R[15] != handler {
		t.Fatalf("PC = %#x, want handler %#x", c.R[15], handler)
	}
	if c.R[14] != 0xFFFFFFFD {
		t.Fatalf("EXC_RETURN = %#x, want 0xfffffffd", c.R[14])
	}
	if c.MSP != msp {
		t.Fatalf("handler SP = %#x, want %#x", c.MSP, msp)
	}
	if c.PSP != psp-32 {
		t.Fatalf("PSP = %#x, want %#x", c.PSP, psp-32)
	}
	if got := bus.Read32(psp - 32); got != 0x1234 {
		t.Fatalf("stacked R0 = %#x", got)
	}
	if got := bus.Read32(psp - 32 + 16); got != 0x5678 {
		t.Fatalf("stacked R12 = %#x", got)
	}
	if got := bus.Read32(psp - 32 + 28); got&0x1FF != 0 || got&(1<<24) == 0 {
		t.Fatalf("stacked xPSR = %#x, want Thumb and IPSR 0", got)
	}
	if len(irq.entered) != 1 || irq.entered[0] != 11 {
		t.Fatalf("entered = %v, want [11]", irq.entered)
	}

	c.exceptionReturn(0xFFFFFFFD)
	if c.IPSR != 0 || c.CONTROL&2 == 0 || c.R[13] != psp {
		t.Fatalf("return: IPSR=%d CONTROL=%d SP=%#x", c.IPSR, c.CONTROL, c.R[13])
	}
	if c.R[0] != 0x1234 || c.R[12] != 0x5678 {
		t.Fatalf("return restored R0=%#x R12=%#x", c.R[0], c.R[12])
	}
	if len(irq.returned) != 1 || irq.returned[0] != 11 {
		t.Fatalf("returned = %v, want [11]", irq.returned)
	}
}

// TestNestedExceptionRestoresHandlerIPSR checks that a higher-priority
// exception preempting a handler stacks the interrupted IPSR and that the
// return goes back to handler mode rather than to the thread.
func TestNestedExceptionRestoresHandlerIPSR(t *testing.T) {
	bus := codeBus()
	const vtor = 0x8000
	vectorAt(bus, vtor, 16, 0x9100)
	vectorAt(bus, vtor, 17, 0x9200)
	irq := &fakeIRQ{allow: true}
	c := New(bus)
	c.Interrupts = irq
	c.VTOR = vtor
	c.MSP = 0x7000
	c.R[0] = 0x41
	c.R[13] = c.MSP

	c.Exception(16)
	outerFrame := c.MSP
	c.R[0] = 0x42
	c.Exception(17)
	if c.R[14] != 0xFFFFFFF1 {
		t.Fatalf("nested EXC_RETURN = %#x, want 0xfffffff1", c.R[14])
	}
	if got := bus.Read32(c.MSP + 28); got&0x1FF != 16 {
		t.Fatalf("nested frame IPSR = %d, want 16", got&0x1FF)
	}
	c.exceptionReturn(0xFFFFFFF1)
	if c.IPSR != 16 {
		t.Fatalf("returned IPSR = %d, want 16", c.IPSR)
	}
	if c.R[13] != outerFrame {
		t.Fatalf("handler SP = %#x, want %#x", c.R[13], outerFrame)
	}
	if c.R[0] != 0x42 {
		t.Fatalf("handler R0 = %#x, want 0x42", c.R[0])
	}
	c.exceptionReturn(0xFFFFFFF9)
	if c.IPSR != 0 || c.R[13] != 0x7000 || c.R[0] != 0x41 {
		t.Fatalf("thread return: IPSR=%d SP=%#x R0=%#x", c.IPSR, c.R[13], c.R[0])
	}
	wantEntered := []uint32{16, 17}
	if len(irq.entered) != 2 || irq.entered[0] != wantEntered[0] || irq.entered[1] != wantEntered[1] {
		t.Fatalf("entered = %v", irq.entered)
	}
	if len(irq.returned) != 2 || irq.returned[0] != 17 || irq.returned[1] != 16 {
		t.Fatalf("returned = %v", irq.returned)
	}
}

// TestSVCBlockedEscalatesToHardFault covers the Armv8-M rule that an SVC that
// cannot be taken (PRIMASK/BASEPRI or equal priority) becomes a HardFault.
func TestSVCBlockedEscalatesToHardFault(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 3, 0x9000)
	put16(t, bus, 0, 0xDF00)
	irq := &fakeIRQ{allow: false}
	c := New(bus)
	c.Interrupts = irq
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.PRIMASK = 1
	stepAt(t, c, 0)
	if c.IPSR != 3 {
		t.Fatalf("IPSR = %d, want HardFault 3", c.IPSR)
	}
	if len(irq.entered) != 1 || irq.entered[0] != 3 {
		t.Fatalf("entered = %v, want [3]", irq.entered)
	}

	// FAULTMASK turns the escalation into lockup.
	c2 := New(bus)
	c2.Interrupts = &fakeIRQ{allow: false}
	c2.VTOR = 0x8000
	c2.MSP = 0x7000
	c2.FAULTMASK = 1
	stepAt(t, c2, 0)
	if !c2.Halted {
		t.Fatal("SVC with FAULTMASK should lock up")
	}
}

// TestSpecialRegisterEncodings checks the architectural MRS/MSR system
// register numbers using encodings emitted by GNU as (objdump-verified).
func TestSpecialRegisterEncodings(t *testing.T) {
	bus := codeBus()
	c := New(bus)
	c.R[3] = 0x40
	put16(t, bus, 0, 0xF383, 0x8811) // msr basepri, r3
	stepAt(t, c, 0)
	if c.BASEPRI != 0x40 {
		t.Fatalf("BASEPRI after msr = %#x", c.BASEPRI)
	}
	put16(t, bus, 0, 0xF3EF, 0x8711) // mrs r7, basepri
	stepAt(t, c, 0)
	if c.R[7] != 0x40 {
		t.Fatalf("mrs basepri = %#x", c.R[7])
	}
	c.R[0] = 1
	put16(t, bus, 0, 0xF380, 0x8813) // msr faultmask, r0
	stepAt(t, c, 0)
	if c.FAULTMASK != 1 {
		t.Fatalf("FAULTMASK after msr = %d", c.FAULTMASK)
	}
	put16(t, bus, 0, 0xF3EF, 0x8713) // mrs r7, faultmask
	stepAt(t, c, 0)
	if c.R[7] != 1 {
		t.Fatalf("mrs faultmask = %d", c.R[7])
	}
}

// TestSVCInsideITBlockStacksITState checks that a conditional SVC stacks the
// advanced IT state: SVC returns to the following instruction, so the next
// instruction must still see its condition.
func TestSVCInsideITBlockStacksITState(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 11, 0x9000)
	put16(t, bus, 0, 0xBF04) // itt eq (two instructions)
	put16(t, bus, 2, 0xDF00) // svceq #0
	put16(t, bus, 4, 0x2001) // moveq r0, #1
	put16(t, bus, 0x9000, 0xE7FE)
	c := New(bus)
	c.Interrupts = &fakeIRQ{allow: true}
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.APSR = flagZ
	stepAt(t, c, 0) // ITT
	if c.itstate != 0x04 {
		t.Fatalf("ITSTATE = %#x", c.itstate)
	}
	c.R[15] = 2
	c.Step() // SVC inside the IT block
	if c.IPSR != 11 {
		t.Fatalf("IPSR = %d", c.IPSR)
	}
	if c.itstate != 0 {
		t.Fatalf("handler ITSTATE = %#x", c.itstate)
	}
	xpsr := bus.Read32(c.MSP + 28)
	if got := uint8((xpsr>>10&0x3F)<<2 | (xpsr>>25)&3); got != 0x08 {
		t.Fatalf("stacked ITSTATE = %#x, want 8 (the next instruction)", got)
	}
	c.exceptionReturn(0xFFFFFFF9)
	if c.itstate != 0x08 {
		t.Fatalf("restored ITSTATE = %#x", c.itstate)
	}
	c.R[0] = 0
	c.R[15] = 4
	c.Step() // moveq under the restored IT state
	if c.R[0] != 1 || c.itstate != 0 {
		t.Fatalf("post-SVC instruction: r0=%#x ITSTATE=%#x", c.R[0], c.itstate)
	}
}

func TestBASEPRIWrites(t *testing.T) {
	c := New(codeBus())
	c.writeSpecialReg(0x11, 0xF5)
	if c.BASEPRI != 0xF0 {
		t.Fatalf("BASEPRI = %#x, want 0xf0", c.BASEPRI)
	}
	c.BASEPRI = 0
	c.writeSpecialReg(0x12, 0x80) // BASEPRI_MAX raises
	if c.BASEPRI != 0x80 {
		t.Fatalf("BASEPRI_MAX first write = %#x", c.BASEPRI)
	}
	c.writeSpecialReg(0x12, 0x40) // higher priority: raises the mask
	if c.BASEPRI != 0x40 {
		t.Fatalf("BASEPRI_MAX did not raise the mask: %#x", c.BASEPRI)
	}
	c.writeSpecialReg(0x12, 0xC0) // lower priority: ignored
	if c.BASEPRI != 0x40 {
		t.Fatalf("BASEPRI_MAX lowered the mask to %#x", c.BASEPRI)
	}
	if c.readSpecialReg(0x12) != 0x40 {
		t.Fatalf("BASEPRI_MAX read = %#x", c.readSpecialReg(0x12))
	}
}

func TestControlKeepsFPCA(t *testing.T) {
	c := New(codeBus())
	c.MSP = 0x7000
	c.PSP = 0x6000
	c.R[13] = c.MSP
	c.writeSpecialReg(0x14, 4) // FPCA only
	if c.CONTROL != 4 || !c.FPUActive {
		t.Fatalf("CONTROL = %d FPUActive=%v", c.CONTROL, c.FPUActive)
	}
	c.writeSpecialReg(0x14, 2|4) // SPSEL + FPCA: switch to PSP
	if c.CONTROL != 6 || c.R[13] != c.PSP || !c.FPUActive {
		t.Fatalf("CONTROL = %d SP=%#x", c.CONTROL, c.R[13])
	}
	if c.readSpecialReg(0x14) != 6 {
		t.Fatalf("MRS CONTROL = %d", c.readSpecialReg(0x14))
	}
}

// TestExtendedFrameLayout checks the architectural FP frame: the basic frame
// is at SP and the FP area follows it at SP+32.
func TestExtendedFrameLayout(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 15, 0x9000)
	c := New(bus)
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.R[0] = 0xCAFE
	c.FPUActive = true
	c.CONTROL = 4
	c.FPCCR = fpcASPN // eager stacking for this layout check
	c.S[0] = 0x3F800000
	c.FPSCR = 0x10

	c.Exception(15)

	if c.MSP != 0x7000-104 {
		t.Fatalf("frame SP = %#x, want %#x", c.MSP, 0x7000-104)
	}
	if got := bus.Read32(c.MSP + 32); got != 0x3F800000 {
		t.Fatalf("S0 after the basic frame = %#x", got)
	}
	if got := bus.Read32(c.MSP + 32 + 64); got != 0x10 {
		t.Fatalf("FPSCR = %#x", got)
	}
	if got := bus.Read32(c.MSP); got != 0xCAFE {
		t.Fatalf("stacked R0 = %#x", got)
	}
	if got := bus.Read32(c.MSP + 28); got&0x1FF != 0 {
		t.Fatalf("stacked xPSR = %#x", got)
	}
	if c.R[14] != 0xFFFFFFE9 {
		t.Fatalf("EXC_RETURN = %#x, want 0xffffffe9", c.R[14])
	}
	if c.FPUActive || c.CONTROL&4 != 0 {
		t.Fatal("handler started with FPCA set")
	}

	c.S[0] = 0
	c.exceptionReturn(0xFFFFFFE9)
	if c.S[0] != 0x3F800000 || c.FPSCR != 0x10 {
		t.Fatalf("restored S0=%#x FPSCR=%#x", c.S[0], c.FPSCR)
	}
	if c.MSP != 0x7000 || c.IPSR != 0 || !c.FPUActive || c.CONTROL&4 == 0 {
		t.Fatalf("return: MSP=%#x IPSR=%d FPUActive=%v", c.MSP, c.IPSR, c.FPUActive)
	}
}

// TestLazyFPStacking checks that with LSPEN the FP frame is reserved but only
// written when the handler first executes an FP instruction.
func TestLazyFPStacking(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 15, 0x9000)
	put16(t, bus, 0x9000, 0xEE30, 0x0A00) // vadd.f32 s0, s0, s0
	c := New(bus)
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.FPUActive = true
	c.CONTROL = 4
	c.S[1] = 0x40490FDB
	c.FPCCR = fpcASPN | fpcLSPEN

	c.Exception(15)
	if c.R[14] != 0xFFFFFFE9 {
		t.Fatalf("EXC_RETURN = %#x, want extended frame", c.R[14])
	}
	if !c.lazyActive || c.FPCCR&fpcLSPACT == 0 {
		t.Fatal("lazy frame not pending")
	}
	if got := bus.Read32(c.MSP + 32 + 4); got != 0 {
		t.Fatalf("FP frame written before first FP instruction: %#x", got)
	}
	frame := c.MSP

	// The handler's first FP instruction materialises the frame and flips the
	// stacked EXC_RETURN to extended.
	c.Step()
	if c.lazyActive || c.FPCCR&fpcLSPACT != 0 {
		t.Fatal("lazy frame not flushed by the FP instruction")
	}
	if got := bus.Read32(frame + 32 + 4); got != 0x40490FDB {
		t.Fatalf("flushed S1 = %#x", got)
	}
	if c.R[14] != 0xFFFFFFE9 {
		t.Fatalf("EXC_RETURN after flush = %#x, want 0xffffffe9", c.R[14])
	}

	c.S[1] = 0
	c.exceptionReturn(0xFFFFFFE9)
	if c.S[1] != 0x40490FDB {
		t.Fatalf("restored S1 = %#x", c.S[1])
	}
}

// TestLazyFrameSurvivesStaleExcReturn covers the case where the handler
// pushed the original (bit4=1) EXC_RETURN before its first FP instruction:
// the frame record, not the stale LR value, drives the unstack.
func TestLazyFrameSurvivesStaleExcReturn(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 15, 0x9000)
	put16(t, bus, 0x9000, 0xEE30, 0x0A00) // vadd.f32 s0, s0, s0
	c := New(bus)
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.FPUActive = true
	c.CONTROL = 4
	c.S[3] = 0x41200000
	c.FPCCR = fpcASPN | fpcLSPEN

	c.Exception(15)
	saved := c.R[14] // 0xFFFFFFF9, as pushed by a C prologue
	c.Step()         // first FP instruction materialises the frame
	if c.R[14] != 0xFFFFFFE9 {
		t.Fatalf("live EXC_RETURN = %#x after flush", c.R[14])
	}
	// The handler restores the stale copy and returns with it.
	c.R[14] = saved
	c.S[3] = 0
	c.exceptionReturn(saved)
	if c.MSP != 0x7000 {
		t.Fatalf("MSP = %#x, want 0x7000", c.MSP)
	}
	if c.S[3] != 0x41200000 {
		t.Fatalf("S3 = %#x, want the stacked value", c.S[3])
	}
	if !c.FPUActive {
		t.Fatal("FP context not marked live after the return")
	}
}

// TestSyntheticExtendedReturn covers an RTOS first-task launch: the handler
// returns through a task frame it built itself, with no frame record.
func TestSyntheticExtendedReturn(t *testing.T) {
	bus := codeBus()
	c := New(bus)
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.IPSR = 11 // inside a handler returning to a task
	frame := uint32(0x6000)
	c.PSP = frame
	c.Bus.Write32(frame+0, 0x11)        // R0
	c.Bus.Write32(frame+20, 0)          // LR
	c.Bus.Write32(frame+24, 0x20000101) // PC
	c.Bus.Write32(frame+28, 0x01000000) // xPSR
	c.Bus.Write32(frame+32+0, 0xA5)     // S0
	c.Bus.Write32(frame+32+64, 0)       // FPSCR

	c.exceptionReturn(0xFFFFFFED) // thread, PSP, extended
	if c.IPSR != 0 || c.CONTROL&2 == 0 {
		t.Fatalf("IPSR=%d CONTROL=%d", c.IPSR, c.CONTROL)
	}
	if c.R[13] != frame+104 || c.R[0] != 0x11 {
		t.Fatalf("SP=%#x R0=%#x", c.R[13], c.R[0])
	}
	if c.S[0] != 0xA5 || !c.FPUActive {
		t.Fatalf("S0=%#x FPUActive=%v", c.S[0], c.FPUActive)
	}
}

// TestLazyFrameNotUsedReclaimsSpace covers the handler that never touches the
// FPU: the reserved FP frame is released and the thread's FP state stays live.
func TestLazyFrameNotUsedReclaimsSpace(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 15, 0x9000)
	c := New(bus)
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.FPUActive = true
	c.CONTROL = 4
	c.S[2] = 0x3E800000
	c.FPCCR = fpcASPN | fpcLSPEN

	c.Exception(15)
	c.exceptionReturn(0xFFFFFFE9)
	if c.MSP != 0x7000 {
		t.Fatalf("MSP = %#x, want 0x7000", c.MSP)
	}
	if !c.FPUActive || c.CONTROL&4 == 0 {
		t.Fatal("unmaterialised lazy frame lost the live FP state")
	}
	if c.S[2] != 0x3E800000 {
		t.Fatalf("S2 = %#x", c.S[2])
	}
}

// TestFPCCRResetAndFPCAR checks the Cortex-M33 reset FPCCR defaults (ASPEN and
// LSPEN set) and that a lazy entry publishes the FP area through FPCAR.
func TestFPCCRResetAndFPCAR(t *testing.T) {
	c := New(newTestBus())
	if c.FPCCR != fpcASPN|fpcLSPEN {
		t.Fatalf("reset FPCCR = %#x, want 0xc0000000", c.FPCCR)
	}
	bus := codeBus()
	vectorAt(bus, 0x8000, 15, 0x9000)
	put16(t, bus, 0x9000, 0xEE30, 0x0A00) // vadd.f32 s0, s0, s0
	c = New(bus)
	c.VTOR = 0x8000
	c.MSP = 0x7000
	c.R[13] = c.MSP
	c.FPUActive = true
	c.CONTROL = 4
	c.S[0] = 0x3F800000
	c.Exception(15)
	if c.FPCAR != c.MSP+32 {
		t.Fatalf("FPCAR = %#x, want %#x", c.FPCAR, c.MSP+32)
	}
	relocated := c.MSP + 0x200
	c.SetFPCAR(relocated)
	c.Step() // first FP instruction materialises the lazy frame
	if got := bus.Read32(relocated); got != 0x3F800000 {
		t.Fatalf("relocated FP area = %#x, want the pre-instruction S0", got)
	}
}

// TestExtendedFrameWithPadding checks the combined alignment padding and FP
// area layout, which Zephyr's ESF walk depends on.
func TestExtendedFrameWithPadding(t *testing.T) {
	bus := codeBus()
	vectorAt(bus, 0x8000, 15, 0x9000)
	c := New(bus)
	c.VTOR = 0x8000
	c.MSP = 0x7004 // 4 mod 8: a padding word is required
	c.R[13] = c.MSP
	c.FPUActive = true
	c.CONTROL = 4
	c.FPCCR = fpcASPN // eager
	c.S[0] = 0x3F800000
	c.Exception(15)
	// 4 byte pad + 32 basic + 72 FP.
	if c.MSP != 0x7000-104 {
		t.Fatalf("frame SP = %#x, want %#x", c.MSP, 0x7000-104)
	}
	if got := bus.Read32(c.MSP + 32); got != 0x3F800000 {
		t.Fatalf("S0 = %#x", got)
	}
	xpsr := bus.Read32(c.MSP + 28)
	if xpsr&(1<<9) == 0 {
		t.Fatal("stacked xPSR does not record the padding word")
	}
	c.exceptionReturn(0xFFFFFFE9)
	if c.MSP != 0x7004 {
		t.Fatalf("returned MSP = %#x, want 0x7004", c.MSP)
	}
}

// TestFaultmaskSurvivesNMIReturn checks the ARMv8-M rule that a return from
// NMI leaves FAULTMASK alone; other returns clear it.
func TestFaultmaskSurvivesNMIReturn(t *testing.T) {
	mk := func(ipsr uint32) (*CPU, *testBus) {
		bus := codeBus()
		c := New(bus)
		c.MSP = 0x7000
		c.R[13] = c.MSP
		c.IPSR = ipsr
		c.FAULTMASK = 1
		bus.Write32(0x7000+24, 0x1235)
		bus.Write32(0x7000+28, 0x01000000)
		return c, bus
	}
	c, _ := mk(2)
	c.exceptionReturn(0xFFFFFFF9)
	if c.FAULTMASK != 1 {
		t.Fatal("return from NMI cleared FAULTMASK")
	}
	c, _ = mk(15)
	c.exceptionReturn(0xFFFFFFF9)
	if c.FAULTMASK != 0 {
		t.Fatal("return from SysTick did not clear FAULTMASK")
	}
}
