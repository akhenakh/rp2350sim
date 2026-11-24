package rp2350

import "testing"

// excHandler installs a vector table in SRAM and returns the handler address
// reserved for an exception. Handlers contain `bx lr` so a test can return
// with the EXC_RETURN value the core put in LR.
func excHandler(c *Chip, vtor uint32, exc int) uint32 {
	handler := vtor + 0x400 + uint32(exc)*4
	c.Write32(vtor+uint32(exc)*4, handler|1)
	c.Write16(handler, 0x4770) // bx lr
	return handler
}

// trapCore parks core 1 and puts core 0 in a `b .` loop at code.
func trapCore(c *Chip, vtor, code uint32) {
	c.CPU1.Parked = true
	c.Write32(SCSBase+0xD08, vtor) // core 0 VTOR
	c.Write16(code, 0xE7FE)
	c.CPU0.R[15] = code
	c.CPU0.R[13] = 0x20040000
	c.CPU0.MSP = 0x20040000
}

func stepN(c *Chip, n int) {
	for range n {
		c.Step()
	}
}

// TestSysTickCountsAndSetsCountFlag covers the 150 MHz CPU-clock source, the
// COUNTFLAG clear-on-read rule and the TICKINT exception.
func TestSysTickCountsAndSetsCountFlag(t *testing.T) {
	c := New()
	n := c.ppb[0]
	n.write(0x014, 1499) // 1500 cycles
	n.write(0x010, 7)    // ENABLE | TICKINT | CPU clock

	if got := n.read(0x018); got != 1499 {
		t.Fatalf("CVR after enable = %d, want 1499", got)
	}
	c.Cycles += 1499
	n.tickSysTick(c.Cycles)
	if n.pendST {
		t.Fatal("SysTick fired one cycle early")
	}
	c.Cycles++
	n.tickSysTick(c.Cycles)
	if !n.pendST {
		t.Fatal("SysTick did not pend at the wrap")
	}
	if got := n.read(0x010); got&(1<<16) == 0 {
		t.Fatal("COUNTFLAG not set")
	}
	if got := n.read(0x010); got&(1<<16) != 0 {
		t.Fatal("COUNTFLAG was not cleared by the CSR read")
	}
	// The counter reloaded and counts down from RVR.
	c.Cycles += 500
	if got := n.read(0x018); got != 999 {
		t.Fatalf("CVR = %d, want 999", got)
	}

	// A CVR write resets the countdown and clears COUNTFLAG.
	n.write(0x018, 0)
	if got := n.read(0x010); got&(1<<16) != 0 {
		t.Fatal("CVR write did not clear COUNTFLAG")
	}
}

// TestSysTickReferenceClock checks the 1 MHz CLKSOURCE=0 path: one tick every
// 150 CPU cycles.
func TestSysTickReferenceClock(t *testing.T) {
	c := New()
	n := c.ppb[0]
	n.write(0x014, 99)  // 100 ticks of 1 us
	n.write(0x010, 0x3) // ENABLE | TICKINT, reference clock

	c.Cycles += 99 * cyclesPerMicrosecond
	n.tickSysTick(c.Cycles)
	if n.pendST {
		t.Fatal("reference SysTick fired early")
	}
	c.Cycles += cyclesPerMicrosecond
	n.tickSysTick(c.Cycles)
	if !n.pendST {
		t.Fatal("reference SysTick did not fire at 100 us")
	}
}

// TestSysTickWakesIdleSkipExactly checks that the WFI fast-forward lands on the
// SysTick wrap instead of the next microsecond boundary.
func TestSysTickWakesIdleSkipExactly(t *testing.T) {
	c := New()
	const vtor = 0x20000000
	const code = 0x20001000
	trapCore(c, vtor, code)
	excHandler(c, vtor, excSysTick)

	// 777 cycles is not a multiple of the 150-cycle microsecond grid, so an
	// unclamped skip would overshoot.
	c.ppb[0].write(0x014, 776)
	c.ppb[0].write(0x010, 0x7)

	c.Write16(code, 0xBF30) // wfi
	c.Step()                // execute WFI
	if !c.CPU0.Sleeping {
		t.Fatal("WFI did not sleep")
	}
	start := c.Cycles
	c.Run(775)
	if c.Cycles != start+775 || !c.CPU0.Sleeping {
		t.Fatalf("run to %d: Cycles=%d sleeping=%v", start+775, c.Cycles, c.CPU0.Sleeping)
	}
	// 776 is on the 8-cycle alignment grid and 124 cycles before the next
	// microsecond boundary; only the SysTick clamp keeps the jump from
	// overshooting the wrap at start+776.
	if got := c.ppb[0].cyclesUntilSysTick(c.Cycles); got != 1 {
		t.Fatalf("cycles until SysTick = %d, want 1", got)
	}
	c.Run(1)
	if c.Cycles != start+776 {
		t.Fatalf("idle skip ended at %d, want %d", c.Cycles, start+776)
	}
	if c.CPU0.Sleeping {
		t.Fatal("SysTick did not wake the core")
	}
	if !c.ppb[0].pendST {
		t.Fatal("SysTick is not pending after the wake")
	}
	c.Step()
	if c.CPU0.IPSR != excSysTick {
		t.Fatalf("IPSR = %d, want %d", c.CPU0.IPSR, excSysTick)
	}
}

