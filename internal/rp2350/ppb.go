package rp2350

import "github.com/akhenakh/rp2350sim/internal/m33"

// Exception numbers of the Armv8-M system exceptions modelled here.
const (
	excNMI       = 2
	excHardFault = 3
	excSVCall    = 11
	excPendSV    = 14
	excSysTick   = 15

	// mpuRegions is the Cortex-M33 MPU region count reported through MPU_TYPE
	// (the RP2350 exposes eight regions).
	mpuRegions = 8

	// nvicIRQs is the number of implemented RP2350 interrupt lines.
	nvicIRQs = 52
)

// irqMask has the implemented IRQ bits for each enabled/pending word.
var irqMask = [2]uint32{0xFFFFFFFF, (1 << (nvicIRQs - 32)) - 1}

// NVIC models one core's private System Control Space: the NVIC, the System
// Control Block and the SysTick timer. On the RP2350 the whole 0xE000E000 page
// is banked per core, so VTOR, enable/pending state, priorities, SysTick and
// the active-exception bookkeeping are independent.
type NVIC struct {
	chip *Chip
	core int

	enabled [2]uint32
	pending [2]uint32
	// activeIRQ is the IABR view: external exceptions that entered and have
	// not returned yet.
	activeIRQ [2]uint32
	prio      [64]byte

	actlr uint32
	aircr uint32
	scr   uint32
	ccr   uint32
	shpr  [12]byte
	shcsr uint32
	cfsr  uint32
	hfsr  uint32
	mmfar uint32
	bfar  uint32

	cpacr  uint32
	fpdscr uint32

	mpuCtrl uint32
	mpuRnr  uint32
	mpuRbar [mpuRegions]uint32
	mpuRlar [mpuRegions]uint32
	mpuMair [2]uint32

	sauCtrl uint32
	sauRnr  uint32
	sauRbar [mpuRegions]uint32
	sauRlar [mpuRegions]uint32

	// System exceptions are pended through ICSR or the SysTick timer.
	pendSV  bool
	pendST  bool
	pendNMI bool

	// activeSys has a bit per active system exception 0..15, and activeCount
	// tracks the nesting depth for ICSR.RETTOBASE.
	activeSys   uint16
	activeCount int

	tick sysTickBlock
}

func (n *NVIC) reset() {
	n.enabled = [2]uint32{}
	n.pending = [2]uint32{}
	n.activeIRQ = [2]uint32{}
	n.prio = [64]byte{}
	n.actlr = 0
	n.aircr = 0
	n.scr = 0
	n.ccr = 0x201 // RES1 bits 9 and 0, no unaligned trap
	n.shpr = [12]byte{}
	n.shcsr = 0
	n.cfsr = 0
	n.hfsr = 0
	n.mmfar = 0
	n.bfar = 0
	n.cpacr = 0
	n.fpdscr = 0
	n.mpuCtrl = 0
	n.mpuRnr = 0
	n.mpuRbar = [mpuRegions]uint32{}
	n.mpuRlar = [mpuRegions]uint32{}
	n.mpuMair = [2]uint32{}
	n.sauCtrl = 0
	n.sauRnr = 0
	n.sauRbar = [mpuRegions]uint32{}
	n.sauRlar = [mpuRegions]uint32{}
	n.pendSV = false
	n.pendST = false
	n.pendNMI = false
	n.activeSys = 0
	n.activeCount = 0
	if cpu := n.cpu(); cpu != nil {
		cpu.SleepOnExit = false
	}
	n.tick.reset()
	n.syncActive()
}

// cpu returns the core this PPB belongs to, or nil during construction.
func (n *NVIC) cpu() *m33.CPU {
	if n.chip == nil {
		return nil
	}
	if n.core == 0 {
		return n.chip.CPU0
	}
	return n.chip.CPU1
}

// hasPending reports an enabled external IRQ or a pending system exception.
func (n *NVIC) hasPending() bool {
	return (n.pending[0]&n.enabled[0])|(n.pending[1]&n.enabled[1]) != 0 ||
		n.pendNMI || n.pendSV || n.pendST
}

// syncActive mirrors the pending-and-enabled state into the core so the
// instruction loop can test a plain bool instead of calling the controller
// every cycle.
func (n *NVIC) syncActive() {
	if cpu := n.cpu(); cpu != nil {
		cpu.IRQPending = n.hasPending()
	}
}

// wake takes this core out of WFI/WFE when an enabled exception is pending.
func (n *NVIC) wake() {
	cpu := n.cpu()
	if cpu == nil {
		return
	}
	if n.hasPending() {
		cpu.Sleeping = false
		cpu.SleepOnEvent = false
	}
}

