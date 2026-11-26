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
