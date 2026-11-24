package rp2350

import (
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

func TestUARTSingleEntryOverrun(t *testing.T) {
	c := New()
	const base = 0x40070000
	c.SendUART(0, []byte{0x11, 0x22})
	if fr := c.Read32(base + 0x18); fr&(1<<6|1<<4) != 1<<6 {
		t.Fatalf("UARTFR = %#x, want full and non-empty single-entry FIFO", fr)
	}
	if got := c.Read32(base); got != 0x811 {
		t.Fatalf("UARTDR = %#x, want data with OE", got)
	}
	if got := c.Read32(base + 0x04); got&(1<<3) == 0 {
		t.Fatalf("UARTRSR.OE not set: %#x", got)
	}
	c.uart[0].tick()
	if got := c.Read32(base + 0x3c); got&(1<<10) == 0 {
		t.Fatalf("UARTRIS.OERIS not set: %#x", got)
	}
	c.Write32(base+0x04, 1<<3) // UARTECR
	if got := c.Read32(base + 0x04); got != 0 {
		t.Fatalf("UARTECR did not clear UARTRSR: %#x", got)
	}
	c.Write32(base+0x44, 1<<10) // UARTICR.OEIC
	if got := c.Read32(base + 0x3c); got&(1<<10) != 0 {
		t.Fatalf("UARTICR did not clear OERIS: %#x", got)
	}
}

func TestUARTECRPreservesOverrunInterrupt(t *testing.T) {
	c := New()
	const base = 0x40070000
	c.SendUART(0, []byte{0x11, 0x22})
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
