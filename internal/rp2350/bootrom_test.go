package rp2350

import (
	"encoding/binary"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/m33"
)

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
	if got := c.Read32(bootromMagicOffset); got != bootromMagic|uint32(bootromVersion)<<24 {
		t.Fatalf("magic/version = %#x, want %#x", got, bootromMagic|uint32(bootromVersion)<<24)
	}
	if got := c.ROM[bootromMagicOffset : bootromMagicOffset+3]; got[0] != 'M' || got[1] != 'u' || got[2] != 2 {
		t.Fatalf("magic bytes = % x", got)
	}
	if got := c.ROM[bootromMagicOffset+3]; got != bootromVersion {
		t.Fatalf("version byte = %#x, want %#x", got, bootromVersion)
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

func TestBootromRebootDelayIsMilliseconds(t *testing.T) {
	c := New()
	c.Cycles = 1000
	c.CPU0.R[0] = 0
	c.CPU0.R[1] = 3
	c.rom.stubReboot(c, c.CPU0)
	want := uint64(3) * cyclesPerMillisecond
	if got := c.pendingResetAt - c.Cycles; got != want {
		t.Fatalf("reboot delay = %d cycles, want %d", got, want)
	}
}

func TestBootROMGetSysInfoBootRandom(t *testing.T) {
	c := New()
	const out = SRAMBase + 0x100
	cpu := c.CPU0
	cpu.R[0], cpu.R[1], cpu.R[2] = out, 5, sysInfoBootRandom
	c.rom.stubGetSysInfo(c, cpu)
	if cpu.R[0] != 5 {
		t.Fatalf("get_sys_info returned %d words, want 5", cpu.R[0])
	}
	if got := c.Read32(out); got != sysInfoBootRandom {
		t.Fatalf("selector mask = %#x, want %#x", got, sysInfoBootRandom)
	}
}

func TestBootROMFlashAlignment(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Chip, *m33.CPU)
	}{
		{name: "erase offset", setup: func(c *Chip, cpu *m33.CPU) {
			cpu.R[0], cpu.R[1] = 1, 4096
			c.rom.stubFlashErase(c, cpu)
		}},
		{name: "erase size", setup: func(c *Chip, cpu *m33.CPU) {
			cpu.R[0], cpu.R[1] = 0, 1
			c.rom.stubFlashErase(c, cpu)
		}},
		{name: "program offset", setup: func(c *Chip, cpu *m33.CPU) {
			cpu.R[0], cpu.R[1], cpu.R[2] = 1, SRAMBase+0x100, 256
			c.rom.stubFlashProgram(c, cpu)
		}},
		{name: "program size", setup: func(c *Chip, cpu *m33.CPU) {
			cpu.R[0], cpu.R[1], cpu.R[2] = 0, SRAMBase+0x100, 1
			c.rom.stubFlashProgram(c, cpu)
		}},
		{name: "flash_op erase", setup: func(c *Chip, cpu *m33.CPU) {
			cpu.R[0], cpu.R[1], cpu.R[2] = 0, 1, 4096
			c.rom.stubFlashOp(c, cpu)
		}},
		{name: "flash_op program", setup: func(c *Chip, cpu *m33.CPU) {
			cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3] = 1<<16, 0, 1, SRAMBase+0x100
			c.rom.stubFlashOp(c, cpu)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.setup(c, c.CPU0)
			if got := c.CPU0.R[0]; got != bootromErrBadAlignment {
				t.Fatalf("result = %d, want BOOTROM_ERROR_BAD_ALIGNMENT", got)
			}
		})
	}

	c := New()
	c.CPU0.R[0], c.CPU0.R[1], c.CPU0.R[2], c.CPU0.R[3] = 2<<16, 1, 1, SRAMBase+0x100
	c.rom.stubFlashOp(c, c.CPU0)
	if got := c.CPU0.R[0]; got != bootromOK {
		t.Fatalf("unaligned flash_op read = %d, want OK", got)
	}
}

func TestBootROMRuntimeToStorageAddress(t *testing.T) {
	c := New()
	cpu := c.CPU0
	cpu.R[0] = FlashBase + 0x1234
	c.rom.stubFlashRuntimeToStorageAddr(c, cpu)
	if cpu.R[0] != 0x1234 {
		t.Fatalf("flat translation = %#x, want 0x1234", cpu.R[0])
	}
	for _, addr := range []uint32{FlashBase - 1, FlashBase + FlashSize, SRAMBase} {
		cpu.R[0] = addr
		c.rom.stubFlashRuntimeToStorageAddr(c, cpu)
		if cpu.R[0] != bootromErrInvalidAddr {
			t.Fatalf("translation of %#x = %#x", addr, cpu.R[0])
		}
	}

	c = buildPartitionFlash(t, 0)
	c.Boot() // slot B at storage offset 0x300000
	cpu = c.CPU0
	cpu.R[0] = FlashBase + 0x1234
	c.rom.stubFlashRuntimeToStorageAddr(c, cpu)
	if cpu.R[0] != 0x301234 {
		t.Fatalf("partition translation = %#x, want 0x301234", cpu.R[0])
	}
	cpu.R[0] = FlashBase + c.partSize
	c.rom.stubFlashRuntimeToStorageAddr(c, cpu)
	if cpu.R[0] != bootromErrInvalidAddr {
		t.Fatalf("partition boundary translation = %#x", cpu.R[0])
	}
}

func TestBootROMRebootNoReturnParksUntilReset(t *testing.T) {
	c := New()
	binary.LittleEndian.PutUint32(c.Flash[0:], SRAMBase+0x1000)
	binary.LittleEndian.PutUint32(c.Flash[4:], FlashBase+0x101)
	copy(c.Flash[0x100:], []byte{0xfe, 0xe7}) // b .
	c.Boot()
	cpu := c.CPU0
	const sentinel = SRAMBase + 0x200
	cpu.R[0], cpu.R[1], cpu.R[14] = rebootNoReturnOnSuccess, 3, sentinel
	addr := c.rom.funcs[romCode("RB")]
	if !c.rom.exec(c, cpu, addr) {
		t.Fatal("reboot ROM hook was not handled")
	}
	if !cpu.Parked || !c.pendingReset {
		t.Fatal("NO_RETURN_ON_SUCCESS did not park the caller and request reset")
	}
	c.Step()
	if cpu.R[15] != sentinel {
		t.Fatalf("parked caller executed: PC = %#x", cpu.R[15])
	}
	c.Run(3 * cyclesPerMillisecond)
	if c.BootCount() != 2 || cpu.Parked {
		t.Fatalf("caller was not released by reset: boots=%d parked=%v", c.BootCount(), cpu.Parked)
	}
}
