package m33

import (
	"math"
	"strings"
	"testing"
)

// codeBus places code and data in one flat window so literal pools work.
func codeBus() *testBus { return newTestBusAt(0, 1<<16) }

func put16(t *testing.T, bus *testBus, addr uint32, hws ...uint16) {
	t.Helper()
	for i, h := range hws {
		bus.Write16(addr+uint32(i*2), h)
	}
}

func stepAt(t *testing.T, cpu *CPU, addr uint32) {
	t.Helper()
	cpu.R[15] = addr
	cpu.Step()
}

// TestADCSShiftUsesOldCarry is the QEMU-verified rule: the carry-in for
// ADC/SBC is the APSR.C value before the barrel shifter runs.
func TestADCSShiftUsesOldCarry(t *testing.T) {
	bus := codeBus()
	put16(t, bus, 0, 0xEB51, 0x0042) // adcs.w r0, r1, r2, lsl #1
	c := New(bus)
	c.R[1] = 0
	c.R[2] = 0x80000000 // shifted out carry 1
	c.APSR = 0          // old carry 0
	stepAt(t, c, 0)
	if c.R[0] != 0 {
		t.Fatalf("adcs result = %#x, want 0 (old carry in)", c.R[0])
	}
	if c.flag(flagC) {
		t.Fatal("ADC result must set C from the addition, not the shifter")
	}
}

// TestImmLogicalCarry covers the rotated modified-immediate carry-out, which
// is the immediate's bit 31 (QEMU-verified), not the result's.
func TestImmLogicalCarry(t *testing.T) {
	bus := codeBus()
	put16(t, bus, 0, 0xF011, 0x4000) // ands.w r0, r1, #0x80000000
	c := New(bus)
	c.R[1] = 0x40000000
	c.APSR = 0
	stepAt(t, c, 0)
	if c.R[0] != 0 {
		t.Fatalf("ands result = %#x", c.R[0])
	}
	if !c.flag(flagC) {
		t.Fatal("rotated immediate must set C from the shifter carry")
	}
}

func TestPLDDoesNotWritePC(t *testing.T) {
	for _, hw := range [][2]uint16{
		{0xF890, 0xF004}, // pld [r0, #4]
		{0xF810, 0xF021}, // pld [r0, r1, lsl #2]
		{0xF990, 0xF004}, // pli [r0, #4]
	} {
		bus := codeBus()
		put16(t, bus, 0, hw[0], hw[1])
		c := New(bus)
		c.R[0] = 0x2000
		c.R[1] = 0
		stepAt(t, c, 0)
		if c.Halted {
			t.Fatalf("preload %04x %04x halted: %s", hw[0], hw[1], c.HaltReason)
		}
		if c.R[15] != 4 {
			t.Fatalf("preload wrote PC: %#x", c.R[15])
		}
	}
}

func TestLiteralLoadsSignExtendAndAlign(t *testing.T) {
	// LDRSB literal: byte 0x80 at Align(PC,4).
	bus := codeBus()
	put16(t, bus, 0, 0xF99F, 0x0000) // ldrsb.w r0, [pc, #0]
	bus.Write32(4, 0x00000080)
	c := New(bus)
	stepAt(t, c, 0)
	if c.R[0] != 0xFFFFFF80 {
		t.Fatalf("ldrsb literal = %#x, want 0xffffff80", c.R[0])
	}

	// LDRSH literal at a 2-mod-4 PC: Align(PC,4) is the instruction base + 4.
	bus = codeBus()
	put16(t, bus, 2, 0xF9BF, 0x0004) // ldrsh.w r0, [pc, #4]
	bus.Write16(8, 0x8000)
	c = New(bus)
	stepAt(t, c, 2)
	if c.R[0] != 0xFFFF8000 {
		t.Fatalf("ldrsh literal (misaligned PC) = %#x, want 0xffff8000", c.R[0])
	}

	// VLDR literal at a 2-mod-4 PC must also use Align(PC,4).
	bus = codeBus()
	put16(t, bus, 2, 0xED9F, 0x0A01) // vldr s0, [pc, #4]
	bus.Write32(8, math.Float32bits(1.5))
	c = New(bus)
	stepAt(t, c, 2)
	if got := c.S[0]; got != math.Float32bits(1.5) {
		t.Fatalf("vldr literal = %#x", got)
	}
}