// TestHigherPriorityIRQPreemptsHandler covers nested exception delivery and the
// EXC_RETURN path back to the preempted handler, then a tail-chained PendSV.
func TestHigherPriorityIRQPreemptsHandler(t *testing.T) {
	c := New()
	const vtor = 0x20000000
	const code = 0x20001000
	trapCore(c, vtor, code)
	excHandler(c, vtor, 16)
	excHandler(c, vtor, 17)
	p14 := excHandler(c, vtor, excPendSV)

	// IRQ 1 (exception 17) outranks IRQ 0 (exception 16).
	c.Write8(SCSBase+0x400, 0x80)
	c.Write8(SCSBase+0x404, 0x40)
	c.Write32(SCSBase+0x100, 3) // enable IRQ 0 and 1
	c.NVIC.Pend(0)
	c.Step()
	if c.CPU0.IPSR != 16 {
		t.Fatalf("IPSR = %d, want 16", c.CPU0.IPSR)
	}
	if c.CPU0.R[15] != vtor+0x400+16*4 {
		t.Fatalf("PC = %#x", c.CPU0.R[15])
	}
	// The handler has not run yet; a higher-priority IRQ preempts it.
	c.NVIC.Pend(1)
	c.Step()
	if c.CPU0.IPSR != 17 {
		t.Fatalf("IPSR = %d, want preempting 17", c.CPU0.IPSR)
	}
	if c.CPU0.R[14] != 0xFFFFFFF1 {
		t.Fatalf("nested EXC_RETURN = %#x, want 0xfffffff1", c.CPU0.R[14])
	}
	// bx lr in the IRQ1 handler returns to the IRQ0 handler.
	c.Step()
	if c.CPU0.IPSR != 16 {
		t.Fatalf("IPSR after return = %d, want 16", c.CPU0.IPSR)
	}
	if c.CPU0.R[15] != vtor+0x400+16*4 {
		t.Fatalf("returned PC = %#x", c.CPU0.R[15])
	}

	// PendSV is lower priority than the active IRQ 0 and must tail-chain
	// after the handler returns, before the thread resumes.
	c.Write8(SCSBase+0xD22, 0xE0) // PendSV priority
	c.Write32(SCSBase+0xD04, 1<<28)
	c.Step() // bx lr -> returns to thread
	if c.CPU0.IPSR != 0 {
		t.Fatalf("IPSR after IRQ0 return = %d", c.CPU0.IPSR)
	}
	// PendSV was pended while IRQ0 was active; the next instruction boundary
	// takes it.
	c.Step()
	if c.CPU0.IPSR != excPendSV {
		t.Fatalf("IPSR = %d, want tail-chained PendSV %d", c.CPU0.IPSR, excPendSV)
	}
	if c.CPU0.R[15] != p14 {
		t.Fatalf("PendSV PC = %#x", c.CPU0.R[15])
	}
}

// TestBasepriMasksLowerPriorityIRQ checks BASEPRI critical sections: an IRQ at
// or below the threshold stays pending until BASEPRI is cleared.
func TestBasepriMasksLowerPriorityIRQ(t *testing.T) {
	c := New()
	const vtor = 0x20000000
	const code = 0x20001000
	trapCore(c, vtor, code)
	excHandler(c, vtor, 16)
	c.Write8(SCSBase+0x400, 0x40)
	c.Write32(SCSBase+0x100, 1)
	c.CPU0.BASEPRI = 0x20 // masks priorities >= 0x20
	c.NVIC.Pend(0)
	stepN(c, 4)
	if c.CPU0.IPSR != 0 {
		t.Fatalf("masked IRQ was taken (IPSR %d)", c.CPU0.IPSR)
	}
	c.CPU0.BASEPRI = 0
	c.Step()
	if c.CPU0.IPSR != 16 {
		t.Fatalf("IPSR = %d, want 16 after the mask was cleared", c.CPU0.IPSR)
	}
}

