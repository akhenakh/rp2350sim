package rp2350

import "testing"

type fakeI2C struct {
	written []byte
	value   byte
}

func (f *fakeI2C) Write(b byte) { f.written = append(f.written, b) }
func (f *fakeI2C) Read() byte   { return f.value }

func TestI2CDeviceTransaction(t *testing.T) {
	c := New()
	dev := &fakeI2C{value: 0x42}
	c.AttachI2C(0, 0x50, dev)

	const base = 0x40090000
	c.Write32(base+i2cENABLE, 1) // enable
	c.Write32(base+i2cTAR, 0x50) // target address

	// Write one register byte, then request one read byte with STOP.
	c.Write32(base+i2cDATA, 0x10)
	if len(dev.written) != 1 || dev.written[0] != 0x10 {
		t.Fatalf("device saw %v", dev.written)
	}
	c.Write32(base+i2cDATA, 1<<8|1<<9) // CMD (read) | STOP
	if got := c.Read32(base + i2cRXFLR); got != 1 {
		t.Fatalf("RXFLR = %d", got)
	}
	if got := c.Read32(base + i2cDATA); got != 0x42 {
		t.Fatalf("read %#x", got)
	}
	if c.Read32(base+i2cRAWINTR)&i2cStopDet == 0 {
		t.Fatal("STOP_DET not set")
	}
	// CLR_STOP_DET clears on read.
	c.Read32(base + 0x60)
	if c.Read32(base+i2cRAWINTR)&i2cStopDet != 0 {
		t.Fatal("STOP_DET did not clear")
	}
}

func TestI2CEmptyBusNacks(t *testing.T) {
	c := New()
	const base = 0x40090000
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	c.Write32(base+i2cDATA, 0x10)
	if got := c.Read32(base + i2cTXABRTSRC); got&(1<<3) == 0 {
		t.Fatalf("abort source = %#x, want address NACK", got)
	}
}
