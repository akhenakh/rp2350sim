package rp2350

import "github.com/akhenakh/rp2350sim/internal/m33"

// NVIC models the Armv8-M Nested Vectored Interrupt Controller and the
// System Control Block registers TinyGo and the Pico SDK touch.
type NVIC struct {
	chip *Chip

	enabled [2]uint32
	pending [2]uint32
	// active is pending & enabled, maintained on every change so the
	// per-cycle check does not compute it twice.
	active [2]uint32
	prio   [64]byte

	actlr uint32
	vtor  uint32
	aircr uint32
	scr   uint32
	ccr   uint32
	shpr  [12]byte
	shcsr uint32
	cfsr  uint32
	hfsr  uint32
	mmfar uint32
	bfar  uint32

	systickCtrl uint32
	systickLoad uint32
	systickVal  uint32
}

func (n *NVIC) reset() {
	n.enabled = [2]uint32{}
	n.pending = [2]uint32{}
	n.active = [2]uint32{}
	n.prio = [64]byte{}
	n.actlr = 0
	n.vtor = 0
	n.aircr = 0
	n.scr = 0
	n.ccr = 0
	n.shpr = [12]byte{}
	n.shcsr = 0
	n.cfsr = 0
	n.hfsr = 0
	n.systickCtrl = 0
	n.systickLoad = 0
	n.systickVal = 0
	n.syncActive()
}

// syncActive recomputes pending & enabled and mirrors it into the cores, so
// the instruction loop can test a plain bool instead of calling into the
// controller every cycle.
func (n *NVIC) syncActive() {
	n.active[0] = n.pending[0] & n.enabled[0]
	n.active[1] = n.pending[1] & n.enabled[1]
	any := n.active[0]|n.active[1] != 0
	if n.chip.CPU0 != nil {
		n.chip.CPU0.IRQPending = any
	}
	if n.chip.CPU1 != nil {
		n.chip.CPU1.IRQPending = any
	}
}

// Pend marks an external IRQ pending.
func (n *NVIC) Pend(irq int) {
	if irq < 0 || irq >= 64 {
		return
	}
	n.pending[irq/32] |= 1 << uint(irq%32)
	n.syncActive()
	n.wake()
}

// ClearPending clears a pending external IRQ.
func (n *NVIC) ClearPending(irq int) {
	if irq < 0 || irq >= 64 {
		return
	}
	n.pending[irq/32] &^= 1 << uint(irq%32)
	n.syncActive()
}

func (n *NVIC) wake() {
	// Any enabled pending interrupt takes the cores out of WFE/WFI.
	if n.active[0]|n.active[1] != 0 {
		n.chip.CPU0.Sleeping = false
		n.chip.CPU0.SleepOnEvent = false
		n.chip.CPU1.Sleeping = false
		n.chip.CPU1.SleepOnEvent = false
	}
}

// PeekPending reports the highest-priority enabled pending IRQ without
// clearing it. It is used for diagnostics only.
func (n *NVIC) PeekPending() int {
	return n.bestPending(false)
}

// NextPending implements m33.Interrupts. Taking an exception clears its pending
// bit, as the core does in hardware.
func (n *NVIC) NextPending() int {
	return n.bestPending(true)
}

func (n *NVIC) bestPending(clear bool) int {
	// Fast path: no enabled pending interrupt. This is the common case and it
	// runs on every cycle.
	if n.active[0]|n.active[1] == 0 {
		return 0
	}
	cpu := n.chip.currentCPU()
	if cpu == nil {
		return 0
	}
	if cpu.PRIMASK&1 != 0 {
		return 0
	}
	best := -1
	bestPrio := 256
	for irq := range 64 {
		if n.pending[irq/32]&(1<<uint(irq%32)) == 0 {
			continue
		}
		if n.enabled[irq/32]&(1<<uint(irq%32)) == 0 {
			continue
		}
		p := int(n.prio[irq])
		if best < 0 || p < bestPrio || (p == bestPrio && irq < best) {
			best = irq
			bestPrio = p
		}
	}
	if best < 0 {
		return 0
	}
	if clear {
		n.pending[best/32] &^= 1 << uint(best%32)
		n.syncActive()
		n.chip.IRQCount++
	}
	return 16 + best
}