// TestNMIOverridesPrimask checks that NMI is not blocked by PRIMASK. It does
// not lock the core up: exception 2 has a real handler here.
func TestNMIOverridesPrimask(t *testing.T) {
	c := New()
	const vtor = 0x20000000
	const code = 0x20001000
	trapCore(c, vtor, code)
	excHandler(c, vtor, excNMI)
	c.CPU0.PRIMASK = 1
	c.Write32(SCSBase+0xD04, 1<<31) // NMIPENDSET
	c.Step()
	if c.CPU0.IPSR != excNMI {
		t.Fatalf("IPSR = %d, want NMI %d", c.CPU0.IPSR, excNMI)
	}
}

// TestSEVONPENDWakesWFE checks SCR.SEVONPEND: a newly pending, disabled IRQ
// still signals an event and wakes a WFE sleeper with PRIMASK set.
func TestSEVONPENDWakesWFE(t *testing.T) {
	c := New()
	c.Write32(SCSBase+0xD10, 1<<4) // SEVONPEND
	c.CPU0.PRIMASK = 1
	c.CPU0.Sleeping = true
	c.CPU0.SleepOnEvent = true
	c.NVIC.Pend(5) // not enabled
	if c.CPU0.Sleeping {
		t.Fatal("SEVONPEND did not wake the WFE sleeper")
	}
	if !c.CPU0.EventRegister {
		t.Fatal("SEVONPEND did not set the event register")
	}
}

// TestPerCoreSCSIsolation checks that VTOR and the NVIC/SysTick state are
// private per core.
func TestPerCoreSCSIsolation(t *testing.T) {
	c := New()
	c.core = 0
	c.Write32(SCSBase+0xD08, 0x10000000)
	c.Write32(SCSBase+0x100, 1<<3) // core 0 enables IRQ 3
	c.Write32(SCSBase+0x014, 99)
	c.Write32(SCSBase+0x010, 7)

	c.core = 1
	if got := c.Read32(SCSBase + 0xD08); got != 0 {
		t.Fatalf("core 1 VTOR = %#x, want 0", got)
	}
	if got := c.Read32(SCSBase + 0x100); got != 0 {
		t.Fatalf("core 1 ISER = %#x, want 0", got)
	}
	if got := c.Read32(SCSBase + 0x014); got != 0 {
		t.Fatalf("core 1 RVR = %#x, want 0", got)
	}
	// A shared peripheral IRQ reaches both PPBs, independent of enable state.
	c.PendIRQ(3)
	if c.ppb[0].pending[0]&(1<<3) == 0 || c.ppb[1].pending[0]&(1<<3) == 0 {
		t.Fatal("shared IRQ did not fan out to both cores")
	}
	c.core = 0
	if got := c.Read32(SCSBase + 0x100); got != 1<<3 {
		t.Fatalf("core 0 ISER = %#x", got)
	}
}

// TestAIRCRRequiresVectKey checks the VECTKEY gate and PRIGROUP storage.
func TestAIRCRRequiresVectKey(t *testing.T) {
	c := New()
	c.Write32(SCSBase+0xD0C, 0x0000_0500) // no key: ignored
	if c.NVIC.aircr != 0 {
		t.Fatalf("AIRCR write without key accepted (%#x)", c.NVIC.aircr)
	}
	c.Write32(SCSBase+0xD0C, 0x05FA_0500)
	if got := c.Read32(SCSBase + 0xD0C); got != 0xFA05_0500 {
		t.Fatalf("AIRCR = %#x, want 0xfa050500", got)
	}
	// SHPR keeps only the four implemented priority bits.
	c.Write8(SCSBase+0xD23, 0xFF)
	if got := c.NVIC.shpr[11]; got != 0xF0 {
		t.Fatalf("SysTick SHPR = %#x, want 0xf0", got)
	}
}

