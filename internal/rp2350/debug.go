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
	skipCore  int
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
		d.skipCore = d.StopCore
		d.skipValid = true
	}
	d.Stopped = false
	d.StopReason = ""
	d.StopAddr = 0
}

// Pause records a user-requested stop on a core.
func (d *Debugger) Pause(core int, pc uint32, cycle uint64) {
	d.Stopped = true
	d.StopReason = "paused"
	d.StopCore = core
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
	if d.skipValid && pc == d.skipPC && core == d.skipCore {
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

func (d *Debugger) checkWrite(addr uint32, size int, core int, pc uint32, cycle uint64) bool {
	if d.Stopped || len(d.Watches) == 0 {
		return false
	}
	if size <= 0 {
		size = 1
	}
	start, end := uint64(addr), uint64(addr)+uint64(size)
	for base, w := range d.Watches {
		lo := uint64(base)
		hi := lo + uint64(w.size)
		// Any overlap counts: a 32-bit store that straddles a byte watchpoint
		// is still a write to the watched address.
		if start < hi && lo < end {
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
	if c.Debug.Stopped {
		// A watchpoint fired during the step; keep that stop instead of
		// reporting a plain step and discarding StopAddr.
		return
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
	c.Debug.checkWrite(addr, size, c.core, cpu.R[15], c.Cycles)
}

// Peek8 reads memory without charging PSRAM latency or touching a peripheral
// register. The debugger uses it for register/memory panels.
func (c *Chip) Peek8(addr uint32) (byte, bool) {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		return c.SRAM[addr-SRAMBase], true
	}
	if off, ok := c.flashIndex(addr, 1); ok {
		return c.Flash[off], true
	}
	if off, ok := c.flashAliasIndex(addr, 1); ok {
		return c.Flash[off], true
	}
	if xipMaintenance(addr) {
		return 0, true
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

// PokeFlash writes a byte through the booted partition's XIP window. The
// debugger uses it to load code; firmware flash writes use the ROM services.
func (c *Chip) PokeFlash(addr uint32, v byte) bool {
	off, ok := c.flashIndex(addr, 1)
	if !ok {
		return false
	}
	c.Flash[off] = v
	return true
}

// PeekRegister returns a modeled peripheral register's raw word without running
// its read hook, so diagnostics can inspect power/reset fields without clearing
// status bits.
func (c *Chip) PeekRegister(addr uint32) (uint32, bool) {
	if addr < PeriphBase {
		return 0, false
	}
	if addr >= SIOBase && addr < SIOBase+0x1000 {
		return c.sio.read(c, addr-SIOBase), true
	}
	if addr >= SCSBase && addr < SCSBase+0x1000 {
		return c.ppbForCore().read(addr - SCSBase), true
	}
	if b := c.regBlockFor(addr); b != nil {
		return b.regs[(addr-b.base)/4], true
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
