package rp2350

import (
	"sync"
	"sync/atomic"
)

// uartBlock models the PL011/DW_apb UART instances (UART0 at 0x40070000,
// UART1 at 0x40078000) closely enough for TinyGo's polling and
// interrupt-driven console drivers.
const (
	uartRXIM  = 0x10 // UARTIMSC/UARTRIS/UARTICR RX bit
	uartRXIRQ = 33   // UART0; UART1 is 34
)

type uartBlock struct {
	block *regBlock
	chip  *Chip
	id    int
	irq   int
	rx    []byte
	rxMu  sync.Mutex
	imsc  uint32
	raw   uint32
	// irqPending is set from the bridge/client goroutine and consumed by the
	// emulator thread so NVIC state is only mutated while stepping.
	irqPending atomic.Bool
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
	u := &uartBlock{block: newRegBlock(base, 0x1000), chip: c, id: id, irq: uartRXIRQ + id}
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
			u.rxMu.Lock()
			fr := uint32(uartFRTXFE | uartFRRXFE)
			if len(u.rx) > 0 {
				fr &^= uartFRRXFE
				u.raw |= uartRXIM
			}
			if u.NextRx != nil && len(u.rx) == 0 {
				if b, ok := u.NextRx(); ok {
					u.rx = append(u.rx, b)
					fr &^= uartFRRXFE
				}
			}
			u.rxMu.Unlock()
			return fr, true
		case 0x00: // UARTDR
			var v uint32
			u.rxMu.Lock()
			if len(u.rx) > 0 {
				v = uint32(u.rx[0])
				u.rx = u.rx[1:]
				if len(u.rx) == 0 {
					u.raw &^= uartRXIM
				}
			}
			u.rxMu.Unlock()
			return v, true
		case 0x38: // UARTIMSC
			return u.imsc, true
		case 0x3C: // UARTRIS
			return u.raw, true
		case 0x40: // UARTMIS
			return u.raw & u.imsc, true
		case 0x44: // UARTICR (write-only)
			return 0, true
		}
		return 0, false
	}
	u.block.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0x00: // UARTDR
			if u.OnByte != nil {
				u.OnByte(byte(v))
			}
		case 0x38: // UARTIMSC
			u.imsc = v
			if u.raw&u.imsc != 0 {
				c.NVIC.Pend(u.irq)
			}
		case 0x44: // UARTICR
			u.raw &^= v
		}
	}
	return u
}

// tick moves a queued RX interrupt onto the NVIC from the emulator thread.
func (u *uartBlock) tick() {
	if !u.irqPending.Swap(false) {
		return
	}
	u.raw |= uartRXIM
	if u.imsc&uartRXIM != 0 {
		u.chip.NVIC.Pend(u.irq)
	}
}

// AddUART attaches a UART peripheral at the given base address.
func (c *Chip) AddUART(base uint32, id int) {
	u := newUART(c, base, id)
	if id >= 0 && id < len(c.uart) {
		c.uart[id] = u
	}
	c.addRegBlock(u.block)
}
