package rp2350

import "github.com/akhenakh/rp2350sim/internal/m33"

// SymbolHook replaces a firmware function at its entry address. The CPU is at
// the function entry when fn runs; the hook reads arguments from R0-R3, writes
// its result to R0 and must leave R4-R11 untouched. Returning is the hook's
// responsibility: the usual ending is cpu.R[15] = cpu.R[14] &^ 1.
//
// Hooks exist for device models that sit behind a firmware symbol, such as the
// CYW43 Bluetooth HCI transport. They are installed by address after an ELF is
// loaded, so a stripped or LTO-inlined image fails at bind time instead of
// running the real hardware path.
type SymbolHook func(cpu *m33.CPU)

// InstallSymbolHook traps the function entry at addr. The Thumb bit is ignored.
// Installing a second hook at the same address replaces the first.
func (c *Chip) InstallSymbolHook(addr uint32, fn SymbolHook) {
	if fn == nil {
		return
	}
	if c.symbolHooks == nil {
		c.symbolHooks = make(map[uint32]SymbolHook)
	}
	c.symbolHooks[addr&^1] = fn
}

// ClearSymbolHooks removes every installed hook. Callers use it when a new
// image replaces the current one.
func (c *Chip) ClearSymbolHooks() {
	c.symbolHooks = nil
}
