package m33

import "testing"

func TestHandlerPrologueEpilogue(t *testing.T) {
	bus := newTestBusAt(0x10000000, 0x1000)
	sb := newTestBusAt(0x20000000, 0x1000)
	// combined bus: dispatch by range
	cb := &rangeBus{a: bus, b: sb}
	put16 := func(addr uint32, v uint16) { cb.Write16(addr, v) }

	put16(0x10000000, 0xE92D) // push.w {r4-r11, lr}
	put16(0x10000002, 0x4FF0)
	put16(0x10000004, 0xB08B) // sub sp,#0x2c
	put16(0x10000006, 0xBF00) // nop
	put16(0x10000008, 0xB00B) // add sp,#0x2c
	put16(0x1000000A, 0xE8BD) // pop.w {r4-r11, pc}
	put16(0x1000000C, 0x8FF0)

	c := New(cb)
	c.R[15] = 0x10000000
	c.R[13] = 0x20000800
	for i := range 12 {
		c.R[i] = 0x100 + uint32(i)
	}
	c.R[14] = 0x10000045
	c.Step()
	c.Step()
	if c.R[13] != 0x200007B0 {
		t.Fatalf("after prologue sp=%#x want 0x200007B0", c.R[13])
	}
	c.Step() // nop
	c.Step() // add sp,#0x2c
	if c.R[13] != 0x200007DC {
		t.Fatalf("after add sp=%#x want 0x200007dc", c.R[13])
	}
	c.Step()
	if c.R[15] != 0x10000044 {
		t.Fatalf("pop pc=%#x want 0x10000044", c.R[15])
	}
}

type rangeBus struct{ a, b *testBus }

func (r *rangeBus) Read8(addr uint32) uint8 {
	if addr >= 0x20000000 {
		return r.b.Read8(addr)
	}
	return r.a.Read8(addr)
}
func (r *rangeBus) Read16(addr uint32) uint16 {
	if addr >= 0x20000000 {
		return r.b.Read16(addr)
	}
	return r.a.Read16(addr)
}
func (r *rangeBus) Read32(addr uint32) uint32 {
	if addr >= 0x20000000 {
		return r.b.Read32(addr)
	}
	return r.a.Read32(addr)
}
func (r *rangeBus) Write8(addr uint32, v uint8) {
	if addr >= 0x20000000 {
		r.b.Write8(addr, v)
		return
	}
	r.a.Write8(addr, v)
}
func (r *rangeBus) Write16(addr uint32, v uint16) {
	if addr >= 0x20000000 {
		r.b.Write16(addr, v)
		return
	}
	r.a.Write16(addr, v)
}
func (r *rangeBus) Write32(addr uint32, v uint32) {
	if addr >= 0x20000000 {
		r.b.Write32(addr, v)
		return
	}
	r.a.Write32(addr, v)
}
