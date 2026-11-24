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

// The +0x4000 alias selects the bit-14 zero-fill mode (datasheet 2.1.5):
// un-addressed lanes are zero rather than a replication of the narrow value.
func TestNarrowZeroFillAlias(t *testing.T) {
	const resets = 0x40020000
	c := New()
	c.Write8(resets+0x4000+1, 0x5a)
	if got := c.Read32(resets); got != 0x00005a00 {
		t.Fatalf("zero-fill byte = %#x, want 0x00005a00", got)
	}
	c.Write32(resets, 0)
	c.Write16(resets+0x4000+2, 0x1234)
	if got := c.Read32(resets); got != 0x12340000 {
		t.Fatalf("zero-fill halfword = %#x, want 0x12340000", got)
	}

	// The qualifier composes with the atomic aliases: a SET alias ORs only
	// the addressed lane.
	c.Write32(resets, 0)
	c.Write8(resets+0x2000+0x4000+1, 0x01)
	if got := c.Read32(resets); got != 0x00000100 {
		t.Fatalf("zero-fill SET alias = %#x, want 0x00000100", got)
	}
}

// DMA uses the same zero-fill qualifier as the CPU.
func TestDMAZeroFillAliasWrite(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	const scratch = 0x400D800C // watchdog scratch0
	dmaConfigure(c, 0, 0x20000000, scratch+0x4000+1, 1, 0)
	c.Write8(0x20000000, 0x5a)
	c.Write32(dmaBase+0x450, 1)
	c.Step()
	if got := c.Read32(scratch); got != 0x00005a00 {
		t.Fatalf("DMA zero-fill byte write = %#x, want 0x00005a00", got)
	}
}

// SIO is an IO register block: narrow writes broadcast replicated data, so a
// byte write at any offset in the containing word behaves like a word write.
func TestSIONarrowWritesBroadcast(t *testing.T) {
	t.Run("fifo", func(t *testing.T) {
		c := New()
		c.sio.core1Parked = false
		c.core = 0
		c.Write8(SIOBase+0x54, 0x5a)
		c.core = 1
		if got := c.Read32(SIOBase + 0x58); got != 0x5a5a5a5a {
			t.Fatalf("FIFO byte write = %#x, want 0x5a5a5a5a", got)
		}
	})
	t.Run("doorbell", func(t *testing.T) {
		c := New()
		c.core = 0
		c.Write8(SIOBase+0x181, 0x04) // DOORBELL_OUT_SET at a non-zero offset
		c.core = 1
		if got := c.Read32(SIOBase + 0x188); got != 0x4 {
			t.Fatalf("doorbell byte write = %#x, want 0x4", got)
		}
	})
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
