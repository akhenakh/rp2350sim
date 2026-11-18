package m33

import "testing"

func TestUDivLarge(t *testing.T) {
	bus := newTestBus()
	bus.Write16(0, 0xFBB1)
	bus.Write16(2, 0xF0F2)
	bus.Write16(4, 0xBF00)
	c := New(bus)
	c.R[15] = 0
	c.R[1] = 517152
	c.R[2] = 65
	c.Step()
	if c.R[0] != 7956 {
		t.Fatalf("udiv 517152/65 = %d want 7956", c.R[0])
	}
}
