package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// MCP2515 is a register-level classic-CAN controller on SPI. It implements the
// command set used by common drivers (reset, read/write, load TX buffer, RTS,
// read RX buffer, read status, bit modify), TX/RX buffers, acceptance of all
// frames and an optional interrupt pin.
type MCP2515 struct {
	chip *rp2350.Chip
	bus  *CANBus
	cs   int
	int  int

	regs [0x80]byte

	cmd   byte
	addr  byte
	state int
	data  []byte
}

// MCP2515Info is a controller snapshot for the CAN tab.
type MCP2515Info struct {
	CANSTAT, CANCTRL, CANINTF, CANINTE, EFLG byte
}

// Info returns the controller status registers.
func (m *MCP2515) Info() MCP2515Info {
	return MCP2515Info{
		CANSTAT: m.regs[0x0E], CANCTRL: m.regs[0x0F],
		CANINTF: m.regs[0x2C], CANINTE: m.regs[0x2B], EFLG: m.regs[0x2D],
	}
}

// NewMCP2515 attaches a controller on cs, optionally driving int low on IRQ.
func NewMCP2515(c *rp2350.Chip, bus *CANBus, cs, intPin int) *MCP2515 {
	m := &MCP2515{chip: c, bus: bus, cs: cs, int: intPin}
	bus.Attach(m)
	m.reset()
	return m
}

func (m *MCP2515) reset() {
	clear(m.regs[:])
	m.regs[0x0E] = 0x80 // CANSTAT: configuration mode
	m.regs[0x0F] = 0x87 // CANCTRL: configuration, CLKOUT off
	m.regs[0x28] = 0x03 // CNF3
	m.regs[0x29] = 0x90 // CNF2
	m.regs[0x2A] = 0x02 // CNF1 (500 kbit/s at 8 MHz)
	m.regs[0x2B] = 0x00 // CANINTE
	m.regs[0x2C] = 0x00 // CANINTF
	m.regs[0x2D] = 0x00 // EFLG
	// Accept everything by default.
	m.regs[0x20], m.regs[0x24] = 0x00, 0x00
	m.regs[0x21], m.regs[0x25] = 0x00, 0x00
	m.regs[0x22], m.regs[0x26] = 0x00, 0x00
	m.regs[0x23], m.regs[0x27] = 0x00, 0x00
	m.updateIRQ()
}

// Select starts a new SPI command.
func (m *MCP2515) Select() { m.cmd = 0; m.state = 0; m.data = m.data[:0] }

// Transfer clocks one SPI byte.
func (m *MCP2515) Transfer(tx byte) byte {
	switch m.state {
	case 0: // command
		m.cmd = tx
		switch {
		case tx == 0xC0: // RESET
			m.reset()
			m.state = 3
			return 0xFF
		case tx == 0x03 || tx == 0x02: // READ / WRITE
			m.state = 1
			return 0xFF
		case tx == 0x90 || tx == 0x94: // READ RX BUFFER
			m.addr = 0x61
			if tx == 0x94 {
				m.addr = 0x71
			}
			m.state = 2
			return m.regs[m.addr]
		case tx == 0xA0: // READ STATUS
			return m.status()
		case tx == 0x05: // BIT MODIFY
			m.state = 7
			return 0xFF
		case tx >= 0x80 && tx <= 0x87: // RTS
			for i := range 3 {
				if tx&(1<<uint(i)) != 0 {
					m.transmit(i)
				}
			}
			m.state = 3
			return 0xFF
		case tx >= 0x40 && tx <= 0x42: // LOAD TX BUFFER
			m.addr = byte(0x31 + (tx-0x40)*0x10)
			m.state = 5
			return 0xFF
		default:
			m.state = 3
			return 0xFF
		}
	case 1: // READ/WRITE address
		m.addr = tx
		m.state = 2
		if m.cmd == 0x02 {
			m.state = 6
			return 0xFF
		}
		return m.regs[m.addr]
	case 2: // READ data
		v := m.regs[m.addr]
		m.addr = (m.addr + 1) & 0x7F
		return v
	case 5, 6: // LOAD TX / WRITE data
		m.regs[m.addr] = tx
		if m.addr == 0x0F { // CANCTRL: the requested mode is mirrored in CANSTAT
			m.regs[0x0E] = m.regs[0x0E]&0x1F | tx&0xE0
		}
		m.addr = (m.addr + 1) & 0x7F
		return 0xFF
	case 7: // BIT MODIFY address
		m.addr = tx
		m.data = m.data[:0]
		m.state = 4
		return 0xFF
	case 4: // BIT MODIFY mask/value
		if len(m.data) == 0 {
			m.data = append(m.data, tx) // mask
		} else {
			mask, value := m.data[0], tx
			m.regs[m.addr] = (m.regs[m.addr] &^ mask) | (value & mask)
			m.state = 3
			m.updateIRQ()
		}
		return 0xFF
	}
	return 0xFF
}