// TestSIOFIFOAndDoorbellIRQs covers the per-core SIO interrupt lines.
func TestSIOFIFOAndDoorbellIRQs(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.sio.core1Parked = false // core 1 has left the bootrom wait loop

	c.core = 0
	c.Write32(SIOBase+0x54, 0xC0FFEE) // FIFO_WR: core 1 receives
	if c.ppb[0].pending[0]&(1<<sioIRQFIFO) != 0 {
		t.Fatal("FIFO IRQ pended on the sender")
	}
	if c.ppb[1].pending[0]&(1<<sioIRQFIFO) == 0 {
		t.Fatal("FIFO IRQ did not pend on the receiver")
	}
	c.core = 1
	if got := c.Read32(SIOBase + 0x58); got != 0xC0FFEE {
		t.Fatalf("FIFO_RD = %#x", got)
	}
	if c.ppb[1].pending[0]&(1<<sioIRQFIFO) != 0 {
		t.Fatal("FIFO IRQ stayed pending after the queue drained")
	}

	c.core = 0
	c.Write32(SIOBase+0x180, 1) // DOORBELL_OUT_SET -> core 1
	if c.ppb[1].pending[0]&(1<<sioIRQBell) == 0 {
		t.Fatal("doorbell did not pend on the peer")
	}
	c.Write32(SIOBase+0x184, 1) // DOORBELL_OUT_CLR
	if c.ppb[1].pending[0]&(1<<sioIRQBell) != 0 {
		t.Fatal("doorbell clear left the IRQ pending")
	}
}

// TestSIODoorbellStatusAndZeroWrite covers the register map: OUT_* reads the
// peer's input status, IN_* reads own, and SET with zero is a no-op.
func TestSIODoorbellStatusAndZeroWrite(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.core = 0
	c.Write32(SIOBase+0x180, 0) // no doorbell
	if c.ppb[1].pending[0]&(1<<sioIRQBell) != 0 {
		t.Fatal("zero OUT_SET pended a doorbell")
	}
	c.Write32(SIOBase+0x180, 0x2)
	if got := c.Read32(SIOBase + 0x180); got != 0x2 {
		t.Fatalf("OUT_SET status = %#x, want 2", got)
	}
	if got := c.Read32(SIOBase + 0x188); got != 0 {
		t.Fatalf("own IN_SET status = %#x, want 0", got)
	}
	c.core = 1
	if got := c.Read32(SIOBase + 0x188); got != 0x2 {
		t.Fatalf("peer IN_SET status = %#x, want 2", got)
	}
	if got := c.Read32(SIOBase + 0x180); got != 0 {
		t.Fatalf("peer OUT_SET status = %#x, want 0", got)
	}
	c.Write32(SIOBase+0x18C, 0x2) // acknowledge own
	if got := c.Read32(SIOBase + 0x188); got != 0 {
		t.Fatalf("IN_SET after clear = %#x", got)
	}
	if c.ppb[1].pending[0]&(1<<sioIRQBell) != 0 {
		t.Fatal("doorbell IRQ stayed pending after IN_CLR")
	}
}

// TestCore1LaunchInitialisesMSP checks the launch sequence primes MSP as well
// as R13, so an immediate MRS MSP or a pre-first-step exception sees the new
// stack.
func TestCore1LaunchInitialisesMSP(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.core = 0
	words := []uint32{0, 0, 1, 0x20000000, 0x20008000, 0x20000101}
	for _, w := range words {
		c.Write32(SIOBase+0x54, w)
	}
	if c.CPU1.Parked {
		t.Fatal("core 1 is still parked")
	}
	if c.CPU1.MSP != 0x20008000 || c.CPU1.R[13] != 0x20008000 {
		t.Fatalf("MSP=%#x R13=%#x, want 0x20008000", c.CPU1.MSP, c.CPU1.R[13])
	}
}

// TestFPCCRResetAndFPCARWindow checks the PPB view of the per-core FP context
// registers.
func TestFPCCRResetAndFPCARWindow(t *testing.T) {
	c := New()
	c.core = 0
	if got := c.Read32(SCSBase + 0xF34); got != 0xC0000000 {
		t.Fatalf("FPCCR reset = %#x, want 0xc0000000", got)
	}
	c.Write32(SCSBase+0xF38, 0x20001003) // FPCAR is 8-byte aligned
	if got := c.Read32(SCSBase + 0xF38); got != 0x20001000 {
		t.Fatalf("FPCAR = %#x, want 0x20001000", got)
	}
	if c.CPU0.FPCAR != 0x20001000 {
		t.Fatalf("CPU FPCAR = %#x", c.CPU0.FPCAR)
	}
}

