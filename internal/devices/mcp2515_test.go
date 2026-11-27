package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestMCP2515LoopbackPeer(t *testing.T) {
	c := rp2350.New()
	bus := NewCANBus(c)
	peer := NewCANPeer(bus, 0x123, 0x124, false)
	m := NewMCP2515(c, bus, 17, -1)

	// Load TXB0: SIDH/SIDL, EID8/EID0, DLC, data.
	m.Select()
	m.Transfer(0x40)
	for _, b := range []byte{0x24, 0x60, 0, 0, 1, 7} {
		m.Transfer(b)
	}
	m.regs[0x30] = 0x08 // TXREQ
	m.Select()
	m.Transfer(0x81) // RTS TXB0
	if m.regs[0x2C]&0x04 == 0 {
		t.Fatal("TX0IF not set")
	}
	if m.regs[0x2C]&0x01 == 0 {
		t.Fatal("RX0IF not set after peer reply")
	}
	// RXB0 SIDH/SIDL should encode 0x124, data byte 8.
	if id := uint32(m.regs[0x61])<<3 | uint32(m.regs[0x62])>>5; id != 0x124 {
		t.Fatalf("RX id = %#x", id)
	}
	if m.regs[0x66] != 8 {
		t.Fatalf("RX data = %d", m.regs[0x66])
	}
	_ = peer
}

func TestMCP2515BitModify(t *testing.T) {
	c := rp2350.New()
	m := NewMCP2515(c, NewCANBus(c), 17, -1)
	m.regs[0x2B] = 0
	m.Select()
	m.Transfer(0x05)
	m.Transfer(0x2B)
	m.Transfer(0x03)
	m.Transfer(0x01)
	if m.regs[0x2B] != 1 {
		t.Fatalf("CANINTE = %#x, want 1", m.regs[0x2B])
	}
}

func TestMCP2515ModeMirror(t *testing.T) {
	c := rp2350.New()
	m := NewMCP2515(c, NewCANBus(c), 17, -1)
	m.Select()
	m.Transfer(0x02)
	m.Transfer(0x0F)
	m.Transfer(0x00) // normal mode
	if got := m.Info().CANSTAT; got != 0 {
		t.Fatalf("CANSTAT = %#x after normal mode, want 0", got)
	}
	m.Select()
	m.Transfer(0x02)
	m.Transfer(0x0F)
	m.Transfer(0x80) // configuration
	if got := m.Info().CANSTAT; got != 0x80 {
		t.Fatalf("CANSTAT = %#x after configuration, want 0x80", got)
	}
}

func TestMCP2515ReadStatus(t *testing.T) {
	c := rp2350.New()
	m := NewMCP2515(c, NewCANBus(c), 17, -1)
	m.regs[0x2C] = 0x01 | 0x02 | 0x04 | 0x08 // RX0, RX1, TX0IF, TX1IF
	m.regs[0x30] = 0x08                      // TX0REQ
	m.Select()
	if got := m.Transfer(0xA0); got != 0x01|0x02|0x04|0x08|0x20 {
		t.Fatalf("READ STATUS = %#x", got)
	}
}

func TestMCP2515InterruptDeassert(t *testing.T) {
	c := rp2350.New()
	bus := NewCANBus(c)
	m := NewMCP2515(c, bus, 17, 20)
	c.Write32(0x40038004+20*4, 1<<6) // input enable on INT
	m.regs[0x2B] = 0x01              // RX0IE
	m.updateIRQ()
	if !c.Pins.Level(20) {
		t.Fatal("INT asserted before a frame")
	}
	m.Receive(CANFrame{ID: 0x123})
	if c.Pins.Level(20) {
		t.Fatal("INT not asserted after RX0")
	}
	m.Select()
	m.Transfer(0x02)
	m.Transfer(0x2C)
	m.Transfer(0) // clear CANINTF
	if !c.Pins.Level(20) {
		t.Fatal("INT did not deassert after clearing CANINTF")
	}
}

func TestMCP2515ExtendedID(t *testing.T) {
	c := rp2350.New()
	bus := NewCANBus(c)
	const id = 0x1ABCDE
	const reply = 0x1ABCDF
	NewCANPeer(bus, id, reply, true)
	m := NewMCP2515(c, bus, 17, -1)

	// Load TXB0 with a 29-bit ID and one data byte.
	m.regs[0x31] = byte(id >> 21)
	m.regs[0x32] = (byte(id>>18)&0x07)<<5 | byte(id>>16)&0x03 | 0x08
	m.regs[0x33] = byte((id >> 8) & 0xFF)
	m.regs[0x34] = byte(id & 0xFF)
	m.regs[0x35] = 1
	m.regs[0x36] = 0x41
	m.regs[0x30] = 0x08 // TXREQ
	m.Select()
	m.Transfer(0x81) // RTS TXB0

	frames := bus.Frames()
	if len(frames) == 0 || frames[0].ID != id || !frames[0].Ext {
		t.Fatalf("TX frames = %+v", frames)
	}
	if m.regs[0x2C]&0x01 == 0 {
		t.Fatal("RX0IF not set after extended reply")
	}
	rxID := uint32(m.regs[0x61])<<21 | uint32(m.regs[0x62]>>5)<<18 |
		uint32(m.regs[0x62]&0x03)<<16 | uint32(m.regs[0x63])<<8 | uint32(m.regs[0x64])
	if rxID != reply || m.regs[0x62]&0x08 == 0 {
		t.Fatalf("RX extended id = %#x (SIDL %#x)", rxID, m.regs[0x62])
	}
	if m.regs[0x66] != 0x42 {
		t.Fatalf("RX data = %#x", m.regs[0x66])
	}
}

func TestMCP2515RxStatus(t *testing.T) {
	c := rp2350.New()
	m := NewMCP2515(c, NewCANBus(c), 17, -1)
	m.Receive(CANFrame{ID: 0x1ABCDE, Ext: true})
	m.Select()
	got := m.Transfer(0xB0)
	if got&0x40 == 0 || got&0x20 == 0 {
		t.Fatalf("RX STATUS = %#x, want RXB0 and extended", got)
	}
	m.regs[0x62] |= 0x10 // RTR
	m.Select()
	got = m.Transfer(0xB0)
	if got&0x18 != 0x18 {
		t.Fatalf("RX STATUS RTR = %#x", got)
	}
}
