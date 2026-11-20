package rp2350

import (
	"encoding/binary"
	"testing"
)

func TestFlashErasedAndProgrammed(t *testing.T) {
	c := New()
	for i, b := range c.Flash {
		if b != 0xFF {
			t.Fatalf("flash[%d] = %#02x, want 0xFF", i, b)
		}
	}
	if !c.programFlashRange(0x10000, []byte{0x0F, 0xF0}) {
		t.Fatal("program failed")
	}
	if c.Flash[0x10000] != 0x0F || c.Flash[0x10001] != 0xF0 {
		t.Fatalf("flash = %#02x %#02x", c.Flash[0x10000], c.Flash[0x10001])
	}
	// Programming can only clear bits.
	if !c.programFlashRange(0x10000, []byte{0xF0, 0x0F}) {
		t.Fatal("second program failed")
	}
	if c.Flash[0x10000] != 0x00 || c.Flash[0x10001] != 0x00 {
		t.Fatalf("bits set by programming: %#02x %#02x", c.Flash[0x10000], c.Flash[0x10001])
	}
	if !c.eraseFlashRange(0x10000, 0x1000) {
		t.Fatal("erase failed")
	}
	if c.Flash[0x10000] != 0xFF {
		t.Fatalf("erase left %#02x", c.Flash[0x10000])
	}
	if c.FlashWrites() == 0 {
		t.Fatal("no write operations counted")
	}
	if c.eraseFlashRange(FlashSize-1, 2) {
		t.Fatal("out-of-range erase accepted")
	}
}

func TestBootROMFlashServices(t *testing.T) {
	c := New()
	cpu := c.CPU0
	call := func(code string) {
		addr := c.rom.funcs[romCode(code)]
		stub, ok := c.rom.hooks[addr]
		if !ok {
			t.Fatalf("no ROM hook for %s", code)
		}
		stub(c, cpu)
	}

	// RE takes a flash offset, not a bus address.
	cpu.R[0] = 0x20000
	cpu.R[1] = 0x1000
	cpu.R[2] = 0x1000
	cpu.R[3] = 0x20
	call("RE")
	if cpu.R[0] != bootromOK {
		t.Fatalf("RE = %d", cpu.R[0])
	}
	if c.Flash[0x20000] != 0xFF {
		t.Fatalf("RE did not erase: %#02x", c.Flash[0x20000])
	}

	// RP copies from a buffer in SRAM and clears bits only.
	copy(c.SRAM[0x100:], []byte{0x12, 0x34, 0x56, 0x78})
	cpu.R[0] = 0x20000
	cpu.R[1] = SRAMBase + 0x100
	cpu.R[2] = 4
	call("RP")
	if cpu.R[0] != bootromOK {
		t.Fatalf("RP = %d", cpu.R[0])
	}
	if got := binary.LittleEndian.Uint32(c.Flash[0x20000:]); got != 0x78563412 {
		t.Fatalf("flash = %#08x", got)
	}

	// FO read returns the same bytes.
	cpu.R[0] = 2 << 16 // read, storage address
	cpu.R[1] = 0x20000
	cpu.R[2] = 4
	cpu.R[3] = SRAMBase + 0x200
	call("FO")
	if cpu.R[0] != bootromOK {
		t.Fatalf("FO = %d", cpu.R[0])
	}
	if got := binary.LittleEndian.Uint32(c.SRAM[0x200:]); got != 0x78563412 {
		t.Fatalf("FO read = %#08x", got)
	}
}

func TestBootCountAndReset(t *testing.T) {
	c := New()
	// First image at flash base.
	binary.LittleEndian.PutUint32(c.Flash[0:], SRAMBase+0x1000)
	binary.LittleEndian.PutUint32(c.Flash[4:], FlashBase+0x101)
	c.Boot()
	if c.BootCount() != 1 || c.BootEntry() != FlashBase+0x100 || c.LastResetReason() != "power-on" {
		t.Fatalf("first boot: count=%d entry=%#x reason=%q", c.BootCount(), c.BootEntry(), c.LastResetReason())
	}
	// Replace the vector table and request a software reset.
	copy(c.Flash[:8], []byte{0x00, 0x10, 0x00, 0x20, 0x01, 0x02, 0x00, 0x10})
	c.requestReset("software", 0)
	c.Step()
	if c.BootCount() != 2 {
		t.Fatalf("boot count = %d, want 2", c.BootCount())
	}
	if c.LastResetReason() != "software" {
		t.Fatalf("reason = %q", c.LastResetReason())
	}
	if c.CPU0.R[15] != 0x10000200 {
		t.Fatalf("PC = %#x, want 0x10000200", c.CPU0.R[15])
	}
}

func TestWatchdogTriggersReset(t *testing.T) {
	c := New()
	binary.LittleEndian.PutUint32(c.Flash[0:], SRAMBase+0x1000)
	binary.LittleEndian.PutUint32(c.Flash[4:], FlashBase+0x101)
	c.Boot()
	c.Write32(0x400d8000, wdCtrlTrigger)
	c.Step()
	if c.BootCount() != 2 {
		t.Fatalf("boot count = %d, want 2", c.BootCount())
	}
	if c.LastResetReason() != "watchdog" {
		t.Fatalf("reason = %q", c.LastResetReason())
	}
	if c.wd == nil || c.wd.reason&wdReasonForce == 0 {
		t.Fatalf("watchdog reason = %#x", c.wd.reason)
	}
}
