package rp2350

import "testing"

type fakeWire struct {
	writeAck bool
	readByte byte
	readAck  bool
}

func (f *fakeWire) Write(byte)         {}
func (f *fakeWire) Read() byte         { return f.readByte }
func (f *fakeWire) WriteAck(byte) bool { return f.writeAck }
func (f *fakeWire) ReadAck() (byte, bool) {
	return f.readByte, f.readAck
}

func TestI2CDataNACKAborts(t *testing.T) {
	c := New()
	c.AttachI2C(0, 0x50, &fakeWire{writeAck: false})
	i := c.i2c[0]
	i.addr = 0x50
	i.dataCmd(0x5a)
	if i.raw&i2cTXAbrt == 0 {
		t.Fatal("TX_ABRT not raised on a data NAK")
	}
	if got := c.Read32(c.i2c[0].block.base + i2cTXABRTSRC); got != i2cABRTTXDataNoAck {
		t.Fatalf("TX_ABRT_SOURCE = %#x, want %#x", got, i2cABRTTXDataNoAck)
	}
}

func TestI2CReadWireNAKAborts(t *testing.T) {
	c := New()
	c.AttachI2C(0, 0x50, &fakeWire{readAck: false})
	i := c.i2c[0]
	i.addr = 0x50
	i.dataCmd(1 << 8) // read
	if i.raw&i2cTXAbrt == 0 {
		t.Fatal("TX_ABRT not raised on a read NAK")
	}
	if len(i.rx) != 0 {
		t.Fatalf("RX FIFO has %d bytes after a read NAK", len(i.rx))
	}
	if got := c.Read32(c.i2c[0].block.base + i2cTXABRTSRC); got != i2cABRTTXDataNoAck {
		t.Fatalf("TX_ABRT_SOURCE = %#x, want %#x", got, i2cABRTTXDataNoAck)
	}
}

func TestI2CAddressNACKReason(t *testing.T) {
	c := New()
	i := c.i2c[0]
	i.addr = 0x51
	i.dataCmd(1 << 8) // read from an empty address
	if got := c.Read32(c.i2c[0].block.base + i2cTXABRTSRC); got != i2cABRT7BAddrNoAck {
		t.Fatalf("TX_ABRT_SOURCE = %#x, want ABRT_7B_ADDR_NOACK (%#x)", got, i2cABRT7BAddrNoAck)
	}
}

func TestI2CWireReadData(t *testing.T) {
	c := New()
	c.AttachI2C(0, 0x50, &fakeWire{readByte: 0x12, readAck: true})
	i := c.i2c[0]
	i.addr = 0x50
	i.dataCmd(1 << 8)
	if i.raw&i2cTXAbrt != 0 {
		t.Fatal("unexpected abort on a valid read")
	}
	if len(i.rx) != 1 || i.rx[0] != 0x12 {
		t.Fatalf("RX FIFO = %v, want [0x12]", i.rx)
	}
}
