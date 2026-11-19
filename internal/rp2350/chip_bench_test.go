package rp2350

import "testing"

// BenchmarkChipIdle measures the per-cycle cost when both cores are asleep:
// this is the peripheral scheduling path that dominates firmware which sleeps
// or polls, and it must stay close to a handful of instructions per cycle.
func BenchmarkChipIdle(b *testing.B) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	b.ReportAllocs()
	for b.Loop() {
		c.Run(1_000_000)
	}
}

// BenchmarkChipCPU measures one core executing a two-instruction loop in
// SRAM: an add and a branch back. It reports the interpreter plus the
// per-cycle scheduler cost, with no firmware or boot rom involved.
func BenchmarkChipCPU(b *testing.B) {
	c := New()
	// 0x3001 = adds r0, #1; 0xe7fd = b to 0x20000000.
	c.SRAM[0], c.SRAM[1] = 0x01, 0x30
	c.SRAM[2], c.SRAM[3] = 0xfd, 0xe7
	c.CPU0.R[13] = 0x2001_0000
	c.CPU0.R[15] = 0x2000_0000
	c.CPU1.Parked = true
	b.ReportAllocs()
	for b.Loop() {
		c.Run(1_000_000)
	}
}

// BenchmarkChipPIO measures a chip with PIO0 enabled, the shape used by LED
// and protocol firmware. It guards the enabled-block dispatch.
func BenchmarkChipPIO(b *testing.B) {
	c := New()
	c.pio[0].writeCtrl(1) // CTRL: SM0 enabled
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	b.ReportAllocs()
	for b.Loop() {
		c.Run(1_000_000)
	}
}
