package rp2350

import "testing"

const dmaBase = 0x50000000

func dmaChanReg(c *Chip, ch, reg int) uint32 {
	return c.Read32(dmaBase + uint32(ch*0x40+reg*4))
}

func dmaConfigure(c *Chip, ch int, src, dst, count, ctrl uint32) {
	base := dmaBase + uint32(ch*0x40)
	c.Write32(base+0x00, src)
	c.Write32(base+0x04, dst)
	c.Write32(base+0x08, count)
	if ctrl&(0xF<<13) == 0 {
		ctrl |= uint32(ch) << 13 // disable the reset-default chain to channel 0
	}
	c.Write32(base+0x10, ctrl|dmaCtrlEN) // AL1_CTRL does not trigger
}

// TestDMAPacedTransfer checks that a transfer takes one element per cycle,
// reports BUSY meanwhile, and raises the channel's completion interrupt.
func TestDMAPacedTransfer(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true

	src := uint32(0x20000000)
	dst := uint32(0x20000100)
	for i := range 4 {
		c.Write8(src+uint32(i), byte(0xA0+i))
	}

	c.Write32(dmaBase+0x00, src)
	c.Write32(dmaBase+0x04, dst)
	c.Write32(dmaBase+0x08, 4)
	c.Write32(dmaBase+0x404, 1) // INTE0: channel 0
	c.Write32(dmaBase+0x0C, dmaCtrlEN|dmaCtrlINCRRead|dmaCtrlINCRWrit)

	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("BUSY not set after trigger")
	}
	if c.Read8(dst) == 0xA0 {
		t.Fatal("transfer completed before any cycle")
	}

	for range 4 {
		c.Step()
	}
	for i := range 4 {
		if got := c.Read8(dst + uint32(i)); got != byte(0xA0+i) {
			t.Fatalf("byte %d = %#x, want %#x", i, got, 0xA0+i)
		}
	}
	if ctrl := dmaChanReg(c, 0, 3); ctrl&dmaCtrlBusy != 0 || ctrl&dmaCtrlEN == 0 {
		t.Fatalf("CTRL_TRIG busy/enable = %#x, want BUSY clear and EN set", ctrl)
	}
	if dmaChanReg(c, 0, 2) != 0 {
		t.Fatal("TRANS_COUNT not cleared")
	}
	if c.dma.pending(0)&1 == 0 {
		t.Fatal("completion interrupt not raised")
	}
	if c.NVIC.pending[0]&(1<<10) == 0 {
		t.Fatal("DMA IRQ 0 not pending in the NVIC")
	}
	// Writing INTR clears the channel bit.
	c.Write32(dmaBase+0x400, 1)
	if c.dma.intr&1 != 0 {
		t.Fatal("INTR not cleared by write-1")
	}
}

// TestDMAIRQQuiet checks that IRQ_QUIET suppresses the completion interrupt.
func TestDMAIRQQuiet(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x42)
	c.Write32(dmaBase+0x40, 0x20000000)
	c.Write32(dmaBase+0x44, 0x20000200)
	c.Write32(dmaBase+0x48, 1)
	c.Write32(dmaBase+0x414, 1) // INTE1: channel 1
	c.Write32(dmaBase+0x4C, dmaCtrlEN|dmaCtrlIRQQuiet)

	for range 1 {
		c.Step()
	}
	if got := c.Read8(0x20000200); got != 0x42 {
		t.Fatalf("copy = %#x", got)
	}
	if c.dma.pending(1) != 0 {
		t.Fatal("IRQ_QUIET still raised the completion interrupt")
	}
}

// TestDMAReTrigger checks that a completed channel keeps EN set and can be
// restarted through MULTI_CHAN_TRIGGER and an AL1 trigger alias.
func TestDMAReTrigger(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x5A)
	c.Write32(dmaBase+0x00, 0x20000000)
	c.Write32(dmaBase+0x04, 0x20000300)
	c.Write32(dmaBase+0x08, 1)
	c.Write32(dmaBase+0x0C, dmaCtrlEN) // no increment: addresses stay put
	c.Step()
	if got := c.Read8(0x20000300); got != 0x5A {
		t.Fatalf("first copy = %#x", got)
	}
	if ctrl := dmaChanReg(c, 0, 3); ctrl&dmaCtrlBusy != 0 || ctrl&dmaCtrlEN == 0 {
		t.Fatalf("CTRL after completion = %#x", ctrl)
	}

	// MULTI_CHAN_TRIGGER starts a channel without touching CTRL or rewriting
	// the programmed transfer count.
	c.Write32(dmaBase+0x450, 1)
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("MULTI_CHAN_TRIGGER did not start the channel")
	}
	c.Step()

	// The AL1 count alias triggers too.
	c.Write32(dmaBase+0x1C, 1) // AL1_TRANS_COUNT_TRIG
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("AL1_TRANS_COUNT_TRIG did not start the channel")
	}
	c.Step()
}

