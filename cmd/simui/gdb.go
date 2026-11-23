package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/akhenakh/rp2350sim/internal/gdb"
	"github.com/akhenakh/rp2350sim/internal/m33"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// gdbDefaultAddr is the address used by the toolbar button and --gdb.
var gdbDefaultAddr = "127.0.0.1:3333"

var (
	gdbMu     sync.Mutex
	gdbServer *gdb.Server
	gdbAddr   string
)

// startGDB starts the RSP listener. It is safe to call more than once.
func startGDB(addr string) error {
	gdbMu.Lock()
	defer gdbMu.Unlock()
	if gdbServer != nil {
		return nil
	}
	if addr == "" {
		addr = gdbDefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := gdb.NewServer(&uiGDBTarget{emu: emu}, func(string, ...any) {})
	gdbServer = srv
	gdbAddr = ln.Addr().String()
	go func() {
		if err := srv.Serve(ln); err != nil {
			fmt.Fprintln(os.Stderr, "gdb server:", err)
		}
	}()
	return nil
}

func stopGDB() {
	gdbMu.Lock()
	defer gdbMu.Unlock()
	if gdbServer == nil {
		return
	}
	_ = gdbServer.Close()
	gdbServer = nil
	gdbAddr = ""
}

func gdbRunning() bool {
	gdbMu.Lock()
	defer gdbMu.Unlock()
	return gdbServer != nil
}

func gdbListenAddr() string {
	gdbMu.Lock()
	defer gdbMu.Unlock()
	return gdbAddr
}

// uiGDBTarget adapts the UI's Emu to the RSP server. Every method takes the
// Emu mutex, which is also held while the UI steps a chunk.
type uiGDBTarget struct {
	emu *Emu
}

func (t *uiGDBTarget) cpu(core int) (*m33.CPU, error) {
	if t.emu.chip == nil {
		return nil, errors.New("no machine")
	}
	switch core {
	case 0:
		return t.emu.chip.CPU0, nil
	case 1:
		return t.emu.chip.CPU1, nil
	}
	return nil, errors.New("22")
}

func (t *uiGDBTarget) ReadMemory(addr uint32, size int) ([]byte, error) {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip == nil {
		return nil, errors.New("no machine")
	}
	if size <= 0 {
		return []byte{}, nil
	}
	if data, ok := t.emu.chip.PeekBytes(addr, size); ok {
		return data, nil
	}
	if !mmioAddr(addr) || !mmioAddr(addr+uint32(size)-1) {
		return nil, errors.New("unmapped address")
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = t.emu.chip.Read8(addr + uint32(i))
	}
	return data, nil
}

func mmioAddr(addr uint32) bool {
	return (addr >= rp2350.PeriphBase && addr < 0x60000000) ||
		(addr >= rp2350.SIOBase && addr < rp2350.SIOBase+0x1000) ||
		(addr >= 0xE0000000 && addr < 0xE0100000)
}

func (t *uiGDBTarget) WriteMemory(addr uint32, data []byte) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip == nil {
		return errors.New("no machine")
	}
	for i, b := range data {
		a := addr + uint32(i)
		if a >= rp2350.FlashBase && a < rp2350.FlashBase+rp2350.FlashSize {
			t.emu.chip.PokeFlash(a, b)
			continue
		}
		t.emu.chip.Write8(a, b)
	}
	return nil
}

func (t *uiGDBTarget) ReadRegisters(core int) ([]uint32, error) {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
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

func (t *uiGDBTarget) WriteRegister(core int, reg int, value uint32) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
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

func (t *uiGDBTarget) NumCores() int { return 2 }

func (t *uiGDBTarget) Stopped() (core int, pc uint32, reason string, addr uint32, ok bool) {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip == nil || t.emu.chip.Debug == nil || !t.emu.chip.Debug.Stopped {
		return 0, 0, "", 0, false
	}
	d := t.emu.chip.Debug
	return d.StopCore, d.StopPC, d.StopReason, d.StopAddr, true
}

func (t *uiGDBTarget) Continue() {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip != nil && t.emu.chip.Debug != nil {
		t.emu.chip.Debug.Resume()
	}
	t.emu.running = true
}

func (t *uiGDBTarget) Step(core int) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip == nil || t.emu.chip.Debug == nil {
		return errors.New("no debugger")
	}
	t.emu.chip.StepDebug(core)
	t.emu.running = false
	return nil
}

func (t *uiGDBTarget) Halt(core int) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	cpu, err := t.cpu(core)
	if err != nil {
		return err
	}
	if t.emu.chip.Debug == nil {
		t.emu.chip.Debug = rp2350.NewDebugger()
	}
	t.emu.chip.Debug.Pause(core, cpu.R[15], t.emu.chip.Cycles)
	t.emu.chip.Debug.StopReason = "interrupt"
	t.emu.running = false
	return nil
}

func (t *uiGDBTarget) AddBreakpoint(addr uint32) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip != nil && t.emu.chip.Debug != nil {
		t.emu.chip.Debug.AddBreakpoint(addr)
	}
	return nil
}

func (t *uiGDBTarget) RemoveBreakpoint(addr uint32) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip != nil && t.emu.chip.Debug != nil {
		t.emu.chip.Debug.RemoveBreakpoint(addr)
	}
	return nil
}

func (t *uiGDBTarget) AddWatchpoint(addr uint32, size int) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip != nil && t.emu.chip.Debug != nil {
		t.emu.chip.Debug.AddWriteWatch(addr, size)
	}
	return nil
}

func (t *uiGDBTarget) RemoveWatchpoint(addr uint32) error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	if t.emu.chip != nil && t.emu.chip.Debug != nil {
		t.emu.chip.Debug.RemoveWatch(addr)
	}
	return nil
}

func (t *uiGDBTarget) Reset() error {
	t.emu.mu.Lock()
	defer t.emu.mu.Unlock()
	t.emu.rebootLocked()
	t.emu.running = false
	if t.emu.chip != nil && t.emu.chip.Debug != nil {
		t.emu.chip.Debug.Pause(0, t.emu.chip.CPU0.R[15], t.emu.chip.Cycles)
		t.emu.chip.Debug.StopReason = "reset"
	}
	return nil
}