// noteEvent implements SCR.SEVONPEND: a transition to pending signals an event
// even when the exception is disabled or masked.
func (n *NVIC) noteEvent() {
	if n.scr&(1<<4) == 0 {
		return
	}
	cpu := n.cpu()
	if cpu == nil {
		return
	}
	cpu.EventRegister = true
	if cpu.SleepOnEvent {
		cpu.Sleeping = false
		cpu.SleepOnEvent = false
	}
}

// Pend marks an external IRQ pending on this core.
func (n *NVIC) Pend(irq int) {
	if irq < 0 || irq >= nvicIRQs {
		return
	}
	if n.pending[irq/32]&(1<<uint(irq%32)) != 0 {
		return
	}
	n.pending[irq/32] |= 1 << uint(irq%32)
	n.noteEvent()
	n.syncActive()
	n.wake()
}

// ClearPending clears a pending external IRQ.
func (n *NVIC) ClearPending(irq int) {
	if irq < 0 || irq >= nvicIRQs {
		return
	}
	n.pending[irq/32] &^= 1 << uint(irq%32)
	n.syncActive()
}

// pendSystem pends a system exception (NMI, PendSV or SysTick).
func (n *NVIC) pendSystem(exc uint32) {
	switch exc {
	case excNMI:
		if n.pendNMI {
			return
		}
		n.pendNMI = true
	case excPendSV:
		if n.pendSV {
			return
		}
		n.pendSV = true
	case excSysTick:
		if n.pendST {
			return
		}
		n.pendST = true
	default:
		return
	}
	n.noteEvent()
	n.syncActive()
	n.wake()
}

// PeekPending reports the highest-priority enabled pending exception without
// taking it. It is used for diagnostics only.
func (n *NVIC) PeekPending() int {
	return int(n.bestPending())
}

// exceptionPriority is the raw (un-grouped) priority of an exception. Lower
// numbers are more urgent.
func (n *NVIC) exceptionPriority(exc uint32) int {
	switch exc {
	case excNMI:
		return -2
	case excHardFault:
		return -1
	}
	if exc >= 4 && exc <= 15 {
		return int(n.shpr[exc-4] & 0xF0)
	}
	if irq := int(exc) - 16; irq >= 0 && irq < 64 {
		return int(n.prio[irq] & 0xF0)
	}
	return 0
}

// groupPriority applies AIRCR.PRIGROUP to the four implemented priority bits.
// Values above 3 cannot be represented by four priority bits and are treated as
// 3, the deterministic no-preemption configuration.
func (n *NVIC) groupPriority(p int) int {
	if p < 0 {
		return p
	}
	group := min((n.aircr>>8)&7, 3)
	switch group {
	case 0:
		return p & 0xF0
	case 1:
		return p & 0xE0
	case 2:
		return p & 0xC0
	default:
		return p & 0x80
	}
}

// executionPriority is the group priority against which a pending exception
// must compare to preempt, derived from the executing exception and the
// PRIMASK/FAULTMASK/BASEPRI masks.
func (n *NVIC) executionPriority(cpu *m33.CPU) int {
	p := 256 // thread mode
	if cpu.IPSR != 0 {
		p = n.groupPriority(n.exceptionPriority(cpu.IPSR))
	}
	if cpu.PRIMASK&1 != 0 {
		p = min(p, 0)
	}
	if cpu.BASEPRI != 0 {
		p = min(p, n.groupPriority(int(cpu.BASEPRI&0xF0)))
	}
	if cpu.FAULTMASK&1 != 0 {
		p = min(p, -1)
	}
	return p
}

// bestPending returns the highest-urgency pending exception number, ignoring
// the execution priority. Ties go to the lower exception number.
func (n *NVIC) bestPending() uint32 {
	var best uint32
	bestPrio := 1 << 20
	consider := func(exc uint32, prio int) {
		if best == 0 || prio < bestPrio || (prio == bestPrio && exc < best) {
			best, bestPrio = exc, prio
		}
	}
	if n.pendNMI {
		consider(excNMI, n.exceptionPriority(excNMI))
	}
	if n.pendSV {
		consider(excPendSV, n.exceptionPriority(excPendSV))
	}
	if n.pendST {
		consider(excSysTick, n.exceptionPriority(excSysTick))
	}
	for i := range nvicIRQs {
		if n.pending[i/32]&n.enabled[i/32]&(1<<uint(i%32)) == 0 {
			continue
		}
		consider(uint32(16+i), n.exceptionPriority(uint32(16+i)))
	}
	return best
}

