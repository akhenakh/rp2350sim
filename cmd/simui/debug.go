package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/debug"
)

// DebugBreakpoint is one execution breakpoint shown in the Debug tab.
type DebugBreakpoint struct {
	Addr   uint32
	Symbol string
	File   string
	Line   int
}

// DebugWatch is one write watchpoint.
type DebugWatch struct {
	Addr uint32
	Size int
}

// DebugSnapshot is the state the Debug tab renders for one frame.
type DebugSnapshot struct {
	PC, SP      uint32
	Registers   [16]uint32
	APSR        uint32
	IPSR        uint32
	CONTROL     uint32
	MSP, PSP    uint32
	FPSCR       uint32
	FPUActive   bool
	Cycles      uint64
	Stopped     bool
	StopReason  string
	StopPC      uint32
	StopCore    int
	Function    string
	SourceFile  string
	SourceLine  int
	SourceStart int
	SourceLines []string
	Locals      []debug.Variable
	Globals     []debug.Variable
	Breakpoints []DebugBreakpoint
	Watches     []DebugWatch
	HasSymbols  bool
}

// AddBreakpoint resolves a hex address or symbol and arms an execution
// breakpoint.
func (e *Emu) AddBreakpoint(spec string) (uint32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	addr, err := e.resolveLocked(spec)
	if err != nil {
		return 0, err
	}
	e.debugger.AddBreakpoint(addr)
	return addr, nil
}

func (e *Emu) resolveLocked(spec string) (uint32, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, fmt.Errorf("empty address")
	}
	raw := strings.TrimPrefix(strings.TrimPrefix(spec, "0x"), "0X")
	if v, err := strconv.ParseUint(raw, 16, 32); err == nil {
		return uint32(v), nil
	}
	if e.debugInfo != nil {
		if addr, ok := e.debugInfo.Resolve(spec); ok {
			return addr, nil
		}
	}
	return 0, fmt.Errorf("%q is neither a hex address nor a known symbol", spec)
}

// RemoveBreakpoint removes an execution breakpoint.
func (e *Emu) RemoveBreakpoint(addr uint32) {
	e.mu.Lock()
	e.debugger.RemoveBreakpoint(addr)
	e.mu.Unlock()
}

// AddWatch resolves an address/symbol and adds a write watchpoint.
func (e *Emu) AddWatch(spec string, size int) (uint32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	addr, err := e.resolveLocked(spec)
	if err != nil {
		return 0, err
	}
	e.debugger.AddWriteWatch(addr, size)
	return addr, nil
}

// RemoveWatch removes a write watchpoint.
func (e *Emu) RemoveWatch(addr uint32) {
	e.mu.Lock()
	e.debugger.RemoveWatch(addr)
	e.mu.Unlock()
}

// StepDebug executes one instruction on core 0 or 1 and pauses.
func (e *Emu) StepDebug(core int) {
	e.mu.Lock()
	e.running = false
	e.chip.StepDebug(core)
	e.mu.Unlock()
}

// RunCycles executes up to n virtual cycles and then pauses, unless a
// breakpoint/watchpoint stops earlier.
func (e *Emu) RunCycles(n uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.running = false
	e.debugger.Resume()
	e.chip.Run(n)
	if !e.debugger.Stopped {
		e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
	}
}

// RunToCycle advances to an absolute virtual cycle count.
func (e *Emu) RunToCycle(target uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if target <= e.chip.Cycles {
		return
	}
	e.running = false
	e.debugger.Resume()
	e.chip.Run(target - e.chip.Cycles)
	if !e.debugger.Stopped {
		e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
	}
}

// RunToAddress arms a temporary-looking breakpoint and resumes the machine.
// The breakpoint stays in the list, so it can be removed like any other.
func (e *Emu) RunToAddress(spec string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	addr, err := e.resolveLocked(spec)
	if err != nil {
		return err
	}
	e.debugger.AddBreakpoint(addr)
	e.debugger.Resume()
	e.running = true
	return nil
}

// ReadMemory returns a side-effect-free memory range for the hex viewer.
func (e *Emu) ReadMemory(addr uint32, size int) ([]byte, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chip.PeekBytes(addr, size)
}

// DebugFunctions returns the ELF function list, when symbols are loaded.
func (e *Emu) DebugFunctions() []debug.Function {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.debugInfo == nil {
		return nil
	}
	return append([]debug.Function(nil), e.debugInfo.Functions...)
}

// DebugSnapshot builds the Debug tab frame. DWARF values are evaluated only
// when the Debug tab asks for a frame.
func (e *Emu) DebugSnapshot() DebugSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	cpu := e.chip.CPU0
	if e.debugger.Stopped && e.debugger.StopCore == 1 {
		cpu = e.chip.CPU1
	}
	s := DebugSnapshot{
		PC:         cpu.R[15],
		SP:         cpu.R[13],
		Registers:  [16]uint32(cpu.R),
		APSR:       cpu.APSR,
		IPSR:       cpu.IPSR,
		CONTROL:    cpu.CONTROL,
		MSP:        cpu.MSP,
		PSP:        cpu.PSP,
		FPSCR:      cpu.FPSCR,
		FPUActive:  cpu.FPUActive,
		Cycles:     e.chip.Cycles,
		Stopped:    e.debugger.Stopped,
		StopReason: e.debugger.StopReason,
		StopPC:     e.debugger.StopPC,
		StopCore:   e.debugger.StopCore,
		HasSymbols: e.debugInfo != nil,
	}
	for _, addr := range e.debugger.BreakpointList() {
		bp := DebugBreakpoint{Addr: addr}
		if e.debugInfo != nil {
			if fn, ok := e.debugInfo.FunctionAt(addr); ok {
				bp.Symbol = fn.Name
			}
			if file, line, ok := e.debugInfo.LineAt(addr); ok {
				bp.File, bp.Line = file, line
			}
		}
		s.Breakpoints = append(s.Breakpoints, bp)
	}
	for _, addr := range e.debugger.WatchList() {
		s.Watches = append(s.Watches, DebugWatch{Addr: addr, Size: 4})
	}
	if e.debugInfo != nil {
		if fn, ok := e.debugInfo.FunctionAt(s.PC); ok {
			s.Function = fn.Name
		}
		if file, line, ok := e.debugInfo.LineAt(s.PC); ok && file != "" {
			s.SourceFile, s.SourceLine = file, line
			if lines, start, err := debug.Source(file, line, 8); err == nil {
				s.SourceLines, s.SourceStart = lines, start
			}
		}
		read := func(addr uint32, size int) ([]byte, bool) { return e.chip.PeekBytes(addr, size) }
		for _, v := range e.debugInfo.VariablesAt(s.PC, s.Registers, read) {
			if v.Scope == "global" {
				s.Globals = append(s.Globals, v)
			} else {
				s.Locals = append(s.Locals, v)
			}
		}
	}
	return s
}
