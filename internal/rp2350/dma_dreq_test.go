package rp2350

import "testing"

// TestDMAPIOTXPacing: a DMA channel feeding a PIO TX FIFO must stall while
// the FIFO is full instead of pushing a word that sets TXOVER.
func TestDMAPIOTXPacing(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	p := c.pio[0]
	for i := range 4 {
		p.sm[0].pushTx(uint32(i))
	}
	if !p.sm[0].txFull() {
		t.Fatal("TX FIFO not full")
	}
	c.Write32(SRAMBase, 0x11223344)
	dmaConfigure(c, 0, SRAMBase, pioBase0+0x10, 8,
		dmaCtrlINCRRead|2<<2|0<<17) // word, DREQ_PIO0_TX0
	c.Write32(dmaBase+0x450, 1)
	c.dma.tick()
	if p.fdebug&(1<<16) != 0 {
		t.Fatal("DMA wrote into a full PIO TX FIFO")
	}
	if c.dma.ch[0].count != 8 {
		t.Fatalf("count = %d, want 8", c.dma.ch[0].count)
	}
	p.sm[0].popTx()
	c.dma.tick()
	if c.dma.ch[0].count != 7 {
		t.Fatalf("count = %d, want 7 after room appeared", c.dma.ch[0].count)
	}
	if got := p.sm[0].tx[3]; got != 0x11223344 {
		t.Fatalf("queued %#x, want 0x11223344", got)
	}
}

// TestDMAPIORXPacing: a DMA read from a PIO RX FIFO waits for a word.
func TestDMAPIORXPacing(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	dst := uint32(SRAMBase + 0x100)
	dmaConfigure(c, 0, pioBase0+0x20, dst, 4,
		dmaCtrlINCRWrit|2<<2|4<<17) // word, DREQ_PIO0_RX0
	c.Write32(dmaBase+0x450, 1)
	c.dma.tick()
	if c.dma.ch[0].count != 4 {
		t.Fatal("DMA consumed an empty PIO RX FIFO")
	}
	c.pio[0].sm[0].pushRx(0x55667788)
	c.dma.tick()
	if c.dma.ch[0].count != 3 {
		t.Fatalf("count = %d, want 3", c.dma.ch[0].count)
	}
	if got := c.Read32(dst); got != 0x55667788 {
		t.Fatalf("copied %#x", got)
	}
}

// TestDMAOtherDREQs checks the SPI/UART/I2C/PWM/SHA dispatch table.
func TestDMAOtherDREQs(t *testing.T) {
	c := New()
	setTreq := func(treq uint32) { c.dma.ch[0].ctrl = treq << 17 }

	setTreq(24) // SPI0 TX is immediate
	if !c.dma.dreq(0) {
		t.Fatal("SPI0 TX not ready")
	}
	setTreq(25) // SPI0 RX
	if c.dma.dreq(0) {
		t.Fatal("SPI0 RX ready while empty")
	}
	c.spi[0].rx = append(c.spi[0].rx, 1)
	if !c.dma.dreq(0) {
		t.Fatal("SPI0 RX not ready with data")
	}

	setTreq(29) // UART0 RX
	if c.dma.dreq(0) {
		t.Fatal("UART0 RX ready while empty")
	}
	c.uart[0].rx = append(c.uart[0].rx, 1)
	if !c.dma.dreq(0) {
		t.Fatal("UART0 RX not ready with data")
	}

	setTreq(45) // I2C0 RX
	if c.dma.dreq(0) {
		t.Fatal("I2C0 RX ready while empty")
	}
	c.i2c[0].rx = append(c.i2c[0].rx, 1)
	if !c.dma.dreq(0) {
		t.Fatal("I2C0 RX not ready with data")
	}

	setTreq(32) // PWM wrap 0
	if c.dma.dreq(0) {
		t.Fatal("PWM wrap DREQ set without a wrap")
	}
	c.Write32(pwmBase+0x10, 3)
	c.Write32(pwmBase, 1)
	for range 4 {
		c.Step()
	}
	if !c.dma.dreq(0) {
		t.Fatal("PWM wrap DREQ did not pulse")
	}
	c.pwm.tick()
	if c.dma.dreq(0) {
		t.Fatal("PWM wrap DREQ survived its cycle")
	}

	setTreq(54) // SHA256
	if !c.dma.dreq(0) {
		t.Fatal("SHA256 not ready at reset")
	}
	c.sha.ready = false
	if c.dma.dreq(0) {
		t.Fatal("SHA256 ready while busy")
	}
}
