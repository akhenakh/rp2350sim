package rp2350

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/m33"
)

func TestSymbolHookDispatch(t *testing.T) {
	c := New()
	const entry = 0x20000000
	const ret = 0x20000101
	called := false
	c.InstallSymbolHook(entry|1, func(cpu *m33.CPU) {
		called = true
		cpu.R[0] = 7
		cpu.R[15] = cpu.R[14] &^ 1
	})
	c.CPU0.R[15] = entry
	c.CPU0.R[14] = ret
	c.stepCore(c.CPU0)
	if !called {
		t.Fatal("symbol hook was not called")
	}
	if c.CPU0.R[0] != 7 {
		t.Fatalf("R0 = %#x", c.CPU0.R[0])
	}
	if c.CPU0.R[15] != ret&^1 {
		t.Fatalf("PC = %#x, want %#x", c.CPU0.R[15], ret&^1)
	}

	c.ClearSymbolHooks()
	c.CPU0.R[15] = entry
	called = false
	// With the hook removed this address holds flash data, so the step may
	// fault. The point of the check is only that the hook no longer fires.
	c.stepCore(c.CPU0)
	if called {
		t.Fatal("cleared symbol hook was called")
	}
}
