package rp2350

import "testing"

type wireRecorder struct {
	acks       []bool
	value      byte
	begins     int
	ends       int
	readCalls  int
	writeCalls int
}

func (w *wireRecorder) Write(byte) {}
func (w *wireRecorder) Read() byte { return w.value }
func (w *wireRecorder) WriteAck(byte) bool {
	w.writeCalls++
	if len(w.acks) == 0 {
		return true
	}
	ack := w.acks[0]
	w.acks = w.acks[1:]
	return ack
}
func (w *wireRecorder) ReadAck() (byte, bool) {
	w.readCalls++
	if len(w.acks) == 0 {
		return w.value, true
	}
	ack := w.acks[0]
	w.acks = w.acks[1:]
	return w.value, ack
}
func (w *wireRecorder) BeginTransaction() { w.begins++ }
func (w *wireRecorder) EndTransaction()   { w.ends++ }

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
	c.Write32(base+i2cDATA, 1<<8)
	raw := c.Read32(base + i2cRAWINTR)
	if raw&i2cRXFull == 0 {
		t.Fatalf("RX_FULL (bit 2) not set at the reset threshold, raw=%#x", raw)
	}
	if raw&(1<<5) != 0 {
		t.Fatalf("RD_REQ (bit 5) was set by a read, raw=%#x", raw)
	}
}

func TestI2CRXThresholdAndOverrun(t *testing.T) {
	c := New()
	const base = 0x40090000
	c.AttachI2C(0, 0x50, &wireRecorder{value: 0x5a})
	c.Write32(base+i2cRXTL, 15)
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	for range 15 {
		c.Write32(base+i2cDATA, 1<<8)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&i2cRXFull != 0 {
		t.Fatalf("RX_FULL set below RX_TL: %#x", raw)
	}
	c.Write32(base+i2cDATA, 1<<8)
	if raw := c.Read32(base + i2cRAWINTR); raw&i2cRXFull == 0 {
		t.Fatalf("RX_FULL not set at RX_TL+1: %#x", raw)
	}
	if status := c.Read32(base + i2cSTATUS); status&(1<<4) == 0 {
		t.Fatalf("RFF not set for a full FIFO: %#x", status)
	}
	c.Write32(base+i2cDATA, 1<<8)
	if got := c.Read32(base + i2cRXFLR); got != 16 {
		t.Fatalf("RXFLR = %d, want 16", got)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&(1<<1) == 0 {
		t.Fatalf("RX_OVER not set after dropping the 17th byte: %#x", raw)
	}
}

func TestI2CRXFullHoldControl(t *testing.T) {
	c := New()
	const base = 0x40090000
	dev := &wireRecorder{value: 0x5a}
	c.AttachI2C(0, 0x50, dev)
	c.Write32(base+i2cCON, 0x65|1<<9)
	c.Write32(base+i2cENABLE, 1)
	c.Write32(base+i2cTAR, 0x50)
	for range 17 {
		c.Write32(base+i2cDATA, 1<<8)
	}
	if dev.readCalls != 16 {
		t.Fatalf("device reads = %d, want 16 while the RX FIFO is held full", dev.readCalls)
	}
	if got := c.Read32(base + i2cTXFLR); got != 1 {
		t.Fatalf("TXFLR = %d, want the held read command", got)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&(1<<1) != 0 {
		t.Fatalf("RX_OVER set while hold control is enabled: %#x", raw)
	}
	c.Read32(base + i2cDATA)
	if dev.readCalls != 17 {
		t.Fatalf("device reads = %d, want 17 after RX space was released", dev.readCalls)
	}
	if got := c.Read32(base + i2cRXFLR); got != 16 {
		t.Fatalf("RXFLR = %d, want 16 after the held command completed", got)
	}
}

func TestI2CTXFIFOCapacityAndEnable(t *testing.T) {
	c := New()
	const base = 0x40090000
	dev := &wireRecorder{}
	c.AttachI2C(0, 0x50, dev)
	c.Write32(base+i2cDATA, 0x42)
	if got := c.Read32(base + i2cTXFLR); got != 0 || dev.writeCalls != 0 {
		t.Fatalf("disabled controller accepted a command: TXFLR=%d writes=%d", got, dev.writeCalls)
	}
	c.Write32(base+i2cENABLE, 1|1<<2)
	c.Write32(base+i2cTAR, 0x50)
	for range 16 {
		c.Write32(base+i2cDATA, 0x42)
	}
	c.Write32(base+i2cDATA, 0x43)
	if got := c.Read32(base + i2cTXFLR); got != 16 {
		t.Fatalf("TXFLR = %d, want 16", got)
	}
	if status := c.Read32(base + i2cSTATUS); status&(1<<1|1<<2) != 0 {
		t.Fatalf("full TX FIFO status = %#x", status)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&i2cTXOver == 0 {
		t.Fatalf("TX_OVER not set on the 17th command: %#x", raw)
	}
}

func TestI2CTXCommandBlockAndAbort(t *testing.T) {
	c := New()
	const base = 0x40090000
	dev := &wireRecorder{}
	c.AttachI2C(0, 0x50, dev)
	c.Write32(base+i2cENABLE, 1|1<<2)
	c.Write32(base+i2cTAR, 0x50)
	c.Write32(base+i2cDATA, 0x42)
	if dev.writeCalls != 0 {
		t.Fatal("TX_CMD_BLOCK did not hold the command")
	}
	if got := c.Read32(base + i2cTXFLR); got != 1 {
		t.Fatalf("TXFLR = %d, want 1", got)
	}
	c.Write32(base+i2cENABLE, 1)
	if dev.writeCalls != 1 {
		t.Fatalf("device writes = %d, want 1 after unblocking", dev.writeCalls)
	}

	c.Write32(base+i2cENABLE, 1|1<<2)
	c.Write32(base+i2cDATA, 0x43)
	c.Write32(base+i2cENABLE, 1|1<<2|1<<1)
	if got := c.Read32(base + i2cTXABRTSRC); got != 1<<16 {
		t.Fatalf("abort source = %#x, want ABRT_USER_ABRT", got)
	}
	if got := c.Read32(base + i2cTXFLR); got != 0 {
		t.Fatalf("TXFLR = %d after abort, want 0", got)
	}
	if dev.ends != 1 {
		t.Fatalf("EndTransaction calls = %d, want 1", dev.ends)
	}
	if raw := c.Read32(base + i2cRAWINTR); raw&i2cStopDet == 0 || raw&i2cTXAbrt == 0 {
		t.Fatalf("abort did not raise STOP_DET and TX_ABRT: %#x", raw)
	}
	c.Write32(base+i2cDATA, 0x44)
	if dev.writeCalls != 1 {
		t.Fatal("command was accepted while TX_ABRT blocked the FIFO")
	}
	c.Read32(base + 0x40) // CLR_INTR
	if got := c.Read32(base + i2cTXABRTSRC); got != 0 {
		t.Fatalf("CLR_INTR left abort source %#x", got)
	}
	c.Write32(base+i2cENABLE, 1) // release TX_CMD_BLOCK independently
	c.Write32(base+i2cDATA, 0x44)
	if dev.writeCalls != 2 {
		t.Fatal("command was not accepted after CLR_INTR released the FIFO")
	}
}
