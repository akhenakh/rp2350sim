package rp2350

import "slices"

// Debugger is the optional execution control hook used by the desktop UI and
// by tests. It is nil during normal runs, so the interpreter pays only one
// pointer check per instruction.
type Debugger struct {
	Breakpoints map[uint32]bool
	Watches     map[uint32]watchPoint

	Stopped    bool
	StopReason string
	StopPC     uint32
	StopCore   int
	StopCycle  uint64
	// StopAddr is the watched address for a watchpoint stop.
	StopAddr uint32

	skipPC    uint32
	skipValid bool
}

type watchPoint struct {
	size  int
	write bool
}

// NewDebugger returns an empty debugger.
func NewDebugger() *Debugger {
	return &Debugger{
		Breakpoints: map[uint32]bool{},
		Watches:     map[uint32]watchPoint{},
	}
}

// AddBreakpoint adds an execution breakpoint at addr.
func (d *Debugger) AddBreakpoint(addr uint32) { d.Breakpoints[addr] = true }

// RemoveBreakpoint removes an execution breakpoint.
func (d *Debugger) RemoveBreakpoint(addr uint32) { delete(d.Breakpoints, addr) }

// BreakpointList returns breakpoints in address order.
func (d *Debugger) BreakpointList() []uint32 {
	out := make([]uint32, 0, len(d.Breakpoints))
	for addr := range d.Breakpoints {
		out = append(out, addr)
	}
	slices.Sort(out)
	return out
}

// AddWriteWatch adds a write watchpoint covering [addr, addr+size).
func (d *Debugger) AddWriteWatch(addr uint32, size int) {
	if size <= 0 {
		size = 1
	}
	d.Watches[addr] = watchPoint{size: size, write: true}
}

// RemoveWatch removes a watchpoint at addr.
func (d *Debugger) RemoveWatch(addr uint32) { delete(d.Watches, addr) }

// WatchList returns watchpoint addresses in order.
func (d *Debugger) WatchList() []uint32 {
	out := make([]uint32, 0, len(d.Watches))
	for addr := range d.Watches {
		out = append(out, addr)
	}
	slices.Sort(out)
	return out
}

// Resume clears a stop and ignores a breakpoint at the current PC once.
func (d *Debugger) Resume() {
	if d.Stopped {
		d.skipPC = d.StopPC
		d.skipValid = true
	}
	d.Stopped = false
	d.StopReason = ""
	d.StopAddr = 0
}

// Pause records a user-requested stop.
func (d *Debugger) Pause(pc uint32, cycle uint64) {
	d.Stopped = true
	d.StopReason = "paused"
	d.StopPC = pc
	d.StopCycle = cycle
	d.StopAddr = 0
}

// Reset clears the stop state but keeps user breakpoints and watches.
func (d *Debugger) Reset() {
	d.Stopped = false
	d.StopReason = ""
	d.StopPC = 0
	d.StopCycle = 0
	d.StopAddr = 0
	d.skipValid = false
}

func (d *Debugger) checkPC(core int, pc uint32, cycle uint64) bool {
	if d.Stopped || !d.Breakpoints[pc] {
		return false
	}
	if d.skipValid && pc == d.skipPC {
		d.skipValid = false
		return false
	}
	d.Stopped = true
	d.StopReason = "breakpoint"
	d.StopPC = pc
	d.StopCore = core
	d.StopCycle = cycle
	return true
}

func (d *Debugger) checkWrite(addr uint32, core int, pc uint32, cycle uint64) bool {
	if d.Stopped || len(d.Watches) == 0 {
		return false
	}
	for base, w := range d.Watches {
		if addr >= base && addr < base+uint32(w.size) {
			d.Stopped = true
			d.StopReason = "watchpoint"
			d.StopPC = pc
			d.StopCore = core
			d.StopCycle = cycle
			d.StopAddr = base
			return true
		}
	}
	return false
}

// StepDebug executes one instruction on the selected core and leaves the
// debugger stopped. Breakpoints at the current PC are stepped over once.
func (c *Chip) StepDebug(core int) {
	if c.Debug == nil || c.Halted {
		return
	}
	c.Debug.Resume()
	c.Cycles++
	c.tickPeripherals()
	cpu := c.CPU0
	if core == 1 {
		cpu = c.CPU1
	}
	if cpu.Sleeping {
		// A debugger step wakes a core sitting in WFE/WFI.
		cpu.Sleeping = false
		cpu.SleepOnEvent = false
	}
	if !cpu.Halted && !cpu.Parked {
		c.stepCore(cpu)
	}
	c.Debug.Stopped = true
	c.Debug.StopReason = "step"
	c.Debug.StopCore = core
	c.Debug.StopPC = cpu.R[15]
	c.Debug.StopCycle = c.Cycles
	c.Debug.StopAddr = 0
}

func (c *Chip) debugWrite(addr uint32, size int) {
	if c.Debug == nil || len(c.Debug.Watches) == 0 {
		return
	}
	cpu := c.CPU0
	if c.core == 1 {
		cpu = c.CPU1
	}
	c.Debug.checkWrite(addr, c.core, cpu.R[15], c.Cycles)
}

// Peek8 reads memory without charging PSRAM latency or touching a peripheral
// register. The debugger uses it for register/memory panels.
func (c *Chip) Peek8(addr uint32) (byte, bool) {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		return c.SRAM[addr-SRAMBase], true
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize {
		return c.Flash[addr-FlashBase], true
	}
	if off, ok := flashAlias(addr); ok {
		if off < FlashSize {
			return c.Flash[off], true
		}
		return 0, false
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize > 0 && off < c.psramSize {
			return c.PSRAM[off], true
		}
		return 0, false
	}
	if addr < ROMSize {
		return c.ROM[addr], true
	}
	return 0, false
}

// PeekBytes reads a side-effect-free byte range.
func (c *Chip) PeekBytes(addr uint32, size int) ([]byte, bool) {
	if size <= 0 || size > 1<<20 {
		return nil, false
	}
	out := make([]byte, size)
	for i := range out {
		b, ok := c.Peek8(addr + uint32(i))
		if !ok {
			return nil, false
		}
		out[i] = b
	}
	return out, true
}