// TestDMAChainPendingReload checks that reloading TRANS_COUNT through the AL2
// alias wakes a channel whose chained trigger found a zero count.
func TestDMAChainPendingReload(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x77)
	c.Write8(0x20000001, 0x88)

	// Channel 1: idle, enabled, no count yet.
	c.Write32(dmaBase+0x40, 0x20000001)
	c.Write32(dmaBase+0x44, 0x20000301)
	c.Write32(dmaBase+0x48, 0)
	c.Write32(dmaBase+0x4C, dmaCtrlEN)
	// Channel 0: one byte, chain to channel 1.
	c.Write32(dmaBase+0x00, 0x20000000)
	c.Write32(dmaBase+0x04, 0x20000300)
	c.Write32(dmaBase+0x08, 1)
	c.Write32(dmaBase+0x0C, dmaCtrlEN|1<<13)
	c.Step()
	if c.dma.chainPending&(1<<1) == 0 {
		t.Fatal("chain trigger was not latched")
	}

	// AL2_TRANS_COUNT is the reload path the DVI example uses.
	c.Write32(dmaBase+0x64, 1)
	if dmaChanReg(c, 1, 3)&dmaCtrlBusy == 0 {
		t.Fatal("AL2_TRANS_COUNT did not start the chained channel")
	}
	c.Step()
	if got := c.Read8(0x20000301); got != 0x88 {
		t.Fatalf("chained copy = %#x", got)
	}
}

func TestDMAContinuesAfterCoreHalt(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.CPU0.Halted = true
	c.Halted = true
	c.ContinueAfterHalt = true
	c.Write8(0x20000000, 0x42)
	dmaConfigure(c, 0, 0x20000000, 0x20000100, 2, 0)
	c.Write32(dmaBase+0x450, 1)

	c.Step()
	if got := dmaChanReg(c, 0, 2); got != 1 {
		t.Fatalf("live count after halted-core cycle = %d, want 1", got)
	}
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("core halt completed the DMA early")
	}
	c.Step()
	if got := dmaChanReg(c, 0, 2); got != 0 {
		t.Fatalf("live count after drain = %d, want 0", got)
	}
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy != 0 {
		t.Fatal("DMA remained busy after draining")
	}
}

func TestDMAPauseRetriggerAndReload(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Write8(0x20000000, 0x5A)
	dmaConfigure(c, 0, 0x20000000, 0x20000100, 3, 0)
	c.Write32(dmaBase+0x450, 1)
	c.Step()
	if got := dmaChanReg(c, 0, 2); got != 2 {
		t.Fatalf("live count = %d, want 2", got)
	}

	// A trigger alias programs RELOAD but cannot disturb the live sequence.
	c.Write32(dmaBase+0x1C, 5) // AL1_TRANS_COUNT_TRIG
	c.Write32(dmaBase+0x450, 1)
	if got := dmaChanReg(c, 0, 2); got != 2 {
		t.Fatalf("busy trigger reset live count to %d, want 2", got)
	}
	if got := c.Read32(dmaBase + 0x804); got != 5 {
		t.Fatalf("DBG_TCR = %d, want reload 5", got)
	}

	// Clearing EN pauses without clearing BUSY or losing the live count.
	c.Write32(dmaBase+0x10, 0)
	c.Step()
	if got := dmaChanReg(c, 0, 2); got != 2 {
		t.Fatalf("paused live count = %d, want 2", got)
	}
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy == 0 {
		t.Fatal("clearing EN cleared BUSY")
	}
	c.Write32(dmaBase+0x10, dmaCtrlEN)
	c.Step()
	c.Step()
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy != 0 {
		t.Fatal("resumed DMA did not complete")
	}

	// The next trigger reuses the most recently programmed reload value.
	c.Write32(dmaBase+0x450, 1)
	if got := dmaChanReg(c, 0, 2); got != 5 {
		t.Fatalf("retrigger live count = %d, want reload 5", got)
	}
}

