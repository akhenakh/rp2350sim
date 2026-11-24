package main

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/gdb"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestUIGDBTargetBasics(t *testing.T) {
	e := NewEmu()
	e.SetRunning(false)
	defer e.SetRunning(false)
	tgt := &uiGDBTarget{emu: e}

	if err := tgt.WriteMemory(0x20000000, []byte{0xAA, 0xBB}); err != nil {
		t.Fatal(err)
	}
	got, err := tgt.ReadMemory(0x20000000, 2)
	if err != nil || string(got) != "\xaa\xbb" {
		t.Fatalf("memory = % x, %v", got, err)
	}
	regs, err := tgt.ReadRegisters(0)
	if err != nil || len(regs) != gdb.RegisterCount {
		t.Fatalf("registers = %d, %v", len(regs), err)
	}
	if err := tgt.Halt(0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, ok := tgt.Stopped(); !ok {
		t.Fatal("halt did not stop the target")
	}
	e.chip.Write16(0x20000000, 0xBF00)
	e.chip.CPU0.R[15] = 0x20000000
	if err := tgt.Step(0); err != nil {
		t.Fatal(err)
	}
	if e.chip.CPU0.R[15] != 0x20000002 {
		t.Fatalf("step PC = %#x", e.chip.CPU0.R[15])
	}
	tgt.Continue()
	if _, _, _, _, ok := tgt.Stopped(); ok {
		t.Fatal("continue left the target stopped")
	}
	if !e.running {
		t.Fatal("continue did not start the UI machine")
	}
}

func TestUIGDBAddBreakpointCreatesDebugger(t *testing.T) {
	e := &Emu{chip: rp2350.New()}
	tgt := &uiGDBTarget{emu: e}
	if err := tgt.AddBreakpoint(0x10000008); err != nil {
		t.Fatal(err)
	}
	if e.debugger == nil || e.chip.Debug != e.debugger {
		t.Fatal("AddBreakpoint did not install the canonical debugger")
	}
	if !e.chip.Debug.Breakpoints[0x10000008] {
		t.Fatal("breakpoint missing after lazy debugger creation")
	}
	// A live reload builds a fresh chip and reattaches e.debugger; the
	// breakpoint must survive that swap.
	e.resetChip()
	if e.chip.Debug != e.debugger || !e.chip.Debug.Breakpoints[0x10000008] {
		t.Fatal("breakpoint lost across a machine reload")
	}
}

func TestUIGDBAddBreakpointWithoutMachine(t *testing.T) {
	e := &Emu{}
	if err := (&uiGDBTarget{emu: e}).AddBreakpoint(0x10000008); err == nil {
		t.Fatal("AddBreakpoint without a machine returned nil")
	}
}

func TestUIGDBServerToggle(t *testing.T) {
	if err := startGDB("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if !gdbRunning() || gdbListenAddr() == "" {
		t.Fatal("server did not start")
	}
	stopGDB()
	if gdbRunning() {
		t.Fatal("server did not stop")
	}
}