func (n *NVIC) clearPendingExc(exc uint32) {
	switch exc {
	case excNMI:
		n.pendNMI = false
	case excPendSV:
		n.pendSV = false
	case excSysTick:
		n.pendST = false
	default:
		if irq := int(exc) - 16; irq >= 0 && irq < 64 {
			n.pending[irq/32] &^= 1 << uint(irq%32)
		}
	}
	n.syncActive()
}

// NextPending implements m33.Interrupts: it returns the pending exception that
// may preempt cpu and clears its pending bit.
func (n *NVIC) NextPending(cpu *m33.CPU) int {
	if cpu == nil {
		return 0
	}
	exec := n.executionPriority(cpu)
	// PRIMASK/FAULTMASK (and a zero BASEPRI threshold) block everything at
	// group priority 0 or below; only NMI can still preempt.
	if !n.pendNMI && exec <= 0 {
		return 0
	}
	exc := n.bestPending()
	if exc == 0 {
		return 0
	}
	if n.groupPriority(n.exceptionPriority(exc)) >= exec {
		return 0
	}
	n.clearPendingExc(exc)
	n.chip.IRQCount++
	return int(exc)
}

// ExceptionEntered records an exception as active.
func (n *NVIC) ExceptionEntered(exc uint32) {
	n.activeCount++
	if exc < 16 {
		n.activeSys |= 1 << exc
		return
	}
	if irq := int(exc) - 16; irq >= 0 && irq < 64 {
		n.activeIRQ[irq/32] |= 1 << uint(irq%32)
	}
}

// ReturnException deactivates an exception left through EXC_RETURN.
func (n *NVIC) ReturnException(exc uint32) {
	if n.activeCount > 0 {
		n.activeCount--
	}
	if exc < 16 {
		n.activeSys &^= 1 << exc
		return
	}
	if irq := int(exc) - 16; irq >= 0 && irq < 64 {
		n.activeIRQ[irq/32] &^= 1 << uint(irq%32)
	}
}

// CanTakeSync reports whether the synchronous exception exc may be taken at
// cpu's effective priority.
func (n *NVIC) CanTakeSync(cpu *m33.CPU, exc uint32) bool {
	return n.groupPriority(n.exceptionPriority(exc)) < n.executionPriority(cpu)
}

func (n *NVIC) icsr(cpu *m33.CPU) uint32 {
	v := cpu.IPSR & 0x1FF
	if n.pendNMI {
		v |= 1 << 31
	}
	if n.pendSV {
		v |= 1 << 28
	}
	if n.pendST {
		v |= 1 << 26
	}
	if best := n.bestPending(); best != 0 {
		v |= (best & 0x1FF) << 12
	}
	if (n.pending[0]&n.enabled[0])|(n.pending[1]&n.enabled[1]) != 0 {
		v |= 1 << 22 // ISRPENDING
	}
	if cpu.IPSR != 0 && n.activeCount <= 1 {
		v |= 1 << 11 // RETTOBASE
	}
	return v
}

// peek returns a register value without read side effects, for byte/halfword
// read-modify-write accesses. In particular a narrow write to SYST_CSR must
// not clear COUNTFLAG through a helper read.
func (n *NVIC) peek(off uint32) uint32 {
	if off == 0x010 {
		return n.tick.ctrl
	}
	return n.read(off)
}

