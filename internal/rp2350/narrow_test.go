package rp2350

import "testing"

func TestNarrowRegisterWritesReplicate(t *testing.T) {
	c := New()
	c.Write8(0x40020000, 0x12) // RESETS register 0
	if got := c.Read32(0x40020000); got != 0x12121212 {
		t.Fatalf("byte register write = %#x, want 0x12121212", got)
	}
	c.Write16(0x40020004, 0x3456) // RESETS register 1
	if got := c.Read32(0x40020004); got != 0x34563456 {
		t.Fatalf("halfword register write = %#x, want 0x34563456", got)
	}
}

func TestNarrowAtomicAliasesReplicate(t *testing.T) {
	c := New()
	c.Write8(0x40020000+0x2000, 0x01) // RESETS SET alias
	if got := c.Read32(0x40020000); got != 0x01010101 {
		t.Fatalf("narrow SET alias = %#x, want 0x01010101", got)
	}
	c.Write16(0x40020000+0x3000, 0x00ff) // RESETS CLR alias
	if got := c.Read32(0x40020000); got != 0x01000100 {
		t.Fatalf("narrow CLR alias = %#x, want 0x01000100", got)
	}
}

func TestNarrowWritesDoNotConsumeReadSensitiveState(t *testing.T) {
	t.Run("uart", func(t *testing.T) {
		c := New()
		c.SendUART(0, []byte{0x41})
		c.Write8(0x40070001, 0x42)
		if got := c.Read8(0x40070000); got != 0x41 {
			t.Fatalf("UART write consumed RX byte: got %#x, want 0x41", got)
		}
	})
	t.Run("spi", func(t *testing.T) {
		c := New()
		c.OnSPI0 = func(tx byte) byte { return tx + 1 }
		c.Write32(0x40080008, 0xaa) // queue 0xab
		c.Write8(0x40080009, 0xcc)  // must not pop 0xab
		if got := c.Read32(0x40080008); got != 0xab {
			t.Fatalf("SPI write consumed RX FIFO: got %#x, want 0xab", got)
		}
	})
	t.Run("sio", func(t *testing.T) {
		c := New()
		c.core = 1
		c.sio.fifo[1] = append(c.sio.fifo[1], 0x12345678)
		c.Write8(SIOBase+0x59, 0)
		if got := c.Read32(SIOBase + 0x58); got != 0x12345678 {
			t.Fatalf("SIO write consumed FIFO: got %#x", got)
		}
	})
}

func TestByteAddressedMemoryKeepsNarrowWrites(t *testing.T) {
	c := New()
	c.Write32(usbDPRAMBase+0x40, 0x11223344)
	c.Write8(usbDPRAMBase+0x41, 0xaa)
	if got := c.Read32(usbDPRAMBase + 0x40); got != 0x1122aa44 {
		t.Fatalf("DPRAM byte write = %#x, want 0x1122aa44", got)
	}

	c.Write32(bootramBase+0x100, 0x11223344)
	c.Write16(bootramBase+0x102, 0xaabb)
	if got := c.Read32(bootramBase + 0x100); got != 0xaabb3344 {
		t.Fatalf("boot RAM halfword write = %#x, want 0xaabb3344", got)
	}
}
