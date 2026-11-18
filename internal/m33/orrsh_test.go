package m33

import "testing"

func TestOrrShifted(t *testing.T) {
	bus := newTestBus()
	bus.Write16(0, 0xEA42)
	bus.Write16(2, 0x01D1)
	c := New(bus)
	c.R[15] = 0
	c.R[2] = 0
	c.R[1] = 0xF8
	c.Step()
	t.Logf("r1=%#x", c.R[1])
	if c.R[1] != 0x1F {
		t.Fatalf("r1=%#x want 0x1F", c.R[1])
	}
}