func (n *NVIC) read(off uint32) uint32 {
	cpu := n.cpu()
	switch {
	case off == 0x008:
		return n.actlr
	case off == 0x010: // SYST_CSR
		n.tick.sync(n.chip.Cycles, n)
		v := n.tick.ctrl
		n.tick.ctrl &^= 1 << 16 // COUNTFLAG clear on read
		return v
	case off == 0x014: // SYST_RVR
		return n.tick.load
	case off == 0x018: // SYST_CVR
		n.tick.sync(n.chip.Cycles, n)
		return n.tick.value(n.chip.Cycles)
	case off == 0x01C: // 10 ms at the 1 MHz reference clock, no skew
		return 100000
	case off >= 0x100 && off < 0x108:
		i := (off - 0x100) / 4
		return n.enabled[i] & irqMask[i]
	case off >= 0x180 && off < 0x188:
		i := (off - 0x180) / 4
		return n.enabled[i] & irqMask[i]
	case off >= 0x200 && off < 0x208:
		i := (off - 0x200) / 4
		return n.pending[i] & irqMask[i]
	case off >= 0x280 && off < 0x288:
		i := (off - 0x280) / 4
		return n.pending[i] & irqMask[i]
	case off >= 0x300 && off < 0x308:
		return n.activeIRQ[(off-0x300)/4]
	case off >= 0x400 && off < 0x400+64:
		i := (off - 0x400) / 4
		var v uint32
		for k := range 4 {
			if irq := int(i)*4 + k; irq < nvicIRQs && irq < len(n.prio) {
				v |= uint32(n.prio[irq]) << (8 * uint(k))
			}
		}
		return v
	case off == 0xD00:
		return 0x410FD210 // Cortex-M33: ARM, variant 1, part 0xD21, revision 0
	case off == 0xD04:
		if cpu == nil {
			return 0
		}
		return n.icsr(cpu)
	case off == 0xD08:
		if cpu == nil {
			return 0
		}
		return cpu.VTOR
	case off == 0xD0C:
		return 0xFA050000 | n.aircr
	case off == 0xD10:
		return n.scr
	case off == 0xD14:
		return n.ccr
	case off >= 0xD18 && off < 0xD24:
		i := (off - 0xD18) / 4
		var v uint32
		for k := range 4 {
			v |= uint32(n.shpr[int(i)*4+k]) << (8 * uint(k))
		}
		return v
	case off == 0xD24:
		// SHCSR: report the active system exceptions alongside the stored
		// enable/pending bits.
		v := n.shcsr
		if n.activeSys&(1<<excNMI) != 0 {
			v |= 1 << 5 // NMIACT
		}
		if n.activeSys&(1<<excHardFault) != 0 {
			v |= 1 << 2 // HARDFAULTACT
		}
		if n.activeSys&(1<<excSVCall) != 0 {
			v |= 1 << 7 // SVCALLACT
		}
		if n.activeSys&(1<<excPendSV) != 0 {
			v |= 1 << 10 // PENDSVACT
		}
		if n.activeSys&(1<<excSysTick) != 0 {
			v |= 1 << 11 // SYSTICKACT
		}
		return v
	case off == 0xD28:
		return n.cfsr
	case off == 0xD2C:
		return n.hfsr
	case off == 0xD34:
		return n.mmfar
	case off == 0xD38:
		return n.bfar
	case off == 0xD88:
		return n.cpacr
	case off == 0xD90: // MPU_TYPE
		return mpuRegions << 8
	case off == 0xD94:
		return n.mpuCtrl
	case off == 0xD98:
		return n.mpuRnr
	case off == 0xD9C:
		return n.mpuRbar[n.mpuRnr%mpuRegions]
	case off == 0xDA0:
		return n.mpuRlar[n.mpuRnr%mpuRegions]
	case off >= 0xDA4 && off < 0xDC0: // MPU_RBAR_A1..A3, RLAR_A1..A3
		i := (off - 0xDA4) / 4
		if (off-0xDA4)&4 == 0 {
			return n.mpuRbar[(n.mpuRnr+i)%mpuRegions]
		}
		return n.mpuRlar[(n.mpuRnr+i)%mpuRegions]
	case off == 0xDC0 || off == 0xDC4:
		return n.mpuMair[(off-0xDC0)/4]
	case off == 0xDD0:
		return n.sauCtrl
	case off == 0xDD4:
		return mpuRegions
	case off == 0xDD8:
		return n.sauRnr
	case off == 0xDDC:
		return n.sauRbar[n.sauRnr%mpuRegions]
	case off == 0xDE0:
		return n.sauRlar[n.sauRnr%mpuRegions]
	case off == 0xF34: // FPCCR
		if cpu == nil {
			return 0
		}
		return cpu.FPCCR
	case off == 0xF38: // FPCAR
		if cpu == nil {
			return 0
		}
		return cpu.FPCAR
	case off == 0xF3C: // FPDSCR
		return n.fpdscr
	}
	return 0
}

