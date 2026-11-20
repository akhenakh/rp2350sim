package m33

import "testing"

type testBus struct {
	base uint32
	mem  []byte
}

func newTestBus() *testBus { return newTestBusAt(0, 1<<16) }

func newTestBusAt(base uint32, size int) *testBus {
	return &testBus{base: base, mem: make([]byte, size)}
}

func (b *testBus) offset(a uint32) (int, bool) {
	o := int(a - b.base)
	if o < 0 || o >= len(b.mem) {
		return 0, false
	}
	return o, true
}

func (b *testBus) Read8(a uint32) uint8 {
	o, ok := b.offset(a)
	if !ok {
		return 0
	}
	return b.mem[o]
}
func (b *testBus) Read16(a uint32) uint16 {
	o, ok := b.offset(a)
	if !ok || o+1 >= len(b.mem) {
		return 0
	}
	return uint16(b.mem[o]) | uint16(b.mem[o+1])<<8
}
func (b *testBus) Read32(a uint32) uint32 {
	o, ok := b.offset(a)
	if !ok || o+3 >= len(b.mem) {
		return 0
	}
	return uint32(b.mem[o]) | uint32(b.mem[o+1])<<8 | uint32(b.mem[o+2])<<16 | uint32(b.mem[o+3])<<24
}
func (b *testBus) Write8(a uint32, v uint8) {
	if o, ok := b.offset(a); ok {
		b.mem[o] = v
	}
}
func (b *testBus) Write16(a uint32, v uint16) {
	if o, ok := b.offset(a); ok && o+1 < len(b.mem) {
		b.mem[o] = byte(v)
		b.mem[o+1] = byte(v >> 8)
	}
}
func (b *testBus) Write32(a uint32, v uint32) {
	if o, ok := b.offset(a); ok && o+3 < len(b.mem) {
		b.mem[o] = byte(v)
		b.mem[o+1] = byte(v >> 8)
		b.mem[o+2] = byte(v >> 16)
		b.mem[o+3] = byte(v >> 24)
	}
}

// load copies the program and scratch data for a differential case.
func (b *testBus) load(c diffCase) {
	data := c.bytes()
	for i, v := range data {
		b.Write8(qemuCodeBase+uint32(i), v)
	}
	for addr, v := range c.mem {
		b.Write32(addr, v)
	}
}

// run16 executes one 16-bit instruction and returns the CPU.
func run16(t *testing.T, hw uint16) *CPU {
	t.Helper()
	bus := newTestBus()
	bus.Write16(0, hw)
	bus.Write16(2, 0xBF00) // nop
	c := New(bus)
	c.R[15] = 0
	c.Step()
	return c
}

// run32 executes one 32-bit instruction and returns the CPU.
func run32(t *testing.T, hw1, hw2 uint16) *CPU {
	t.Helper()
	bus := newTestBus()
	bus.Write16(0, hw1)
	bus.Write16(2, hw2)
	bus.Write16(4, 0xBF00)
	c := New(bus)
	c.R[15] = 0
	c.Step()
	return c
}

func TestMulDiv(t *testing.T) {
	// mul r0, r1, r2
	c := run32(t, 0xFB01, 0xF002)
	c.R[1] = 6
	c.R[2] = 7
	_ = c
	// re-run with values set before stepping
	bus := newTestBus()
	bus.Write16(0, 0xFB01)
	bus.Write16(2, 0xF002)
	bus.Write16(4, 0xBF00)
	c = New(bus)
	c.R[15] = 0
	c.R[1] = 6
	c.R[2] = 7
	c.Step()
	if c.R[0] != 42 {
		t.Fatalf("mul: got %d want 42", c.R[0])
	}

	// udiv r0, r1, r2
	bus = newTestBus()
	bus.Write16(0, 0xFBB1)
	bus.Write16(2, 0xF0F2)
	bus.Write16(4, 0xBF00)
	c = New(bus)
	c.R[15] = 0
	c.R[1] = 100
	c.R[2] = 7
	c.Step()
	if c.R[0] != 14 {
		t.Fatalf("udiv: got %d want 14", c.R[0])
	}

	// sdiv r0, r1, r2 with negative numerator
	bus = newTestBus()
	bus.Write16(0, 0xFB91)
	bus.Write16(2, 0xF0F2)
	bus.Write16(4, 0xBF00)
	c = New(bus)
	c.R[15] = 0
	c.R[1] = 0xFFFFFFF9 // -7
	c.R[2] = 2
	c.Step()
	if c.R[0] != 0xFFFFFFFD {
		t.Fatalf("sdiv: got %#x want %#x", c.R[0], uint32(0xFFFFFFFD))
	}
}

