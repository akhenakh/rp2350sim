package rp2350

import "testing"

func drainHSTX(h *hstxBlock) {
	for h.fifoLevel > 0 {
		h.tick()
	}
}

// TestHSTXHeightFollowsWidth checks that a mode switch relearns the derived
// frame height instead of keeping the first width's height forever.
func TestHSTXHeightFollowsWidth(t *testing.T) {
	h := newHSTX()
	h.csr = 1 | 1<<1 // EN | EXPAND_EN
	h.push(2<<12 | 640)
	h.tick()
	if h.height != 480 || h.activeW != 640 {
		t.Fatalf("640 mode = %dx%d", h.activeW, h.height)
	}
	h.endCommand()
	h.push(2<<12 | 320)
	h.tick()
	if h.height != 240 || h.activeW != 320 {
		t.Fatalf("320 mode = %dx%d", h.activeW, h.height)
	}
}

func TestHSTXFrame(t *testing.T) {
	c := New()
	// RGB332 with the lane rotations used by the Pico SDK DVI example.
	c.Write32(hstxCtrlBase+hstxExpandTMDS, 2<<21|0<<16|2<<13|29<<8|1<<5|26)
	c.Write32(hstxCtrlBase+hstxExpandShift, 4<<24|8<<16|1<<8)
	c.Write32(hstxCtrlBase+hstxCSR, 1|1<<1)
	h := c.hstx

	pixelWord := uint32(0xE0) | uint32(0x1C)<<8 | uint32(0x03)<<16 | uint32(0xFF)<<24
	// Two active lines of four pixels each.
	for range 2 {
		h.push(2<<12 | 4) // TMDS, 4 pixels
		h.tick()
		h.push(pixelWord)
		h.tick()
	}
	h.push(0xF << 12) // NOP: blanking line ends the frame
	h.tick()
	h.push(2<<12 | 4) // first line of the next frame
	h.tick()

	img := c.HSTXFrame()
	if img == nil {
		t.Fatal("no DVI frame")
	}
	if b := img.Bounds(); b.Dx() != 4 || b.Dy() != 2 {
		t.Fatalf("frame = %dx%d, want 4x2", b.Dx(), b.Dy())
	}
	// RGB332 0xE0 is red (111 000 00), 0x1C is green-ish, 0x03 is blue.
	if r, g, b, _ := img.At(0, 0).RGBA(); r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Fatalf("pixel 0 = %d,%d,%d", r>>8, g>>8, b>>8)
	}
	if _, _, b, _ := img.At(2, 0).RGBA(); b>>8 < 200 {
		t.Fatalf("blue pixel = %d", b>>8)
	}
}

func TestHSTXResetValuesAndMasks(t *testing.T) {
	c := New()
	if got := c.Read32(hstxCtrlBase + hstxCSR); got != hstxCSRReset {
		t.Fatalf("CSR reset = %#x, want %#x", got, hstxCSRReset)
	}
	if got := c.Read32(hstxCtrlBase + hstxExpandShift); got != hstxExpandShiftReset {
		t.Fatalf("EXPAND_SHIFT reset = %#x, want %#x", got, hstxExpandShiftReset)
	}
	c.Write32(hstxCtrlBase+hstxCSR, 0xFFFFFFFF)
	if got := c.Read32(hstxCtrlBase + hstxCSR); got != hstxCSRMask {
		t.Fatalf("CSR masked value = %#x, want %#x", got, hstxCSRMask)
	}
	c.Write32(hstxCtrlBase+hstxExpandShift, 0xFFFFFFFF)
	if got := c.Read32(hstxCtrlBase + hstxExpandShift); got != hstxExpandShiftMask {
		t.Fatalf("EXPAND_SHIFT masked value = %#x, want %#x", got, hstxExpandShiftMask)
	}
	c.Write32(hstxCtrlBase+hstxExpandTMDS, 0xFFFFFFFF)
	if got := c.Read32(hstxCtrlBase + hstxExpandTMDS); got != hstxExpandTMDSMask {
		t.Fatalf("EXPAND_TMDS masked value = %#x, want %#x", got, hstxExpandTMDSMask)
	}

	c.hstx.push(1)
	c.hstx.wof = true
	c.hstx.serial = 9
	c.Reset()
	if got := c.Read32(hstxFIFOBase); got&(1<<9) == 0 {
		t.Fatalf("reset FIFO STAT = %#x, want empty", got)
	}
	if c.hstx.wof || c.hstx.serial != 0 {
		t.Fatal("reset did not clear FIFO overflow/frame serial")
	}
}

