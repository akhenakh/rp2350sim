package m33

import "testing"

func TestITThree(t *testing.T) {
	bus := newTestBus()
	bus.Write16(0, 0x2809) // cmp r0, #9
	bus.Write16(2, 0xBF9E) // ittt ls
	bus.Write16(4, 0x460C) // movls r4, r1
	bus.Write16(6, 0x460D) // movls r5, r1
	bus.Write16(8, 0x460E) // movls r6, r1
	bus.Write16(10, 0xBF00)
	c := New(bus)
	c.R[15] = 0
	c.R[0] = 4 // LS true
	c.R[1] = 0x99
	for range 5 {
		c.Step()
	}
	if c.R[4] != 0x99 || c.R[5] != 0x99 || c.R[6] != 0x99 {
		t.Fatalf("r4=%#x r5=%#x r6=%#x want all 0x99", c.R[4], c.R[5], c.R[6])
	}
}