func TestFlags(t *testing.T) {
	// cmp r0, r1 (16-bit ALU CMP: 0x4288 family)
	c := run16(t, 0x4288) // cmp r0, r1
	if !c.flag(flagZ) {
		t.Fatalf("cmp equal should set Z")
	}
	// subs r0, #1 with r0 = 1 -> Z set and C set
	bus := newTestBus()
	bus.Write16(0, 0x3801) // subs r0, #1
	c = New(bus)
	c.R[15] = 0
	c.R[0] = 1
	c.Step()
	if !c.flag(flagZ) || !c.flag(flagC) {
		t.Fatalf("subs 1-1: Z=%v C=%v want Z=1 C=1", c.flag(flagZ), c.flag(flagC))
	}
	// 0-1 must clear carry (borrow) and Z
	bus = newTestBus()
	bus.Write16(0, 0x3801)
	c = New(bus)
	c.R[15] = 0
	c.R[0] = 0
	c.Step()
	if c.flag(flagZ) || c.flag(flagC) {
		t.Fatalf("subs 0-1: Z=%v C=%v want Z=0 C=0", c.flag(flagZ), c.flag(flagC))
	}
}

func TestBranchTargets(t *testing.T) {
	// b #+8 (16-bit) at address 0 -> target 0x8
	bus := newTestBus()
	bus.Write16(0, 0xE002) // b +4 -> pc+4+4 = 8
	c := New(bus)
	c.R[15] = 0
	c.Step()
	if c.R[15] != 8 {
		t.Fatalf("b: pc=%#x want 8", c.R[15])
	}

	// beq with imm8=3 at address 0 -> pc + 4 + 6 = 0xa
	bus = newTestBus()
	bus.Write16(0, 0xD003)
	c = New(bus)
	c.R[15] = 0
	c.APSR |= flagZ
	c.Step()
	if c.R[15] != 0xA {
		t.Fatalf("beq: pc=%#x want 0xa", c.R[15])
	}
}

// TestWideConditionalBranches covers the F3xx encodings that collide with the
// system/bitfield instruction space: BGT.W (0xF300) and BLE.W (0xF340).
func TestWideConditionalBranches(t *testing.T) {
	// bgt.w #0x100: Z == 0 and N == V.
	c := run32(t, 0xF300, 0x8080)
	if c.R[15] != 0x104 {
		t.Fatalf("bgt.w: pc=%#x want 0x104", c.R[15])
	}

	// ble.w #0x100: Z == 1.
	c = run32With(t, 0xF340, 0x8080, flagZ)
	if c.R[15] != 0x104 {
		t.Fatalf("ble.w: pc=%#x want 0x104", c.R[15])
	}

	// sbfx r0, r1, #4, #8 must still decode as a bitfield instruction.
	bus := newTestBus()
	bus.Write16(0, 0xF341)
	bus.Write16(2, 0x1007)
	bus.Write16(4, 0xBF00)
	c = New(bus)
	c.R[15] = 0
	c.R[1] = 0x0000_0F80
	c.Step()
	if c.R[15] != 4 {
		t.Fatalf("sbfx: pc=%#x want 4", c.R[15])
	}
	if c.R[0] != 0xFFFFFFF8 {
		t.Fatalf("sbfx: r0=%#x want 0xfffffff8", c.R[0])
	}
}

func run32With(t *testing.T, hw1, hw2 uint16, flags uint32) *CPU {
	t.Helper()
	bus := newTestBus()
	bus.Write16(0, hw1)
	bus.Write16(2, hw2)
	bus.Write16(4, 0xBF00)
	c := New(bus)
	c.R[15] = 0
	c.APSR |= flags
	c.Step()
	return c
}

func TestITBlock(t *testing.T) {
	// it eq ; movs r0, #1  (taken)
	bus := newTestBus()
	bus.Write16(0, 0xBF08)
	bus.Write16(2, 0x2001)
	c := New(bus)
	c.R[15] = 0
	c.APSR |= flagZ
	c.Step() // IT
	c.Step() // movs r0
	if c.R[0] != 1 {
		t.Fatalf("it eq: r0=%d want 1", c.R[0])
	}

	// it ne ; movs r1, #2  (skipped when Z set)
	bus = newTestBus()
	bus.Write16(0, 0xBF18)
	bus.Write16(2, 0x2102)
	c = New(bus)
	c.R[15] = 0
	c.APSR |= flagZ
	c.Step() // IT
	c.Step() // skipped
	if c.R[1] != 0 {
		t.Fatalf("it ne: r1=%d want 0", c.R[1])
	}
}

// TestBICShiftedASR32Carry covers the compiler's boolean-select idiom
// `bics r0, r0, r2, asr #32`, where the carry is the sign of r2.
func TestBICShiftedASR32Carry(t *testing.T) {
	run := func(r2 uint32) *CPU {
		bus := newTestBus()
		bus.Write16(0, 0xEA30) // bics.w r0, r0, r2, asr #32
		bus.Write16(2, 0x0022)
		bus.Write16(4, 0xBF00)
		c := New(bus)
		c.R[15] = 0
		c.R[0] = 0x1000
		c.R[2] = r2
		c.Step()
		return c
	}
	neg := run(0xFFFFFFFF)
	if neg.R[0] != 0 {
		t.Fatalf("negative select: r0 = %#x, want 0", neg.R[0])
	}
	if !neg.flag(flagC) {
		t.Fatal("negative select did not set carry")
	}
	pos := run(1)
	if pos.R[0] != 0x1000 {
		t.Fatalf("positive select: r0 = %#x, want 0x1000", pos.R[0])
	}
	if pos.flag(flagC) {
		t.Fatal("positive select set carry")
	}
}