func TestHSTXFIFOEnableAndOverflow(t *testing.T) {
	c := New()
	h := c.hstx
	for i := range hstxFIFODepth {
		c.Write32(hstxFIFOBase+hstxFIFOData, uint32(i))
	}
	h.tick() // disabled: no pop
	if h.fifoLevel != hstxFIFODepth {
		t.Fatalf("disabled FIFO level = %d", h.fifoLevel)
	}
	stat := c.Read32(hstxFIFOBase)
	if stat&(1<<8) == 0 || stat&0xFF != hstxFIFODepth {
		t.Fatalf("full FIFO STAT = %#x", stat)
	}
	c.Write32(hstxFIFOBase+hstxFIFOData, 9)
	if c.Read32(hstxFIFOBase)&(1<<10) == 0 {
		t.Fatal("write to full FIFO did not set WOF")
	}
	c.Write32(hstxFIFOBase, 1<<10)
	if c.Read32(hstxFIFOBase)&(1<<10) != 0 {
		t.Fatal("WOF did not clear through STAT")
	}
	c.Write32(hstxCtrlBase+hstxCSR, 1) // EN, expander bypassed
	h.tick()
	if h.fifoLevel != hstxFIFODepth-1 {
		t.Fatalf("enabled FIFO level = %d, want %d", h.fifoLevel, hstxFIFODepth-1)
	}
}

func TestHSTXRawBypass(t *testing.T) {
	c := New()
	h := c.hstx
	c.Write32(hstxCtrlBase+hstxCSR, 1) // EN with EXPAND_EN clear
	h.push(2<<12 | 4)
	h.push(0xE0 | 0x1C<<8 | 0x03<<16 | 0xFF<<24)
	drainHSTX(h)
	if h.mode != hstxModeIdle || h.fifoLevel != 0 {
		t.Fatalf("raw bypass state mode=%d level=%d", h.mode, h.fifoLevel)
	}
	if c.HSTXFrame() != nil {
		t.Fatal("raw data was interpreted as an expander command")
	}
}

func TestHSTXDMAStallsOnFullFIFO(t *testing.T) {
	c := New()
	h := c.hstx
	for i := range hstxFIFODepth {
		h.push(uint32(i))
	}
	const src = uint32(0x20000000)
	c.Write32(src, 0x12345678)
	d := c.dma
	d.block.regs[0] = src
	d.block.regs[1] = hstxFIFOBase + hstxFIFOData
	d.setCount(0, 1)
	d.setCtrl(0, dmaCtrlEN|2<<2)
	d.start(0)
	c.Write32(hstxCtrlBase+hstxCSR, 1)

	ch := &d.ch[0]
	d.step(0, ch)
	if ch.count != 1 || ch.read != src || h.fifoLevel != hstxFIFODepth {
		t.Fatal("DMA advanced while the HSTX FIFO was full")
	}
	h.tick() // one slot becomes available
	d.step(0, ch)
	if ch.count != 0 || h.fifoLevel != hstxFIFODepth {
		t.Fatal("DMA did not resume after HSTX popped a FIFO word")
	}
	if got := h.fifoData[(h.fifoHead+hstxFIFODepth-1)%hstxFIFODepth]; got != 0x12345678 {
		t.Fatalf("DMA FIFO payload = %#x", got)
	}
}

func TestHSTXInfiniteCommandCount(t *testing.T) {
	c := New()
	h := c.hstx
	c.Write32(hstxCtrlBase+hstxExpandTMDS, 2<<21|2<<13|1<<5)
	c.Write32(hstxCtrlBase+hstxExpandShift, 4<<24|8<<16)
	c.Write32(hstxCtrlBase+hstxCSR, 1|1<<1)
	h.push(2 << 12) // TMDS, count 0 = infinite
	h.tick()
	h.push(0xE0 | 0x1C<<8 | 0x03<<16 | 0xFF<<24)
	h.tick()
	if h.mode != hstxModeTMDS || h.remaining != -1 || len(h.line) != 4 {
		t.Fatalf("infinite command mode=%d remaining=%d pixels=%d", h.mode, h.remaining, len(h.line))
	}
	c.Write32(hstxCtrlBase+hstxCSR, 0)
	if h.mode != hstxModeIdle || h.remaining != 0 {
		t.Fatal("disabling HSTX did not reset the expander")
	}
}
