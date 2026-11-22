package main

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/gdb"
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
