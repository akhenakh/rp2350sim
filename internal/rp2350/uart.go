package rp2350

// uartBlock models the PL011/DW_apb UART instances (UART0 at 0x40070000,
// UART1 at 0x40078000) closely enough for TinyGo's polling and
// interrupt-driven console drivers.
type uartBlock struct {
	block *regBlock
	rx    []byte
	// OnByte receives every byte written to the data register.
	OnByte func(b byte)
	// NextRx supplies a byte when the receive queue is empty; ok=false means
	// no data is available.
	NextRx func() (b byte, ok bool)
	fr     uint32
}

const (
	uartFRTXFE = 1 << 7 // transmit FIFO empty
	uartFRTXFF = 1 << 5 // transmit FIFO full
	uartFRRXFE = 1 << 4 // receive FIFO empty
	uartFRBUSY = 1 << 3 // UART busy
)

func newUART(c *Chip, base uint32, id int) *uartBlock {
	u := &uartBlock{block: newRegBlock(base, 0x1000)}
	if id == 0 {
		u.OnByte = func(b byte) {
			if c.OnUART0 != nil {
				c.OnUART0(b)
			}
		}
	} else {
		u.OnByte = func(b byte) {
			if c.OnUART1 != nil {
				c.OnUART1(b)
			}
		}
	}
	u.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x18: // UARTFR
			fr := uint32(uartFRTXFE | uartFRRXFE)
			if len(u.rx) > 0 {
				fr &^= uartFRRXFE
			}
			if u.NextRx != nil && len(u.rx) == 0 {
				if b, ok := u.NextRx(); ok {
					u.rx = append(u.rx, b)
					fr &^= uartFRRXFE
				}
			}
			return fr, true
		case 0x00: // UARTDR
			var v uint32
			if len(u.rx) > 0 {
				v = uint32(u.rx[0])
				u.rx = u.rx[1:]
			}
			return v, true
		}
		return 0, false
	}
	u.block.writeFn = func(c *Chip, off, v uint32) {
		if off == 0x00 { // UARTDR
			if u.OnByte != nil {
				u.OnByte(byte(v))
			}
		}
	}
	return u
}

// AddUART attaches a UART peripheral at the given base address.
func (c *Chip) AddUART(base uint32, id int) {
	u := newUART(c, base, id)
	if id >= 0 && id < len(c.uart) {
		c.uart[id] = u
	}
	c.addRegBlock(u.block)
}