// TinyGo's task assembly writes PSP and switches CONTROL.SPSEL directly. An
// interrupt can preempt before the next instruction, so exception entry must
// see that PSP rather than a stale MSP copy.
func TestExceptionAfterPSPSwitchUsesThreadStack(t *testing.T) {
	bus := codeBus()
	const (
		vtor    = 0x8000
		handler = 0x9000
		msp     = 0x7000
		psp     = 0x6000
	)
	vectorAt(bus, vtor, 11, handler)
	irq := &fakeIRQ{allow: true}
	c := New(bus)
	c.Interrupts = irq
	c.VTOR = vtor
	c.MSP = msp
	c.R[13] = msp
	put16(t, bus, 0, 0xF380, 0x8814) // msr CONTROL, r0
	c.CONTROL = 2
	c.R[13] = psp // tinygo_swapTask wrote PSP and switched CONTROL.SPSEL
	c.R[1] = 0x12345678
	irq.next = 11
	c.IRQPending = true
	c.Step() // preempt before the next instruction

	if c.IPSR != 11 || c.R[15] != handler {
		t.Fatalf("exception did not enter: IPSR=%d PC=%#x", c.IPSR, c.R[15])
	}
	if c.PSP != psp-32 {
		t.Fatalf("PSP = %#x, want frame at %#x", c.PSP, psp-32)
	}
	if c.MSP != msp || c.R[13] != msp {
		t.Fatalf("handler stack changed: MSP=%#x SP=%#x", c.MSP, c.R[13])
	}
	if got := bus.Read32(psp - 32 + 4); got != 0x12345678 {
		t.Fatalf("R1 in PSP frame = %#x, want %#x", got, uint32(0x12345678))
	}
	if got := bus.Read32(msp - 32); got != 0 {
		t.Fatalf("frame unexpectedly written at MSP: %#x", got)
	}
}

func TestWideHintIsNotABranch(t *testing.T) {
	bus := codeBus()
	put16(t, bus, 0, 0xF3AF, 0x8002) // wfe.w
	c := New(bus)
	stepAt(t, c, 0)
	if c.Halted {
		t.Fatalf("wfe.w halted: %s", c.HaltReason)
	}
	if c.R[15] != 4 {
		t.Fatalf("wfe.w branched to %#x", c.R[15])
	}
	if !c.Sleeping || !c.SleepOnEvent {
		t.Fatal("wfe.w did not sleep")
	}
}

func TestUnsupportedDSPHalts(t *testing.T) {
	cases := [][2]uint16{
		{0xF381, 0x0008}, // usat r0, #8, r1
		{0xFA82, 0xF081}, // qadd r0, r1, r2
		{0xFA91, 0x02F0}, // sadd16 r0, r1, r2
		{0xFA81, 0x42F0}, // uadd8 r0, r1, r2
	}
	for _, hw := range cases {
		bus := codeBus()
		put16(t, bus, 0, hw[0], hw[1])
		c := New(bus)
		c.R[1] = 7
		c.R[0] = 0xDEADBEEF
		stepAt(t, c, 0)
		if !c.Halted {
			t.Fatalf("%04x %04x was silently executed", hw[0], hw[1])
		}
		if !strings.Contains(c.HaltReason, "unimplemented") {
			t.Fatalf("halt reason = %q", c.HaltReason)
		}
		if c.R[0] != 0xDEADBEEF {
			t.Fatalf("%04x %04x wrote a destination register: %#x", hw[0], hw[1], c.R[0])
		}
	}
}

func TestCPSFaultmask(t *testing.T) {
	bus := codeBus()
	put16(t, bus, 0, 0xB671) // cpsid f
	c := New(bus)
	stepAt(t, c, 0)
	if c.PRIMASK != 0 || c.FAULTMASK != 1 {
		t.Fatalf("cpsid f: PRIMASK=%d FAULTMASK=%d", c.PRIMASK, c.FAULTMASK)
	}
	put16(t, bus, 0, 0xB672) // cpsid i
	c = New(bus)
	stepAt(t, c, 0)
	if c.PRIMASK != 1 || c.FAULTMASK != 0 {
		t.Fatalf("cpsid i: PRIMASK=%d FAULTMASK=%d", c.PRIMASK, c.FAULTMASK)
	}
	put16(t, bus, 0, 0xB661) // cpsie f
	c = New(bus)
	c.FAULTMASK = 1
	stepAt(t, c, 0)
	if c.FAULTMASK != 0 {
		t.Fatal("cpsie f did not clear FAULTMASK")
	}
}

func TestWFIPendingInterruptDoesNotSleep(t *testing.T) {
	bus := codeBus()
	put16(t, bus, 0, 0xBF30) // wfi
	c := New(bus)
	c.PRIMASK = 1
	c.IRQPending = true
	stepAt(t, c, 0)
	if c.Sleeping {
		t.Fatal("WFI slept with an enabled pending interrupt")
	}

	c = New(bus)
	stepAt(t, c, 0)
	if !c.Sleeping {
		t.Fatal("WFI did not sleep with no pending interrupt")
	}
}