func TestDMARoundRobinOneChannelPerCycle(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	for ch := range 3 {
		dmaConfigure(c, ch, 0x20000000+uint32(ch), 0x20000100+uint32(ch), 2, 0)
	}
	c.Write32(dmaBase+0x450, 0b111)

	for cycle, counts := range [][3]uint32{
		{1, 2, 2},
		{1, 1, 2},
		{1, 1, 1},
		{0, 1, 1},
		{0, 0, 1},
		{0, 0, 0},
	} {
		c.Step()
		for ch, count := range counts {
			if got := dmaChanReg(c, ch, 2); got != count {
				t.Fatalf("cycle %d channel %d count = %d, want %d", cycle+1, ch, got, count)
			}
		}
	}
}

func TestDMAAddressGenerationAndBSWAP(t *testing.T) {
	t.Run("reverse read", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		for i, v := range []byte{0xA0, 0xA1, 0xA2} {
			c.Write8(0x20000002+uint32(i), v)
		}
		ctrl := uint32(dmaCtrlINCRRead | dmaCtrlINCRWrit | 1<<5)
		dmaConfigure(c, 0, 0x20000004, 0x20000100, 3, ctrl)
		c.Write32(dmaBase+0x450, 1)
		for range 3 {
			c.Step()
		}
		for i, want := range []byte{0xA2, 0xA1, 0xA0} {
			if got := c.Read8(0x20000100 + uint32(i)); got != want {
				t.Fatalf("byte %d = %#x, want %#x", i, got, want)
			}
		}
	})

	t.Run("double read", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		for i, v := range []byte{0xB0, 0xB1, 0xB2} {
			c.Write8(0x20000000+uint32(2*i), v)
		}
		ctrl := uint32(dmaCtrlINCRRead | dmaCtrlINCRWrit | 1<<5)
		dmaConfigure(c, 0, 0x20000000, 0x20000100, 3, ctrl&^dmaCtrlINCRRead)
		c.Write32(dmaBase+0x450, 1)
		for range 3 {
			c.Step()
		}
		for i, want := range []byte{0xB0, 0xB1, 0xB2} {
			if got := c.Read8(0x20000100 + uint32(i)); got != want {
				t.Fatalf("byte %d = %#x, want %#x", i, got, want)
			}
		}
	})

	t.Run("reverse write", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		c.Write8(0x20000000, 0xC0)
		ctrl := uint32(dmaCtrlINCRWrit | 1<<7)
		dmaConfigure(c, 0, 0x20000000, 0x20000104, 3, ctrl)
		c.Write32(dmaBase+0x450, 1)
		for range 3 {
			c.Step()
		}
		for _, addr := range []uint32{0x20000104, 0x20000103, 0x20000102} {
			if got := c.Read8(addr); got != 0xC0 {
				t.Fatalf("write at %#x = %#x, want 0xc0", addr, got)
			}
		}
	})

	t.Run("double write", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		c.Write8(0x20000000, 0xD0)
		ctrl := uint32(1 << 7)
		dmaConfigure(c, 0, 0x20000000, 0x20000100, 3, ctrl)
		c.Write32(dmaBase+0x450, 1)
		for range 3 {
			c.Step()
		}
		for _, addr := range []uint32{0x20000100, 0x20000102, 0x20000104} {
			if got := c.Read8(addr); got != 0xD0 {
				t.Fatalf("write at %#x = %#x, want 0xd0", addr, got)
			}
		}
	})

	t.Run("ring read and write", func(t *testing.T) {
		tests := []struct {
			name      string
			writeRing bool
			want      []byte
		}{
			{name: "read", want: []byte{0xE0, 0xE1, 0xE2, 0xE3, 0xE0, 0xE1}},
			{name: "write", writeRing: true, want: []byte{0xE4, 0xE5, 0xE2, 0xE3, 0x00, 0x00}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				c := New()
				c.CPU0.Parked = true
				c.CPU1.Parked = true
				for i := range 6 {
					c.Write8(0x20000200+uint32(i), byte(0xE0+i))
				}
				ctrl := uint32(dmaCtrlINCRRead | dmaCtrlINCRWrit | 2<<8)
				if tt.writeRing {
					ctrl |= 1 << 12
				}
				dmaConfigure(c, 0, 0x20000200, 0x20000300, 6, ctrl)
				c.Write32(dmaBase+0x450, 1)
				for range 6 {
					c.Step()
				}
				for i, want := range tt.want {
					if got := c.Read8(0x20000300 + uint32(i)); got != want {
						t.Fatalf("byte %d = %#x, want %#x", i, got, want)
					}
				}
			})
		}
	})

	t.Run("bswap", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		c.Write16(0x20000000, 0x1234)
		c.Write32(0x20000004, 0x12345678)
		dmaConfigure(c, 0, 0x20000000, 0x20000100, 1, 1<<2|1<<24)
		dmaConfigure(c, 1, 0x20000004, 0x20000104, 1, 2<<2|1<<24)
		c.Write32(dmaBase+0x450, 0b11)
		for range 2 {
			c.Step()
		}
		if got := c.Read16(0x20000100); got != 0x3412 {
			t.Fatalf("halfword BSWAP = %#x, want 0x3412", got)
		}
		if got := c.Read32(0x20000104); got != 0x78563412 {
			t.Fatalf("word BSWAP = %#x, want 0x78563412", got)
		}
	})
}

