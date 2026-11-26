package rp2350

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
)

func TestUARTResetAndIdentification(t *testing.T) {
	c := New()
	for _, base := range []uint32{0x40070000, 0x40078000} {
		for _, tc := range []struct {
			off  uint32
			want uint32
		}{
			{0x004, 0},
			{0x018, 0x90},
			{0x030, 0x300},
			{0x034, 0x12},
			{0xfe0, 0x11},
			{0xfe4, 0x10},
			{0xfe8, 0x34},
			{0xfec, 0x00},
			{0xff0, 0x0d},
			{0xff4, 0xf0},
			{0xff8, 0x05},
			{0xffc, 0xb1},
		} {
			if got := c.Read32(base + tc.off); got != tc.want {
				t.Errorf("UART %#x register %#x = %#x, want %#x", base, tc.off, got, tc.want)
			}
		}
	}
}

func TestUARTHostBurstPreservesSingleEntryFIFO(t *testing.T) {
	c := New()
	const base = 0x40070000
	c.SendUART(0, []byte{0x11, 0x22})
	if fr := c.Read32(base + 0x18); fr&(1<<6|1<<4) != 1<<6 {
		t.Fatalf("UARTFR = %#x, want full and non-empty single-entry FIFO", fr)
	}
	for _, want := range []uint32{0x11, 0x22} {
		if got := c.Read32(base); got != want {
			t.Fatalf("UARTDR = %#x, want %#x", got, want)
		}
	}
	if got := c.Read32(base + 0x04); got&(1<<3) != 0 {
		t.Fatalf("host burst set UARTRSR.OE: %#x", got)
	}
}

func TestUARTResetClearsHostQueue(t *testing.T) {
	c := New()
	c.Write32(0x4007002c, 1<<4)
	c.SendUART(0, bytes.Repeat([]byte{0x5a}, uartFIFODepth+8))
	c.Reset()
	if len(c.uart[0].rx) != 0 || len(c.uart[0].hostRx) != 0 {
		t.Fatalf("reset retained RX data: fifo=%d host=%d", len(c.uart[0].rx), len(c.uart[0].hostRx))
	}
}

func TestUARTECRPreservesOverrunInterrupt(t *testing.T) {
	c := New()
	const base = 0x40070000
	c.SendUART(0, make([]byte, uartHostQueueLimit+1))
	c.Write32(base+0x04, 1<<3) // UARTECR before the emulator polls the event
	if got := c.Read32(base + 0x04); got != 0 {
		t.Fatalf("UARTECR did not clear UARTRSR: %#x", got)
	}
	if got := c.Read32(base + 0x3c); got&(1<<10) == 0 {
		t.Fatalf("UARTECR cleared the overrun interrupt: %#x", got)
	}
}

func TestUARTFIFOCapacityAndRXInterrupt(t *testing.T) {
	c := New()
	const base = 0x40070000
	c.Write32(base+0x2c, 1<<4) // UARTLCR_H.FEN
	c.Write32(base+0x38, 1<<4) // UARTIMSC.RXIM
	data := make([]byte, 33)
	for i := range data {
		data[i] = byte(i)
	}
	c.SendUART(0, data)
	c.uart[0].tick()
	if got := len(c.uart[0].rx); got != 32 {
		t.Fatalf("RX FIFO depth = %d, want 32", got)
	}
	if got := len(c.uart[0].hostRx); got != 1 {
		t.Fatalf("host queue depth = %d, want 1", got)
	}
	if fr := c.Read32(base + 0x18); fr&(1<<6|1<<4) != 1<<6 {
		t.Fatalf("UARTFR = %#x, want full and non-empty FIFO", fr)
	}
	if ris := c.Read32(base + 0x3c); ris&(1<<4) == 0 {
		t.Fatalf("RXRIS not set at the reset 16-byte threshold: %#x", ris)
	}
	if c.NVIC.pending[1]&(1<<(uartRXIRQ-32)) == 0 {
		t.Fatal("RX interrupt did not reach the NVIC")
	}
	for range 16 {
		c.Read32(base)
	}
	if ris := c.Read32(base + 0x3c); ris&(1<<4) == 0 {
		t.Fatalf("RXRIS cleared at 16 bytes: %#x", ris)
	}
	// The 33rd byte was queued behind the FIFO and refills it as it drains.
	// RXRIS therefore stays asserted until the queue is empty and only 15
	// architectural FIFO entries remain.
	c.Read32(base)
	if ris := c.Read32(base + 0x3c); ris&(1<<4) == 0 {
		t.Fatalf("RXRIS cleared while the queued byte kept the FIFO at threshold: %#x", ris)
	}
	c.Read32(base)
	if ris := c.Read32(base + 0x3c); ris&(1<<4) != 0 {
		t.Fatalf("RXRIS stayed set below threshold: %#x", ris)
	}
	if c.NVIC.pending[1]&(1<<(uartRXIRQ-32)) != 0 {
		t.Fatal("RX interrupt stayed pending below threshold")
	}
}

func TestUARTRXFIFOInterruptThresholds(t *testing.T) {
	for _, tc := range []struct {
		name string
		sel  uint32
		want int
	}{
		{"eighth", 0, 4},
		{"quarter", 1, 8},
		{"half", 2, 16},
		{"three_quarters", 3, 24},
		{"seven_eighths", 4, 28},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			const base = 0x40070000
			c.Write32(base+0x2c, 1<<4)
			c.Write32(base+0x34, tc.sel<<3|2)
			c.SendUART(0, make([]byte, tc.want-1))
			c.uart[0].tick()
			if ris := c.Read32(base + 0x3c); ris&(1<<4) != 0 {
				t.Fatalf("RXRIS set below %d-byte threshold: %#x", tc.want, ris)
			}
			c.SendUART(0, []byte{0x42})
			c.uart[0].tick()
			if ris := c.Read32(base + 0x3c); ris&(1<<4) == 0 {
				t.Fatalf("RXRIS not set at %d-byte threshold: %#x", tc.want, ris)
			}
		})
	}
}

func TestUARTConcurrentSendAndRead(t *testing.T) {
	c := New()
	const base = 0x40070000
	c.Write32(base+0x2c, 1<<4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			c.SendUART(0, make([]byte, 8))
		})
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	var got []byte
	for {
		fr := c.Read32(base + 0x18)
		if fr&(1<<4) == 0 {
			got = append(got, byte(c.Read32(base)))
		}
		select {
		case <-done:
			if fr&(1<<4) != 0 {
				if len(got) != 32 {
					t.Fatalf("read %d bytes, want 32", len(got))
				}
				return
			}
		default:
			runtime.Gosched()
		}
	}
}

type recordingUART struct {
	tx []byte
	c  *Chip
}

func (d *recordingUART) TX(b byte) {
	d.tx = append(d.tx, b)
	if b == '?' {
		d.c.SendUART(1, []byte("ok"))
	}
}
func (d *recordingUART) Reset() {}

func TestAttachUARTDevice(t *testing.T) {
	c := New()
	dev := &recordingUART{c: c}
	c.AttachUART(1, dev)
	// UART1 data register at 0x40078000: TX '?' and read the plugin's reply.
	c.Write32(0x40078000, '?')
	if got := string(dev.tx); got != "?" {
		t.Fatalf("plugin TX = %q", got)
	}
	if got := c.Read32(0x40078000) & 0xFF; got != 'o' {
		t.Fatalf("UART RX = %#x, want 'o'", got)
	}
	if got := c.Read32(0x40078000) & 0xFF; got != 'k' {
		t.Fatalf("UART RX = %#x, want 'k'", got)
	}
}