func (m *MCP2515) status() byte {
	var v byte
	if m.regs[0x2C]&0x01 != 0 {
		v |= 0x01
	}
	if m.regs[0x2C]&0x02 != 0 {
		v |= 0x02
	}
	if m.regs[0x2C]&0x04 != 0 {
		v |= 0x04
	}
	if m.regs[0x2C]&0x40 != 0 {
		v |= 0x40
	}
	return v
}

func (m *MCP2515) updateIRQ() {
	if m.int < 0 {
		return
	}
	// CANINTF is active low when any enabled interrupt is pending.
	low := m.regs[0x2C]&m.regs[0x2B] != 0
	m.chip.Pins.SetExternal(m.int, !low)
}

// Receive copies a bus frame into RXB0 or RXB1.
func (m *MCP2515) Receive(f CANFrame) {
	base := byte(0x60)
	bit := byte(0x01)
	if m.regs[0x2C]&0x01 != 0 {
		base, bit = 0x70, 0x02
	}
	m.writeRX(base, f)
	m.regs[0x2C] |= bit
	m.updateIRQ()
}

func (m *MCP2515) writeRX(base byte, f CANFrame) {
	id := f.ID
	if f.Ext {
		m.regs[base+1] = byte(id >> 21)
		m.regs[base+2] = byte(id>>13) | 0x08 // IDE
		m.regs[base+3] = byte(id >> 5)
		m.regs[base+4] = byte(id << 3)
	} else {
		m.regs[base+1] = byte(id >> 3)
		m.regs[base+2] = byte(id << 5)
	}
	if f.RTR {
		m.regs[base+2] |= 0x10
	}
	dlc := min(len(f.Data), 8)
	m.regs[base+5] = byte(dlc)
	for i := range dlc {
		m.regs[int(base)+6+i] = f.Data[i]
	}
}

func (m *MCP2515) transmit(n int) {
	base := byte(0x30 + n*0x10)
	if m.regs[base]&0x08 == 0 { // TXREQ
		return
	}
	m.regs[base] &^= 0x08
	f := CANFrame{
		ID:  uint32(m.regs[base+1])<<3 | uint32(m.regs[base+2])>>5,
		Ext: m.regs[base+2]&0x08 != 0,
		RTR: m.regs[base+2]&0x10 != 0,
	}
	if f.Ext {
		f.ID = uint32(m.regs[base+1])<<21 | uint32(m.regs[base+2]&0x03)<<19 |
			uint32(m.regs[base+3])<<11 | uint32(m.regs[base+4])<<3 |
			uint32(m.regs[base+5])>>5
	}
	dlc := min(int(m.regs[base+5]&0x0F), 8)
	f.Data = append([]byte(nil), m.regs[base+6:base+6+byte(dlc)]...)
	m.regs[0x2C] |= 1 << uint(2+n)
	m.updateIRQ()
	m.bus.Send(f, m)
}