func (n *NVIC) read(off uint32) uint32 {
	switch {
	case off == 0x008:
		return n.actlr
	case off == 0x010:
		return n.systickCtrl
	case off == 0x014:
		return n.systickLoad
	case off == 0x018:
		return n.systickVal
	case off == 0x01C:
		return 0x40000000
	case off >= 0x100 && off < 0x108:
		return n.enabled[(off-0x100)/4]
	case off >= 0x180 && off < 0x188:
		return n.enabled[(off-0x180)/4]
	case off >= 0x200 && off < 0x208:
		return n.pending[(off-0x200)/4]
	case off >= 0x280 && off < 0x288:
		return n.pending[(off-0x280)/4]
	case off >= 0x300 && off < 0x308:
		return n.active[(off-0x300)/4]
	case off >= 0x400 && off < 0x400+64:
		i := (off - 0x400) / 4
		var v uint32
		for k := range 4 {
			if int(i)*4+k < len(n.prio) {
				v |= uint32(n.prio[int(i)*4+k]) << (8 * uint(k))
			}
		}
		return v
	case off == 0xD00:
		return 0x410FD210 // Cortex-M33 r0p2
	case off == 0xD04:
		v := uint32(n.chip.CPU0.IPSR)
		return v
	case off == 0xD08:
		return n.vtor
	case off == 0xD0C:
		return n.aircr
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
		return n.shcsr
	case off == 0xD28:
		return n.cfsr
	case off == 0xD2C:
		return n.hfsr
	case off == 0xD34:
		return n.mmfar
	case off == 0xD38:
		return n.bfar
	}
	return 0
}

func (n *NVIC) write(off, v uint32) {
	switch {
	case off == 0x008:
		n.actlr = v
	case off == 0x010:
		n.systickCtrl = v & 0x7
	case off == 0x014:
		n.systickLoad = v & 0xFFFFFF
	case off == 0x018:
		n.systickVal = 0
	case off >= 0x100 && off < 0x108: // ISER
		i := (off - 0x100) / 4
		n.enabled[i] |= v
		n.syncActive()
		n.wake()
	case off >= 0x180 && off < 0x188: // ICER
		i := (off - 0x180) / 4
		n.enabled[i] &^= v
		n.syncActive()
	case off >= 0x200 && off < 0x208: // ISPR
		i := (off - 0x200) / 4
		n.pending[i] |= v
		n.syncActive()
		n.wake()
	case off >= 0x280 && off < 0x288: // ICPR
		i := (off - 0x280) / 4
		n.pending[i] &^= v
		n.syncActive()
	case off >= 0x400 && off < 0x400+64: // IPR
		i := (off - 0x400) / 4
		for k := range 4 {
			if int(i)*4+k < len(n.prio) {
				n.prio[int(i)*4+k] = byte(v >> (8 * uint(k)))
			}
		}
	case off == 0xD04: // ICSR
		if v&(1<<28) != 0 { // PENDSVSET
			n.chip.Logger.Debug("ppb: PENDSVSET ignored")
		}
	case off == 0xD08: // VTOR
		n.vtor = v
		n.chip.CPU0.VTOR = v
		n.chip.CPU1.VTOR = v
	case off == 0xD0C: // AIRCR
		n.aircr = v & 0xFFFF
		if v&(1<<2) != 0 {
			// SYSRESETREQ
			n.chip.requestReset("software", 0)
		}
	case off == 0xD10:
		n.scr = v
	case off == 0xD14:
		n.ccr = v
	case off >= 0xD18 && off < 0xD24:
		i := (off - 0xD18) / 4
		for k := range 4 {
			n.shpr[int(i)*4+k] = byte(v >> (8 * uint(k)))
		}
	case off == 0xD24:
		n.shcsr = v
	case off == 0xD28:
		n.cfsr = v
	case off == 0xD2C:
		n.hfsr = v
	case off == 0xF00: // STIR
		irq := int(v & 0x1FF)
		if irq >= 16 && irq < 16+64 {
			n.Pend(irq - 16)
		}
	}
}

// currentCPU returns the CPU currently executing.
func (c *Chip) currentCPU() *m33.CPU {
	if c.core == 0 {
		return c.CPU0
	}
	return c.CPU1
}