func TestVCVTSaturates(t *testing.T) {
	cases := []struct {
		name string
		hw1  uint16
		in   float32
		want uint32
		ioc  bool
	}{
		{"s32 NaN", 0xEEFD, float32(math.NaN()), 0, true},
		{"s32 +inf", 0xEEFD, float32(math.Inf(1)), 0x7FFFFFFF, true},
		{"s32 -inf", 0xEEFD, float32(math.Inf(-1)), 0x80000000, true},
		{"s32 1e20", 0xEEFD, 1e20, 0x7FFFFFFF, true},
		{"s32 -3.5", 0xEEFD, -3.5, 0xFFFFFFFD, false},
		{"u32 NaN", 0xEEFC, float32(math.NaN()), 0, true},
		{"u32 -1", 0xEEFC, -1, 0, true},
		{"u32 5e9", 0xEEFC, 5e9, 0xFFFFFFFF, true},
		{"u32 3.9", 0xEEFC, 3.9, 3, false},
	}
	for _, tc := range cases {
		bus := codeBus()
		put16(t, bus, 0, tc.hw1, 0x0AC0)
		c := New(bus)
		c.S[0] = math.Float32bits(tc.in)
		stepAt(t, c, 0)
		if c.S[1] != tc.want {
			t.Fatalf("%s = %#x, want %#x", tc.name, c.S[1], tc.want)
		}
		if got := c.FPSCR&fpscrIOC != 0; got != tc.ioc {
			t.Fatalf("%s IOC = %v, want %v", tc.name, got, tc.ioc)
		}
	}
}

func TestExceptionFrameAlignsAndStacksXPSR(t *testing.T) {
	bus := newTestBusAt(0, 1<<16)
	c := New(bus)
	c.VTOR = 0
	bus.Write32(4*15, 0x101) // SysTick vector
	c.MSP = 0x8004           // 4 mod 8, so a padding word is required
	c.IPSR = 0
	c.APSR = flagZ
	c.Exception(15)
	if c.MSP&7 != 0 {
		t.Fatalf("exception frame is not 8-aligned: SP=%#x", c.MSP)
	}
	xpsr := bus.Read32(c.MSP + 28)
	if xpsr&(1<<24) == 0 {
		t.Fatal("stacked xPSR has no Thumb bit")
	}
	if xpsr&0x1FF != 0 {
		t.Fatalf("stacked xPSR IPSR = %d, want the interrupted IPSR 0", xpsr&0x1FF)
	}
	if xpsr&(1<<9) == 0 {
		t.Fatal("stacked xPSR does not record the padding word")
	}
	if c.IPSR != 15 {
		t.Fatalf("IPSR = %d", c.IPSR)
	}
	c.exceptionReturn(excReturnBase | 0x9)
	if c.MSP != 0x8004 {
		t.Fatalf("returned MSP = %#x, want 0x8004", c.MSP)
	}
	if c.IPSR != 0 {
		t.Fatalf("returned IPSR = %d", c.IPSR)
	}
}

func TestExceptionClearsMonitor(t *testing.T) {
	bus := newTestBusAt(0, 1<<16)
	c := New(bus)
	bus.Write32(4*15, 0x101)
	c.loadExclusive(0x400, 0, 4)
	if !c.monitorValid {
		t.Fatal("monitor not set by LDREX")
	}
	c.EventRegister = false
	c.Exception(15)
	if c.monitorValid {
		t.Fatal("exception entry left the exclusive monitor armed")
	}
	if c.EventRegister {
		t.Fatal("exception entry spuriously signaled an exclusive-loss event")
	}
	c.monitor, c.monitorValid = 0x400, true
	c.EventRegister = false
	c.exceptionReturn(c.R[14])
	if c.monitorValid || c.EventRegister {
		t.Fatal("exception return must clear the monitor without signaling an event")
	}
}

// TestPostIndexedLdrPcKeepsReturnedSP covers the handler idiom that loads an
// EXC_RETURN with a post-indexed PC load: the writeback must happen before
// exception return replaces SP, or R13 is clobbered with the handler's MSP.
func TestPostIndexedLdrPcKeepsReturnedSP(t *testing.T) {
	bus := codeBus()
	put16(t, bus, 0, 0xF85D, 0xFB04) // ldr.w pc, [sp], #4
	c := New(bus)
	c.IPSR = 11
	c.MSP = 0x8000
	bus.Write32(0x8000, 0xFFFFFFFD) // thread, PSP
	c.PSP = 0x6000
	c.R[13] = c.MSP
	bus.Write32(0x6000+24, 0x1235) // PC with Thumb bit
	bus.Write32(0x6000+28, 0x01000000)
	stepAt(t, c, 0)
	if t.Failed() {
		return
	}
	if c.IPSR != 0 || c.CONTROL&2 == 0 {
		t.Fatalf("IPSR=%d CONTROL=%d", c.IPSR, c.CONTROL)
	}
	if c.R[15] != 0x1234 {
		t.Fatalf("PC = %#x", c.R[15])
	}
	if c.R[13] != 0x6020 || c.PSP != 0x6020 {
		t.Fatalf("SP=%#x PSP=%#x, want 0x6020", c.R[13], c.PSP)
	}
	if c.MSP != 0x8004 {
		t.Fatalf("MSP = %#x, want the post-indexed 0x8004", c.MSP)
	}
}