func (n *NVIC) write(off, v uint32) {
	cpu := n.cpu()
	now := n.chip.Cycles
	switch {
	case off == 0x008:
		n.actlr = v
	case off == 0x010: // SYST_CSR
		n.tick.writeCSR(v, now, n)
		n.syncActive()
		n.wake()
	case off == 0x014: // SYST_RVR
		n.tick.writeRVR(v)
	case off == 0x018: // SYST_CVR
		n.tick.writeCVR(now, n)
	case off >= 0x100 && off < 0x108: // ISER
		i := (off - 0x100) / 4
		n.enabled[i] |= v & irqMask[i]
		n.syncActive()
		n.wake()
	case off >= 0x180 && off < 0x188: // ICER
		i := (off - 0x180) / 4
		n.enabled[i] &^= v & irqMask[i]
		n.syncActive()
	case off >= 0x200 && off < 0x208: // ISPR
		i := (off - 0x200) / 4
		v &= irqMask[i]
		newbits := v &^ n.pending[i]
		n.pending[i] |= v
		if newbits != 0 {
			n.noteEvent()
		}
		n.syncActive()
		n.wake()
	case off >= 0x280 && off < 0x288: // ICPR
		i := (off - 0x280) / 4
		n.pending[i] &^= v & irqMask[i]
		n.syncActive()
	case off >= 0x400 && off < 0x400+64: // IPR
		i := (off - 0x400) / 4
		for k := range 4 {
			if irq := int(i)*4 + k; irq < nvicIRQs && irq < len(n.prio) {
				// Only the four top priority bits are implemented.
				n.prio[irq] = byte(v>>(8*uint(k))) & 0xF0
			}
		}
	case off == 0xD04: // ICSR
		if v&(1<<31) != 0 { // NMIPENDSET
			n.pendSystem(excNMI)
		}
		if v&(1<<30) != 0 { // PENDNMICLR
			n.pendNMI = false
			n.syncActive()
		}
		if v&(1<<28) != 0 { // PENDSVSET
			n.pendSystem(excPendSV)
		}
		if v&(1<<27) != 0 { // PENDSVCLR
			n.pendSV = false
			n.syncActive()
		}
		if v&(1<<26) != 0 { // PENDSTSET
			n.pendSystem(excSysTick)
		}
		if v&(1<<25) != 0 { // PENDSTCLR
			n.pendST = false
			n.syncActive()
		}
	case off == 0xD08: // VTOR is banked per core
		if cpu != nil {
			cpu.VTOR = v &^ 0x7F
		}
	case off == 0xD0C: // AIRCR
		if v>>16 != 0x05FA {
			return
		}
		n.aircr = v & 0x0000_6F08
		if group := (n.aircr >> 8) & 7; group > 3 {
			n.aircr &^= 0x0000_0700
			n.aircr |= 3 << 8
		}
		if v&(1<<2) != 0 {
			n.chip.resetCore(n.core)
		}
	case off == 0xD10:
		n.scr = v & 0x1E
		if cpu != nil {
			cpu.SleepOnExit = n.scr&(1<<1) != 0
		}
	case off == 0xD14:
		n.ccr = (v & 0x0000051A) | 0x201
	case off >= 0xD18 && off < 0xD24: // SHPR
		i := (off - 0xD18) / 4
		for k := range 4 {
			// Only the four top priority bits are implemented.
			n.shpr[int(i)*4+k] = byte(v>>(8*uint(k))) & 0xF0
		}
	case off == 0xD24:
		n.shcsr = v
	case off == 0xD28:
		n.cfsr = v
	case off == 0xD2C:
		n.hfsr = v
	case off == 0xD34:
		n.mmfar = v
	case off == 0xD38:
		n.bfar = v
	case off == 0xD88: // CPACR
		n.cpacr = v & 0x00F0_FFFF
	case off == 0xD94:
		n.mpuCtrl = v & 7
	case off == 0xD98:
		n.mpuRnr = v & 7
	case off == 0xD9C:
		n.mpuRbar[n.mpuRnr%mpuRegions] = v
	case off == 0xDA0:
		n.mpuRlar[n.mpuRnr%mpuRegions] = v & 0xFFFFFFEF
	case off >= 0xDA4 && off < 0xDC0:
		i := (off - 0xDA4) / 4
		if (off-0xDA4)&4 == 0 {
			n.mpuRbar[(n.mpuRnr+i)%mpuRegions] = v
		} else {
			n.mpuRlar[(n.mpuRnr+i)%mpuRegions] = v & 0xFFFFFFEF
		}
	case off == 0xDC0 || off == 0xDC4:
		n.mpuMair[(off-0xDC0)/4] = v
	case off == 0xDD0:
		n.sauCtrl = v & 3
	case off == 0xDD8:
		n.sauRnr = v & 0xFF
	case off == 0xDDC:
		n.sauRbar[n.sauRnr%mpuRegions] = v & 0xFFFFFFE0
	case off == 0xDE0:
		n.sauRlar[n.sauRnr%mpuRegions] = v & 0xFFFFFFE3
	case off == 0xF00: // STIR: v is an interrupt number
		n.Pend(int(v & 0x1FF))
	case off == 0xF34: // FPCCR
		if cpu != nil {
			cpu.SetFPCCR(v)
		}
	case off == 0xF38: // FPCAR: location for lazy stacking
		if cpu != nil {
			cpu.SetFPCAR(v)
		}
	case off == 0xF3C: // FPDSCR
		n.fpdscr = v & 0x0700_0000
	}
}