func TestDMANarrowWriteReplication(t *testing.T) {
	tests := []struct {
		name string
		size uint32
		val  uint32
		want uint32
	}{
		{name: "byte", size: 0, val: 0x5A, want: 0x5A5A5A5A},
		{name: "halfword", size: 1, val: 0x1234, want: 0x12341234},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.CPU0.Parked = true
			c.CPU1.Parked = true
			const dst = 0x400D800C // watchdog scratch0
			dmaConfigure(c, 0, 0x20000000, dst, 1, tt.size<<2)
			if tt.size == 0 {
				c.Write8(0x20000000, byte(tt.val))
			} else {
				c.Write16(0x20000000, uint16(tt.val))
			}
			c.Write32(dmaBase+0x450, 1)
			c.Step()
			if got := c.Read32(dst); got != tt.want {
				t.Fatalf("narrow DMA write = %#x, want %#x", got, tt.want)
			}
		})
	}
}

func TestDMAQuietNullTrigger(t *testing.T) {
	tests := []struct {
		name  string
		quiet bool
	}{
		{name: "normal"},
		{name: "quiet", quiet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.CPU0.Parked = true
			c.CPU1.Parked = true
			ctrl := uint32(0)
			if tt.quiet {
				ctrl |= dmaCtrlIRQQuiet
			}
			dmaConfigure(c, 0, 0x20000000, 0x20000100, 1, ctrl)
			c.Write32(dmaBase+0x404, 1)
			c.Write32(dmaBase+0x450, 1)
			c.Step()
			if tt.quiet && c.dma.intr&1 != 0 {
				t.Fatal("quiet completion raised INTR")
			}
			if !tt.quiet && c.dma.intr&1 == 0 {
				t.Fatal("normal completion did not raise INTR")
			}
			c.Write32(dmaBase+0x400, 1)
			c.Write32(dmaBase+0x2C, 0) // AL2_WRITE_ADDR_TRIG null trigger
			if tt.quiet && c.dma.intr&1 == 0 {
				t.Fatal("quiet null trigger did not raise INTR")
			}
			if !tt.quiet && c.dma.intr&1 != 0 {
				t.Fatal("null trigger raised INTR without IRQ_QUIET")
			}
		})
	}
}

func TestDMAInterruptForceAndMask(t *testing.T) {
	c := New()
	c.Write32(dmaBase+0x404, 1<<1) // INTE0 channel 1
	c.Write32(dmaBase+0x408, 1<<1) // INTF0 channel 1
	if got := c.Read32(dmaBase + 0x40C); got != 1<<1 {
		t.Fatalf("INTS0 = %#x, want %#x", got, uint32(1<<1))
	}
	if c.NVIC.pending[0]&(1<<10) == 0 {
		t.Fatal("forced DMA interrupt did not pend IRQ 10")
	}
	c.Write32(dmaBase+0x40C, 1<<1)
	if c.dma.intr&(1<<1) != 0 {
		t.Fatal("INTS acknowledge changed forced state")
	}
	c.Write32(dmaBase+0x404, 0)
	if c.NVIC.pending[0]&(1<<10) != 0 {
		t.Fatal("masking DMA interrupt did not deassert IRQ 10")
	}
	c.Write32(dmaBase+0x404, 1<<1)
	if c.NVIC.pending[0]&(1<<10) == 0 {
		t.Fatal("unmasking DMA interrupt did not reassert IRQ 10")
	}
	c.Write32(dmaBase+0x408, 0)
	if c.NVIC.pending[0]&(1<<10) != 0 {
		t.Fatal("clearing INTF did not deassert IRQ 10")
	}
}