// TestSysTickRVRZeroStops covers the ARMv8-M rule that RVR==0 raises
// COUNTFLAG once and stops, rather than wrapping forever.
func TestSysTickRVRZeroStops(t *testing.T) {
	c := New()
	n := c.ppb[0]
	n.write(0x014, 0)
	n.write(0x010, 7) // ENABLE | TICKINT | CPU clock
	c.Cycles = 1
	n.tickSysTick(c.Cycles)
	if got := n.read(0x010); got&sysTickCountFlag == 0 {
		t.Fatal("RVR=0 did not set COUNTFLAG")
	}
	if !n.pendST {
		t.Fatal("RVR=0 did not pend SysTick")
	}
	n.pendST = false
	c.Cycles = 10
	n.tickSysTick(c.Cycles)
	if n.pendST {
		t.Fatal("RVR=0 timer kept wrapping after it stopped")
	}
	if got := n.read(0x010); got&sysTickCountFlag != 0 {
		t.Fatal("RVR=0 timer set COUNTFLAG after it stopped")
	}

	// A CVR write with RVR=0 also stops and leaves CVR at zero.
	n.write(0x018, 0)
	if got := n.read(0x018); got != 0 {
		t.Fatalf("CVR after write with RVR=0 = %d", got)
	}
}

// TestSysTickCVRReadback checks that CVR reads zero until the next clock edge
// reloads RVR.
func TestSysTickCVRReadback(t *testing.T) {
	c := New()
	n := c.ppb[0]
	n.write(0x014, 99)
	n.write(0x010, 7)
	n.write(0x018, 0)
	if got := n.read(0x018); got != 0 {
		t.Fatalf("CVR immediately after write = %d, want 0", got)
	}
	c.Cycles++
	if got := n.read(0x018); got != 99 {
		t.Fatalf("CVR after one tick = %d, want 99", got)
	}
}

// TestSysTickCSRWriteCatchesStall checks that a CSR write after a PSRAM-style
// mid-instruction stall observes a wrap crossed before the write.
func TestSysTickCSRWriteCatchesStall(t *testing.T) {
	c := New()
	n := c.ppb[0]
	n.write(0x014, 99)
	n.write(0x010, 7)
	c.Cycles = 100 // a wrap happened at 100
	n.write(0x010, 7)
	if got := n.read(0x010); got&sysTickCountFlag == 0 {
		t.Fatal("CSR rewrite dropped the stalled wrap")
	}
	if !n.pendST {
		t.Fatal("stalled wrap did not pend SysTick")
	}
}

// TestNVICImplements52IRQs checks that writes to the unimplemented NVIC bits
// (52..63) and priority bytes are ignored, like the RP2350.
func TestNVICImplements52IRQs(t *testing.T) {
	c := New()
	c.NVIC.write(0x104, 1<<20) // ISER word 1, IRQ 52: not implemented
	if c.NVIC.enabled[1] != 0 {
		t.Fatalf("ISER1 enabled unimplemented IRQs: %#x", c.NVIC.enabled[1])
	}
	c.NVIC.write(0x104, 1<<19) // IRQ 51: implemented
	if c.NVIC.enabled[1] != 1<<19 {
		t.Fatalf("IRQ 51 not enabled: %#x", c.NVIC.enabled[1])
	}
	c.NVIC.write(0x204, 1<<20) // ISPR IRQ52
	if c.NVIC.pending[1] != 0 {
		t.Fatalf("ISPR pending unimplemented IRQ: %#x", c.NVIC.pending[1])
	}
	c.NVIC.Pend(60)
	if c.NVIC.pending[1] != 0 {
		t.Fatalf("Pend(60) accepted: %#x", c.NVIC.pending[1])
	}
	c.NVIC.write(0x4D0, 0x80) // IPR for IRQ 52
	if c.NVIC.prio[52] != 0 {
		t.Fatalf("IPR for unimplemented IRQ = %#x", c.NVIC.prio[52])
	}
}

// TestSysTickNarrowCSRWriteKeepsCountFlag checks the byte/halfword
// read-modify-write path does not clear COUNTFLAG through a helper read.
func TestSysTickNarrowCSRWriteKeepsCountFlag(t *testing.T) {
	c := New()
	n := c.ppb[0]
	n.write(0x014, 9)
	n.write(0x010, 7)
	c.Cycles = 10 // wrap
	n.tickSysTick(c.Cycles)
	c.Write8(SCSBase+0x010, 7) // byte RMW of SYST_CSR
	if got := c.Read32(SCSBase + 0x010); got&sysTickCountFlag == 0 {
		t.Fatal("narrow SYST_CSR write cleared COUNTFLAG")
	}
}
