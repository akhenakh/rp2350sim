package m33

import "testing"

// newExecBus returns a bus with enough RAM for a short program and data.
func newExecBus() *testBus { return newTestBusAt(0x20000000, 1<<16) }

// TestLDRWImm12HighBit guards the T3/T4 split in the F8 load/store group: an
// LDR.W with imm12 bit 11 set used to be decoded as the imm8 form, reading the
// wrong address. The Pico SDK hits this on [rX, #0x828].
func TestLDRWImm12HighBit(t *testing.T) {
	bus := newExecBus()
	cpu := New(bus)
	// ldr.w r3, [r2, #0x828] / str r3, [r4] / b .
	prog := []byte{0xd2, 0xf8, 0x28, 0x38, 0x23, 0x60, 0xfe, 0xe7}
	copy(bus.mem, prog)
	bus.Write32(0x20008828, 0x80) // the value must be fetched through the bus
	cpu.R[2] = 0x20008000
	cpu.R[4] = 0x20001000
	cpu.R[15] = 0x20000000
	for range 2 {
		cpu.Step()
	}
	if got := bus.Read32(0x20001000); got != 0x80 {
		t.Fatalf("loaded %#x, want 0x80", got)
	}
}

// TestLDRWPCThumbBit: loading PC from a literal must mask the Thumb bit, as
// the Pico SDK's long-call veneers do (`ldr.w pc, [pc, #0]`).
func TestLDRWPCThumbBit(t *testing.T) {
	bus := newExecBus()
	cpu := New(bus)
	// ldr.w pc, [pc, #0] / .word 0x20000021
	prog := []byte{0x5f, 0xf8, 0x00, 0xf0, 0x21, 0x00, 0x00, 0x20}
	copy(bus.mem, prog)
	cpu.R[15] = 0x20000000
	cpu.Step()
	if cpu.R[15] != 0x20000020 {
		t.Fatalf("pc = %#x, want 0x20000020", cpu.R[15])
	}
}

// TestExclusiveByte round-trips LDAEXB/STREXB, used by the SDK's lock_core
// byte spinlocks.
func TestExclusiveByte(t *testing.T) {
	bus := newExecBus()
	cpu := New(bus)
	// ldaexb r2, [r3] / strexb r0, r2, [r3] / strexb r1, r2, [r3] / b .
	prog := []byte{
		0xd3, 0xe8, 0xcf, 0x2f,
		0xc3, 0xe8, 0x40, 0x2f,
		0xc3, 0xe8, 0x41, 0x2f,
		0xfe, 0xe7,
	}
	copy(bus.mem, prog)
	bus.Write8(0x20001000, 0x5a)
	cpu.R[3] = 0x20001000
	cpu.R[15] = 0x20000000
	cpu.Step() // load exclusive
	if cpu.R[2] != 0x5a {
		t.Fatalf("ldaexb = %#x, want 0x5a", cpu.R[2])
	}
	cpu.R[2] = 0xa5
	cpu.Step() // store succeeds
	if cpu.R[0] != 0 || bus.Read8(0x20001000) != 0xa5 {
		t.Fatalf("strexb status=%d mem=%#x", cpu.R[0], bus.Read8(0x20001000))
	}
	cpu.Step() // second store without a load must fail
	if cpu.R[1] != 1 {
		t.Fatalf("second strexb status = %d, want 1", cpu.R[1])
	}
}

// TestStoreReleaseByte covers STLB, emitted by spin_locks_reset.
func TestStoreReleaseByte(t *testing.T) {
	bus := newExecBus()
	cpu := New(bus)
	// stlb r1, [r3] / b .
	copy(bus.mem, []byte{0xc3, 0xe8, 0x8f, 0x1f, 0xfe, 0xe7})
	cpu.R[3] = 0x20001000
	cpu.R[1] = 0x7e
	cpu.R[15] = 0x20000000
	cpu.Step()
	if got := bus.Read8(0x20001000); got != 0x7e {
		t.Fatalf("stlb stored %#x", got)
	}
}

// TestPKHBT covers the packed-halfword insert instructions emitted by the
// TinyGo math/PWM helpers.
func TestPKHBT(t *testing.T) {
	bus := newExecBus()
	cpu := New(bus)
	// pkhbt r1, r2, r3, lsl #16 / pkhtb r1, r2, r3, asr #16 / b .
	prog := []byte{
		0xc2, 0xea, 0x03, 0x41,
		0xc2, 0xea, 0x23, 0x41,
		0xfe, 0xe7,
	}
	copy(bus.mem, prog)
	cpu.R[2] = 0x11112222
	cpu.R[3] = 0x33334444
	cpu.R[15] = 0x20000000
	cpu.Step()
	if cpu.R[1] != 0x22224444 {
		t.Fatalf("pkhbt = %#x, want 0x22224444", cpu.R[1])
	}
	cpu.Step()
	if cpu.R[1] != 0x11113333 {
		t.Fatalf("pkhtb = %#x, want 0x11113333", cpu.R[1])
	}
}
