package rp2350

import "testing"

// The Pico SDK reads the bootrom lookup pointer from 0x16 and calls it. Keep the
// table and the LDRH path honest: a regression here makes every C image fault in
// runtime_init.
func TestBootROMTable(t *testing.T) {
	c := New()
	if got := c.Read16(bootromFuncTablePtr); got == 0 {
		t.Fatalf("function table pointer at %#x is null", bootromFuncTablePtr)
	}
	lookup := c.Read16(bootromLookupPtr)
	if lookup&1 == 0 {
		t.Fatalf("lookup pointer %#x is not a Thumb function pointer", lookup)
	}
	if lookup&^1 >= ROMSize {
		t.Fatalf("lookup pointer %#x is outside the ROM", lookup)
	}
	if got := c.Read32(bootromMagicOffset); got != bootromMagic {
		t.Fatalf("magic = %#x, want %#x", got, bootromMagic)
	}
}

// TestBootROMLookupExec drives rom_func_lookup through the CPU: the lookup stub
// must return the SR hook as a Thumb pointer.
func TestBootROMLookupExec(t *testing.T) {
	c := New()
	c.Boot()
	const codeAddr = 0x20000000
	// ldr r3, [pc, #8] / bx r3 / nops / .word lookup (Thumb-1).
	prog := []byte{
		0x02, 0x4b, // ldr r3, [pc, #8]  (0x2000000c)
		0x18, 0x47, // bx r3
		0x00, 0xbf, // nop
		0x00, 0xbf, // nop
		0x00, 0xbf, // nop
		0x00, 0xbf, // nop
	}
	c.LoadSegment(codeAddr, prog)
	// The literal at code+12 is the lookup address, filled below.
	lookup := c.Read16(bootromLookupPtr) &^ 1
	c.LoadSegment(codeAddr+12, []byte{byte(lookup), byte(lookup >> 8), 0, 0})

	cpu := c.CPU0
	cpu.R[15] = codeAddr
	cpu.R[0] = romCode("SR")
	cpu.R[14] = 0x20000100 // sentinel return address
	for i := 0; i < 4 && !cpu.Halted; i++ {
		c.Step()
	}
	if cpu.Halted {
		t.Fatalf("halted: %s", cpu.HaltReason)
	}
	if cpu.R[0]&1 == 0 || cpu.R[0]&^1 >= ROMSize {
		t.Fatalf("lookup returned %#x, want a Thumb ROM pointer", cpu.R[0])
	}
}

// TestLDRHFromBootROM executes the exact instruction the SDK's
// rom_func_lookup uses to read the lookup pointer.
func TestLDRHFromBootROM(t *testing.T) {
	c := New()
	c.Boot()
	const codeAddr = 0x20000000
	c.LoadSegment(codeAddr, []byte{
		0xdb, 0x8a, // ldrh r3, [r3, #0x16]
		0x23, 0x60, // str  r3, [r4]
		0xfe, 0xe7, // b .
	})
	cpu := c.CPU0
	if hw := c.Read16(codeAddr); hw != 0x8adb {
		t.Fatalf("opcode = %#x", hw)
	}
	if v := c.Read16(0x16); v == 0 {
		t.Fatalf("ROM lookup pointer is null before stepping")
	}
	cpu.R[3] = 0
	cpu.R[4] = 0x20000010
	cpu.R[15] = codeAddr
	for i := 0; i < 3 && !cpu.Halted; i++ {
		c.Step()
	}
	if got := c.Read32(0x20000010); got != uint32(c.Read16(bootromLookupPtr)) {
		t.Fatalf("ldrh stored %#x, want %#x", got, c.Read16(bootromLookupPtr))
	}
}
