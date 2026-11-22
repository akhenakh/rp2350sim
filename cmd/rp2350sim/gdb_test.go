package main

import (
	"sync"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/gdb"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestGDBTargetBasics(t *testing.T) {
	c := rp2350.New()
	var mu sync.Mutex
	tgt := &gdbTarget{chip: c, mu: &mu}

	if err := tgt.WriteMemory(0x20000000, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	got, err := tgt.ReadMemory(0x20000000, 4)
	if err != nil || string(got) != "\x01\x02\x03\x04" {
		t.Fatalf("memory = % x, %v", got, err)
	}
	regs, err := tgt.ReadRegisters(0)
	if err != nil || len(regs) != gdb.RegisterCount {
		t.Fatalf("registers = %d, %v", len(regs), err)
	}
	if err := tgt.WriteRegister(0, 15, 0x20000001); err != nil {
		t.Fatal(err)
	}
	if c.CPU0.R[15] != 0x20000000 {
		t.Fatalf("PC = %#x", c.CPU0.R[15])
	}

	if err := tgt.AddBreakpoint(0x20000010); err != nil {
		t.Fatal(err)
	}
	if err := tgt.Halt(0); err != nil {
		t.Fatal(err)
	}
	core, _, reason, _, ok := tgt.Stopped()
	if !ok || core != 0 || reason != "interrupt" {
		t.Fatalf("stopped = %v core=%d reason=%q", ok, core, reason)
	}

	// Step one NOP from SRAM.
	c.Write16(0x20000000, 0xBF00)
	c.CPU0.R[15] = 0x20000000
	if err := tgt.Step(0); err != nil {
		t.Fatal(err)
	}
	if c.CPU0.R[15] != 0x20000002 {
		t.Fatalf("step PC = %#x", c.CPU0.R[15])
	}
	if _, _, _, _, ok := tgt.Stopped(); !ok {
		t.Fatal("step did not leave the target stopped")
	}
	tgt.Continue()
	if _, _, _, _, ok := tgt.Stopped(); ok {
		t.Fatal("continue left the target stopped")
	}

	// MMIO reads needed for watchpoint expressions fall back to the bus.
	if _, err := tgt.ReadMemory(0x400B0000, 4); err != nil {
		t.Fatalf("MMIO read: %v", err)
	}
}
