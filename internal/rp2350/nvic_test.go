package rp2350

import "testing"

// TestExternalIRQDelivery checks that taking an enabled external IRQ loads PC
// from the vector table, stacks the caller frame and clears the pending bit.
func TestExternalIRQDelivery(t *testing.T) {
	c := New()
	c.CPU0.R[15] = FlashBase
	c.CPU0.R[13] = c.CPU0.MSP
	c.CPU0.SetVectorTable(FlashBase)
	put := func(addr, v uint32) {
		c.LoadSegment(addr, []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
	}
	// Flash is read-only through the bus, so preload it directly.
	put(FlashBase, 0xF3FF)
	put(FlashBase+4*30, 0x10000101)

	const handler = 0x10000100
	put(handler, 0xF3FF)

	c.NVIC.write(0x100, 1<<14) // ISER: enable IRQ 14
	c.NVIC.Pend(14)

	for i := 0; i < 32 && c.CPU0.IPSR == 0; i++ {
		c.Step()
	}
	if c.CPU0.IPSR != 30 {
		t.Fatalf("IPSR = %d, want 30", c.CPU0.IPSR)
	}
	if c.CPU0.R[15] != handler {
		t.Fatalf("pc = %#x, want %#x", c.CPU0.R[15], handler)
	}
	if c.NVIC.pending[0]&(1<<14) != 0 {
		t.Fatal("pending bit was not cleared on entry")
	}
}

func TestNVICByteAndHalfwordPriorityWrites(t *testing.T) {
	c := New()
	c.Write8(SCSBase+0x402, 0x80)
	if got := c.NVIC.prio[2]; got != 0x80 {
		t.Fatalf("byte IPR write = %#x, want 0x80", got)
	}
	c.Write16(SCSBase+0x404, 0x6040)
	if c.NVIC.prio[4] != 0x40 || c.NVIC.prio[5] != 0x60 {
		t.Fatalf("halfword IPR write = %#x %#x", c.NVIC.prio[4], c.NVIC.prio[5])
	}
}
