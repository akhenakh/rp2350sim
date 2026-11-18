package m33

import "testing"

func TestPushPopWide(t *testing.T) {
	bus := newTestBus()
	// push.w {r3-r11, lr}
	bus.Write16(0, 0xE92D)
	bus.Write16(2, 0x4FF8)
	// pop.w {r3-r11, pc}
	bus.Write16(4, 0xE8BD)
	bus.Write16(6, 0x8FF8)
	bus.Write16(8, 0xBF00)
	c := New(bus)
	c.R[15] = 0
	c.R[13] = 0x8000
	for i := 3; i <= 11; i++ {
		c.R[i] = uint32(0x1000 + i)
	}
	c.R[14] = 0x100
	c.Step() // push
	if c.R[13] != 0x8000-10*4 {
		t.Fatalf("sp after push = %#x", c.R[13])
	}
	for i := 3; i <= 11; i++ {
		c.R[i] = 0
	}
	c.R[15] = 4
	c.Step() // pop
	for i := 3; i <= 11; i++ {
		if c.R[i] != uint32(0x1000+i) {
			t.Fatalf("r%d = %#x want %#x", i, c.R[i], 0x1000+i)
		}
	}
}
