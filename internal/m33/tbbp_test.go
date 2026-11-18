package m33

import "testing"

func TestTBBPC(t *testing.T) {
	bus := newTestBusAt(0x20000000, 0x1000)
	bus.Write16(0x20000000, 0xE8DF)
	bus.Write16(0x20000002, 0xF001)
	bus.Write8(0x20000004, 4)
	bus.Write8(0x20000005, 6)
	bus.Write16(0x2000000C, 0xBF00)
	c := New(bus)
	c.R[15] = 0x20000000
	c.R[1] = 0
	c.Step()
	t.Logf("pc=%#x", c.R[15])
	if c.R[15] != 0x2000000C {
		t.Fatalf("pc=%#x want 0x2000000C", c.R[15])
	}
}
