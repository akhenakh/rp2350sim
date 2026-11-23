package main

import (
	"errors"
	"sync"

	"github.com/akhenakh/rp2350sim/internal/gdb"
	"github.com/akhenakh/rp2350sim/internal/m33"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// gdbTarget adapts the headless chip to the RSP target interface. mu is the
// same mutex the run loop holds while executing a chunk.
type gdbTarget struct {
	chip *rp2350.Chip
	mu   *sync.Mutex
}

func (t *gdbTarget) cpu(core int) (*m33.CPU, error) {
	switch core {
	case 0:
		return t.chip.CPU0, nil
	case 1:
		return t.chip.CPU1, nil
	}
	return nil, errors.New("22") // EINVAL
}

func (t *gdbTarget) ReadMemory(addr uint32, size int) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if size <= 0 {
		return []byte{}, nil
	}
	if data, ok := t.chip.PeekBytes(addr, size); ok {
		return data, nil
	}
	// GDB reads the expression behind a watchpoint. Peek only covers memory,
	// so fall back to a bus read for MMIO/SIO/PPB. A debug MMIO read can have
	// the same side effects as firmware reading the register.
	if !mmioRange(addr) || !mmioRange(addr+uint32(size)-1) {
		return nil, errors.New("unmapped address")
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = t.chip.Read8(addr + uint32(i))
	}
	return data, nil
}

func mmioRange(addr uint32) bool {
	return (addr >= rp2350.PeriphBase && addr < 0x60000000) ||
		(addr >= rp2350.SIOBase && addr < rp2350.SIOBase+0x1000) ||
		(addr >= 0xE0000000 && addr < 0xE0100000)
}

func (t *gdbTarget) WriteMemory(addr uint32, data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, b := range data {
		a := addr + uint32(i)
		// GDB may load code with M/X; writing the flash backing array is
		// debug-only but much more useful than silently ignoring it. The
		// partition window decides which storage bytes a runtime address
		// names.
		if a >= rp2350.FlashBase && a < rp2350.FlashBase+rp2350.FlashSize {
			t.chip.PokeFlash(a, b)
			continue
		}
		t.chip.Write8(a, b)
	}
	return nil
}

func (t *gdbTarget) ReadRegisters(core int) ([]uint32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cpu, err := t.cpu(core)
	if err != nil {
		return nil, err
	}
	regs := make([]uint32, gdb.RegisterCount)
	for i := range 13 {
		regs[i] = cpu.R[i]
	}
	regs[13] = cpu.R[13]
	regs[14] = cpu.R[14]
	regs[15] = cpu.R[15]
	regs[16] = cpu.APSR | 1<<24 | cpu.IPSR&0x1FF
	return regs, nil
}

func (t *gdbTarget) WriteRegister(core int, reg int, value uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	cpu, err := t.cpu(core)
	if err != nil {
		return err
	}
	switch {
	case reg >= 0 && reg <= 12:
		cpu.R[reg] = value
	case reg == 13:
		cpu.R[13] = value
		if cpu.IPSR == 0 && cpu.CONTROL&2 != 0 {
			cpu.PSP = value
		} else {
			cpu.MSP = value
		}
	case reg == 14:
		cpu.R[14] = value
	case reg == 15:
		cpu.R[15] = value &^ 1
	case reg == 16:
		cpu.APSR = value & 0xF8000000
	default:
		return errors.New("22")
	}
	return nil
}

func (t *gdbTarget) NumCores() int { return 2 }

func (t *gdbTarget) Stopped() (core int, pc uint32, reason string, addr uint32, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.chip.Debug
	if d == nil || !d.Stopped {
		return 0, 0, "", 0, false
	}
	return d.StopCore, d.StopPC, d.StopReason, d.StopAddr, true
}

func (t *gdbTarget) Continue() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chip.Debug != nil {
		t.chip.Debug.Resume()
	}
}

func (t *gdbTarget) Step(core int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chip.Debug == nil {
		return errors.New("no debugger")
	}
	t.chip.StepDebug(core)
	return nil
}

func (t *gdbTarget) Halt(core int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chip.Debug == nil {
		t.chip.Debug = rp2350.NewDebugger()
	}
	cpu, err := t.cpu(core)
	if err != nil {
		return err
	}
	t.chip.Debug.Pause(core, cpu.R[15], t.chip.Cycles)
	t.chip.Debug.StopReason = "interrupt"
	return nil
}

func (t *gdbTarget) AddBreakpoint(addr uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureDebugger()
	t.chip.Debug.AddBreakpoint(addr)
	return nil
}

func (t *gdbTarget) RemoveBreakpoint(addr uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chip.Debug != nil {
		t.chip.Debug.RemoveBreakpoint(addr)
	}
	return nil
}

func (t *gdbTarget) AddWatchpoint(addr uint32, size int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureDebugger()
	t.chip.Debug.AddWriteWatch(addr, size)
	return nil
}

func (t *gdbTarget) RemoveWatchpoint(addr uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chip.Debug != nil {
		t.chip.Debug.RemoveWatch(addr)
	}
	return nil
}

func (t *gdbTarget) Reset() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.chip.Boot()
	t.ensureDebugger()
	t.chip.Debug.Pause(0, t.chip.CPU0.R[15], t.chip.Cycles)
	t.chip.Debug.StopReason = "reset"
	return nil
}

func (t *gdbTarget) ensureDebugger() {
	if t.chip.Debug == nil {
		t.chip.Debug = rp2350.NewDebugger()
	}
}