func TestDMADebugAbortAndIdentity(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	if got := c.Read32(dmaBase + 0x468); got != 16 {
		t.Fatalf("N_CHANNELS = %d, want 16", got)
	}
	dmaConfigure(c, 0, 0x20000000, 0x20000100, 7, 0)
	if got := c.Read32(dmaBase + 0x804); got != 7 {
		t.Fatalf("DBG_TCR before trigger = %d, want 7", got)
	}
	c.Write32(dmaBase+0x450, 1)
	if got := c.Read32(dmaBase + 0x450); got != 0 {
		t.Fatalf("MULTI_CHAN_TRIGGER readback = %#x, want self-cleared 0", got)
	}
	c.Step()
	if got := dmaChanReg(c, 0, 2); got != 6 {
		t.Fatalf("live count = %d, want 6", got)
	}
	if got := c.Read32(dmaBase + 0x804); got != 7 {
		t.Fatalf("DBG_TCR during transfer = %d, want reload 7", got)
	}
	c.Write32(dmaBase+0x464, 1)
	if dmaChanReg(c, 0, 3)&dmaCtrlBusy != 0 {
		t.Fatal("abort did not clear BUSY")
	}
	if got := dmaChanReg(c, 0, 2); got != 0 {
		t.Fatalf("aborted live count = %d, want 0", got)
	}
	if got := c.Read32(dmaBase + 0x464); got != 1 {
		t.Fatalf("CHAN_ABORT before drain = %#x, want 1", got)
	}
	c.Step()
	if got := c.Read32(dmaBase + 0x464); got != 0 {
		t.Fatalf("CHAN_ABORT after drain = %#x, want 0", got)
	}
	if got := c.Read32(dmaBase + 0x460); got != 0 {
		t.Fatalf("FIFO_LEVELS = %#x, want no modelled FIFO occupancy", got)
	}
	c.Write32(dmaBase+0x800, 123)
	if got := c.Read32(dmaBase + 0x800); got != 0 {
		t.Fatalf("DBG_CTDREQ after reset write = %#x, want 0", got)
	}
}

func TestDMAChainIgnoresDisabledAndBusyTargets(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		c.Write32(dmaBase+0x40, 0x20000001)
		c.Write32(dmaBase+0x44, 0x20000101)
		c.Write32(dmaBase+0x48, 1)
		c.Write32(dmaBase+0x50, 0) // AL1_CTRL: disabled
		dmaConfigure(c, 0, 0x20000000, 0x20000100, 1, 1<<13)
		c.Write32(dmaBase+0x450, 1)
		c.Step()
		if c.dma.active&(1<<1) != 0 || c.dma.chainPending&(1<<1) != 0 {
			t.Fatal("chain triggered or latched a disabled target")
		}
	})

	t.Run("busy", func(t *testing.T) {
		c := New()
		c.CPU0.Parked = true
		c.CPU1.Parked = true
		dmaConfigure(c, 1, 0x20000001, 0x20000101, 2, 0)
		dmaConfigure(c, 0, 0x20000000, 0x20000100, 1, 1<<13)
		c.Write32(dmaBase+0x450, 0b11)
		c.Step() // channel 0 completes; its chain to busy channel 1 is ignored
		if got := dmaChanReg(c, 1, 2); got != 2 {
			t.Fatalf("busy target live count = %d, want 2", got)
		}
		c.Step()
		if got := dmaChanReg(c, 1, 2); got != 1 {
			t.Fatalf("busy target live count = %d, want 1", got)
		}
		c.Step()
		if dmaChanReg(c, 1, 3)&dmaCtrlBusy != 0 {
			t.Fatal("busy target did not finish its original sequence")
		}
	})
}

func TestDMASnifferExplicitlyRejected(t *testing.T) {
	c := New()
	c.Write32(dmaBase+0x454, 1)
	if c.Unhandled[dmaBase+0x454] == 0 {
		t.Fatal("SNIFF_CTRL write was silently accepted")
	}
	c.Read32(dmaBase + 0x458)
	if c.Unhandled[dmaBase+0x458] == 0 {
		t.Fatal("SNIFF_DATA read was silently accepted")
	}
}
