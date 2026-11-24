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
	if got := c.Read32(base + i2cTXABRTSRC); got&i2cABRT7BAddrNoAck == 0 {
		t.Fatalf("abort source = %#x, want ABRT_7B_ADDR_NOACK", got)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&i2cStopDet == 0 {
		t.Fatalf("empty-bus NACK did not emit STOP_DET: %#x", raw)
	}
	if !c.i2c[0].idle {
		t.Fatal("empty-bus NACK did not return the controller to idle")
	}
}

func TestI2CResetValuesAndActivity(t *testing.T) {
	c := New()
	const base = 0x40090000
	for _, tc := range []struct {
		name string
		off  uint32
		want uint32
	}{
		{"CON", i2cCON, 0x65},
		{"TAR", i2cTAR, 0x55},
		{"SAR", 0x08, 0x55},
		{"INTR_MASK", i2cINTRMASK, 0x7ff},
		{"RX_TL", i2cRXTL, 0},
		{"TX_TL", i2cTXTL, 0},
		{"SDA_HOLD", 0x7c, 1},
		{"FS_SPKLEN", 0xa0, 7},
		{"ENABLE", i2cENABLE, 0},
		{"STATUS", i2cSTATUS, 0x6},
		{"RAW_INTR", i2cRAWINTR, 0},
	} {
		if got := c.Read32(base + tc.off); got != tc.want {
			t.Errorf("%s reset = %#x, want %#x", tc.name, got, tc.want)
		}
	}

	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	raw := c.Read32(base + i2cRAWINTR)
	if raw&(1<<8) == 0 || raw&(1<<10) == 0 {
		t.Fatalf("START/ACTIVITY not latched: %#x", raw)
	}
	if status := c.Read32(base + i2cSTATUS); status&1 == 0 {
		t.Fatalf("master activity not reported: %#x", status)
	}
	c.Write32(base+i2cDATA, 1<<9)
	if status := c.Read32(base + i2cSTATUS); status&1 != 0 {
		t.Fatalf("master still active after STOP: %#x", status)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&(1<<8) == 0 {
		t.Fatalf("latched ACTIVITY cleared before CLR_ACTIVITY: %#x", raw)
	}
	c.Read32(base + 0x5c)
	c.Read32(base + 0x64)
	if raw := c.Read32(base + i2cRAWINTR); raw&(1<<8|1<<10) != 0 {
		t.Fatalf("ACTIVITY/START_DET clear registers failed: %#x", raw)
	}
}
