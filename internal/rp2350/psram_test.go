package rp2350

import "testing"

func TestPSRAMWindowWriteGateAndLatency(t *testing.T) {
	c := New()
	c.ConfigurePSRAM(8*1024*1024, 5, 8)

	// Window 1 is read-only until XIP_CTRL.WRITABLE_M1 is set: the write is
	// dropped but still costs an external access.
	before := c.Cycles
	c.Write32(PSRAMBase, 0xdeadbeef)
	if got := c.Cycles - before; got != 5 {
		t.Fatalf("read-only write cost = %d cycles, want 5", got)
	}
	if v := c.Read32(PSRAMBase); v != 0 {
		t.Fatalf("read-only window stored %#x", v)
	}

	c.Write32(xipBase, xipCtrlWritableM1)
	before = c.Cycles
	c.Write32(PSRAMBase, 0xdeadbeef)
	if got := c.Cycles - before; got != 5 {
		t.Fatalf("write cost = %d cycles, want 5", got)
	}
	before = c.Cycles
	if v := c.Read32(PSRAMNoCacheBase); v != 0xdeadbeef {
		t.Fatalf("uncached read = %#x, want 0xdeadbeef", v)
	}
	if got := c.Cycles - before; got != 5 {
		t.Fatalf("read cost = %d cycles, want 5", got)
	}
	if v := c.Read32(PSRAMMaintBase); v != 0 {
		t.Fatalf("maintenance read = %#x, want 0", v)
	}
	if v := c.Read32(PSRAMNoXlateBase); v != 0xdeadbeef {
		t.Fatalf("no-translate read = %#x, want 0xdeadbeef", v)
	}
}

// A ranged cache-maintenance sweep inside the PSRAM maintenance window must
// not alias the data array: the window carries cache operations, so writes are
// no-ops and reads are not data.
func TestPSRAMMaintenanceWindowDoesNotAliasData(t *testing.T) {
	c := New()
	c.ConfigurePSRAM(8*1024*1024, 0, 8)
	c.Write32(xipBase, xipCtrlWritableM1)
	c.Write32(PSRAMBase, 0xdeadbeef)

	c.Write32(PSRAMMaintBase, 0)
	c.Write32(PSRAMMaintBase+0x4000, 0xffffffff)

	if got := c.Read32(PSRAMBase); got != 0xdeadbeef {
		t.Fatalf("maintenance write corrupted PSRAM: %#x", got)
	}
	if got := c.Read32(PSRAMMaintBase); got != 0 {
		t.Fatalf("maintenance read = %#x, want 0", got)
	}
	if len(c.Unhandled) != 0 {
		t.Fatalf("maintenance window reported unhandled accesses: %v", c.Unhandled)
	}
}

func TestPSRAMSizeLimitAndDevinfo(t *testing.T) {
	c := New()
	c.ConfigurePSRAM(2*1024*1024, 0, 19)
	if got := c.PSRAMSize(); got != 2*1024*1024 {
		t.Fatalf("size = %d", got)
	}
	devinfo := c.Read16(bootramBase + bootramFlashDevinfo)
	if want := uint16(9)<<12 | 12<<8 | 19; devinfo != want {
		t.Fatalf("FLASH_DEVINFO = %#x, want %#x", devinfo, want)
	}
	c.Write32(xipBase, xipCtrlWritableM1)
	c.Write32(PSRAMBase+2*1024*1024, 1)
	if c.Unhandled[PSRAMBase+2*1024*1024] == 0 {
		t.Fatal("an access past the configured size was not reported")
	}

	c.ConfigurePSRAM(0, 0, 0)
	if got := c.Read16(bootramBase + bootramFlashDevinfo); got != 12<<8 {
		t.Fatalf("devinfo without PSRAM = %#x, want default CS0 size", got)
	}
}

func TestQMIProvidesPSRAMID(t *testing.T) {
	c := New()
	c.ConfigurePSRAM(8*1024*1024, 0, 8)

	c.Write32(qmiBase, 1|1<<7) // DIRECT_CSR.EN | AUTO_CS1N
	if v := c.Read32(qmiBase); v&(1<<1) != 0 {
		t.Fatalf("DIRECT_CSR reports busy: %#x", v)
	}
	c.Write32(qmiBase+4, 0x9f)
	for range 7 {
		c.Write32(qmiBase+4, 0xff)
	}
	var got [8]byte
	for i := range got {
		got[i] = byte(c.Read32(qmiBase + 8))
	}
	if got[5] != psramIDKGD || got[6] != psramIDEID {
		t.Fatalf("JEDEC ID = % x, want KGD %#x EID %#x", got, psramIDKGD, psramIDEID)
	}
	if v := c.Read32(qmiBase); v&(1<<16) == 0 {
		t.Fatalf("DIRECT_CSR should be RXEMPTY after the queue drains: %#x", v)
	}
}

func TestXIPCacheMaintenanceIsNoOp(t *testing.T) {
	c := New()
	// The SDK's cache clean-all writes the set/way sweep at this address.
	const sweep = XIPNoTranslateBase - 16*1024
	c.Write8(sweep, 0)
	const reserved = XIPMaintenanceBase + 0x02000000 + 0x1234
	c.Write8(reserved, 0)
	if len(c.Unhandled) != 0 {
		t.Fatalf("maintenance window reported unhandled accesses: %v", c.Unhandled)
	}
	if v := c.Read32(reserved); v != 0 {
		t.Fatalf("maintenance read = %#x", v)
	}
}
