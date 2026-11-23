package rp2350

import "testing"

type wireRecorder struct {
	acks   []bool
	value  byte
	begins int
}

func (w *wireRecorder) Write(byte) {}
func (w *wireRecorder) Read() byte { return w.value }
func (w *wireRecorder) WriteAck(byte) bool {
	if len(w.acks) == 0 {
		return true
	}
	ack := w.acks[0]
	w.acks = w.acks[1:]
	return ack
}
func (w *wireRecorder) ReadAck() (byte, bool) {
	if len(w.acks) == 0 {
		return w.value, true
	}
	ack := w.acks[0]
	w.acks = w.acks[1:]
	return w.value, ack
}
func (w *wireRecorder) BeginTransaction() { w.begins++ }
func (w *wireRecorder) EndTransaction()   {}

func TestI2CWireAddressNACKReason(t *testing.T) {
	c := New()
	const base = 0x40090000
	c.AttachI2C(0, 0x50, &wireRecorder{acks: []bool{false}})
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	c.Write32(base+i2cDATA, 0x00)
	if got := c.Read32(base + i2cTXABRTSRC); got != i2cABRT7BAddrNoAck {
		t.Fatalf("address NAK source = %#x, want %#x", got, i2cABRT7BAddrNoAck)
	}
}

func TestI2CWireDataNACKReason(t *testing.T) {
	c := New()
	const base = 0x40090000
	c.AttachI2C(0, 0x50, &wireRecorder{acks: []bool{true, false}})
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	c.Write32(base+i2cDATA, 0x00) // address byte, ACK
	c.Write32(base+i2cDATA, 0x42) // data byte, NAK
	if got := c.Read32(base + i2cTXABRTSRC); got != i2cABRTTXDataNoAck {
		t.Fatalf("data NAK source = %#x, want %#x", got, i2cABRTTXDataNoAck)
	}
}

func TestI2CRestartBeginsTransaction(t *testing.T) {
	c := New()
	const base = 0x40090000
	dev := &wireRecorder{acks: []bool{true, true, true}, value: 0x5a}
	c.AttachI2C(0, 0x50, dev)
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	if dev.begins != 1 {
		t.Fatalf("TAR began %d transactions, want 1", dev.begins)
	}
	c.Write32(base+i2cDATA, 0x10)
	c.Write32(base+i2cDATA, i2cRestart|1<<8) // repeated start + read
	if dev.begins != 2 {
		t.Fatalf("RESTART began %d transactions, want 2", dev.begins)
	}
	if got := c.Read32(base + i2cRXFLR); got != 1 {
		t.Fatalf("RXFLR = %d, want 1", got)
	}
}

// TestI2CSecondTransactionWithoutTAR checks that a command after STOP starts
// a fresh transaction at the latched TAR, even when software does not rewrite
// IC_TAR or set RESTART.
func TestI2CSecondTransactionWithoutTAR(t *testing.T) {
	c := New()
	const base = 0x40090000
	dev := &wireRecorder{acks: []bool{true, true, true}}
	c.AttachI2C(0, 0x50, dev)
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	c.Write32(base+i2cDATA, 1<<9) // first transaction, STOP
	if dev.begins != 1 {
		t.Fatalf("first transaction began %d times", dev.begins)
	}
	c.Write32(base+i2cDATA, 0x42) // new transaction, TAR unchanged
	if dev.begins != 2 {
		t.Fatalf("second transaction began %d times, want 2", dev.begins)
	}
}

func TestI2CRXFullUsesRXFullBit(t *testing.T) {
	c := New()
	const base = 0x40090000
	c.AttachI2C(0, 0x50, &wireRecorder{value: 0x5a})
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	for range 16 {
		c.Write32(base+i2cDATA, 1<<8)
	}
	raw := c.Read32(base + i2cRAWINTR)
	if raw&i2cRXFull == 0 {
		t.Fatalf("RX_FULL (bit 2) not set, raw=%#x", raw)
	}
	if raw&(1<<5) != 0 {
		t.Fatalf("RD_REQ (bit 5) was set by a read, raw=%#x", raw)
	}
}
