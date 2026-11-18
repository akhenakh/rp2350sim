package rp2350

import "testing"

func TestPIOExecSetX(t *testing.T) {
	c := New()
	const base = pioBase0
	// Push 0x99 to SM0's TX FIFO, then execute OUT X,32; IN X,32 and read RXF0.
	c.Write32(base+0x10, 0x99)
	c.Write32(base+0xD8, 0x6020) // out x, 32
	c.Write32(base+0xD8, 0x4020) // in x, 32
	c.Write32(base+0xD8, 0x8020) // push block
	got := c.Read32(base + 0x20)
	t.Logf("rxf0=%#x", got)
	if got != 0x99 {
		t.Fatalf("rxf0=%#x want 0x99", got)
	}
}
