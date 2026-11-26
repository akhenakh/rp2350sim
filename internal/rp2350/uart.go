package rp2350

import (
	"sync"
	"sync/atomic"
)

// uartBlock models the PL011/DW_apb UART instances (UART0 at 0x40070000,
// UART1 at 0x40078000) closely enough for TinyGo's polling and
// interrupt-driven console drivers.
const (
	uartRXIM  = 1 << 4 // UARTIMSC/UARTRIS/UARTICR RX bit
	uartRXIRQ = 33     // UART0; UART1 is 34

	uartFEN       = 1 << 4
	uartOEIRQ     = 1 << 10
	uartRSOE      = 1 << 3
	uartDROE      = 1 << 11
	uartFRRXFF    = 1 << 6
	uartFIFODepth = 32

	// Host transports deliver bursts, while the PL011 FIFO can be disabled.
	// Keep bytes waiting for FIFO space outside the architectural RX FIFO so a
	// TCP read does not manufacture an overrun before firmware can drain it.
	uartHostQueueLimit = 64 << 10
)

type uartBlock struct {
	block *regBlock
	chip  *Chip
	id    int
	irq   int
	rx    []byte
	rxMu  sync.Mutex
	// hostRx holds bytes delivered by host transports while the hardware FIFO
	// is full. It is protected by rxMu and drained as firmware reads UARTDR.
	hostRx []byte
	// rxDepth is atomic because host input reads it concurrently. Receive data
	// and error status are protected by rxMu; imsc/raw stay on the emulator thread.
	rxDepth   atomic.Int32
	rsr       uint32
	drOE      bool
	oePending bool
	oeIRQ     bool
	imsc      uint32
	raw       uint32
	// irqPending is set from the bridge/client goroutine and consumed by the
	// emulator thread so NVIC state is only mutated while stepping.
	irqPending atomic.Bool
	// OnByte receives every byte written to the data register.
	OnByte func(b byte)
	// NextRx supplies a byte when the receive queue is empty; ok=false means
	// no data is available.
	NextRx func() (b byte, ok bool)
	// dev is an optional attached device model (a plugin serial target).
	// Its TX runs before the observation callbacks.
	dev UARTDevice
}

// UARTDevice is an external device on a hardware UART. TX receives bytes the
// firmware writes; the device sends bytes back with Chip.SendUART (typically
// from a host callback while handling TX).
type UARTDevice interface {
	TX(b byte)
	Reset()
}

const (
	uartFRTXFE = 1 << 7 // transmit FIFO empty
	uartFRTXFF = 1 << 5 // transmit FIFO full
	uartFRRXFE = 1 << 4 // receive FIFO empty
	uartFRBUSY = 1 << 3 // UART busy
)

func (u *uartBlock) reset() {
	u.rxMu.Lock()
	u.rx = u.rx[:0]
	u.hostRx = u.hostRx[:0]
	u.rxDepth.Store(1)
	u.rsr = 0
	u.drOE = false
	u.oePending = false
	u.oeIRQ = false
	u.imsc = 0
	u.raw = 0
	u.irqPending.Store(false)
	if u.block != nil {
		u.block.regs[0x30/4] = 0x300
		u.block.regs[0x34/4] = 0x12
	}
	u.rxMu.Unlock()
	u.chip.ClearPendingIRQ(u.irq)
}

func newUART(c *Chip, base uint32, id int) *uartBlock {
	u := &uartBlock{block: newRegBlock(base, 0x1000), chip: c, id: id, irq: uartRXIRQ + id}
	u.block.resetFn = func(c *Chip) { u.reset() }
	u.reset()
	u.OnByte = func(b byte) {
		if u.dev != nil {
			u.dev.TX(b)
		}
		if id == 0 {
			if c.OnUART0 != nil {
				c.OnUART0(b)
			}
			return
		}
		if c.OnUART1 != nil {
			c.OnUART1(b)
		}
	}
	u.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x00: // UARTDR
			var v uint32
			u.rxMu.Lock()
			u.pollNextRxLocked()
			if len(u.rx) > 0 {
				v = uint32(u.rx[0])
				u.rx = u.rx[1:]
				if u.drOE {
					v |= uartDROE
				}
			}
			u.fillRXLocked()
			u.updateInterruptsLocked(c)
			u.rxMu.Unlock()
			return v, true
		case 0x04: // UARTRSR
			u.rxMu.Lock()
			u.updateInterruptsLocked(c)
			v := u.rsr
			u.rxMu.Unlock()
			return v, true
		case 0x18: // UARTFR
			u.rxMu.Lock()
			u.pollNextRxLocked()
			u.updateInterruptsLocked(c)
			fr := uint32(uartFRTXFE)
			if len(u.rx) == 0 {
				fr |= uartFRRXFE
			}
			if len(u.rx) >= int(u.rxDepth.Load()) {
				fr |= uartFRRXFF
			}
			u.rxMu.Unlock()
			return fr, true
		case 0x38: // UARTIMSC
			return u.imsc, true
		case 0x3c: // UARTRIS
			u.rxMu.Lock()
			u.updateInterruptsLocked(c)
			v := u.raw
			u.rxMu.Unlock()
			return v, true
		case 0x40: // UARTMIS
			u.rxMu.Lock()
			u.updateInterruptsLocked(c)
			v := u.raw & u.imsc
			u.rxMu.Unlock()
			return v, true
		case 0x44: // UARTICR (write-only)
			return 0, true
		case 0xfe0:
			return 0x11, true
		case 0xfe4:
			return 0x10, true
		case 0xfe8:
			return 0x34, true
		case 0xfec:
			return 0, true
		case 0xff0:
			return 0x0d, true
		case 0xff4:
			return 0xf0, true
		case 0xff8:
			return 0x05, true
		case 0xffc:
			return 0xb1, true
		}
		return 0, false
	}
	u.block.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0x00: // UARTDR
			if u.OnByte != nil {
				u.OnByte(byte(v))
			}
		case 0x04: // UARTECR
			u.rxMu.Lock()
			u.rsr &^= v & 0xf
			u.updateInterruptsLocked(c)
			u.rxMu.Unlock()
		case 0x2c: // UARTLCR_H
			depth := int32(1)
			if v&uartFEN != 0 {
				depth = uartFIFODepth
			}
			u.rxDepth.Store(depth)
			u.rxMu.Lock()
			if len(u.rx) > int(depth) {
				u.markOverrunLocked()
				u.rx = u.rx[:depth]
			}
			u.fillRXLocked()
			u.updateInterruptsLocked(c)
			u.rxMu.Unlock()
		case 0x38: // UARTIMSC
			u.imsc = v
			u.rxMu.Lock()
			u.updateInterruptsLocked(c)
			u.rxMu.Unlock()
		case 0x44: // UARTICR
			u.raw &^= v & 0x7ff
			u.rxMu.Lock()
			if v&uartOEIRQ != 0 {
				u.oeIRQ = false
			}
			u.updateInterruptsLocked(c)
			u.rxMu.Unlock()
		}
	}
	return u
}

func (u *uartBlock) send(data []byte) {
	if len(data) == 0 {
		return
	}
	u.rxMu.Lock()
	room := uartHostQueueLimit - len(u.hostRx)
	if room <= 0 {
		u.markOverrunLocked()
	} else {
		if len(data) > room {
			data = data[:room]
			u.markOverrunLocked()
		}
		u.hostRx = append(u.hostRx, data...)
	}
	u.fillRXLocked()
	u.irqPending.Store(true)
	u.rxMu.Unlock()
}

// fillRXLocked moves queued host bytes into the architectural FIFO. Keeping the
// two queues separate preserves FEN capacity and RXFF while allowing interrupt
// driven firmware to drain a host burst without dropping its tail.
func (u *uartBlock) fillRXLocked() {
	depth := int(u.rxDepth.Load())
	if len(u.hostRx) == 0 || len(u.rx) >= depth {
		return
	}
	n := min(depth-len(u.rx), len(u.hostRx))
	u.rx = append(u.rx, u.hostRx[:n]...)
	u.hostRx = u.hostRx[n:]
	if len(u.hostRx) == 0 {
		u.hostRx = nil
	}
}

func (u *uartBlock) markOverrunLocked() {
	u.rsr |= uartRSOE
	u.drOE = true
	u.oePending = true
}

func (u *uartBlock) pollNextRxLocked() {
	u.fillRXLocked()
	if u.NextRx == nil || len(u.rx) >= int(u.rxDepth.Load()) {
		return
	}
	if b, ok := u.NextRx(); ok {
		u.rx = append(u.rx, b)
		u.drOE = false
	}
}

func (u *uartBlock) rxThresholdLocked() int {
	if u.rxDepth.Load() == 1 {
		return 1
	}
	sel := (u.block.regs[0x34/4] >> 3) & 7
	thresholds := [...]int{4, 8, 16, 24, 28}
	if sel >= uint32(len(thresholds)) {
		sel = uint32(len(thresholds) - 1)
	}
	return thresholds[sel]
}

func (u *uartBlock) updateInterruptsLocked(c *Chip) {
	if u.oePending {
		u.oePending = false
		u.oeIRQ = true
	}
	u.raw &^= uartRXIM | uartOEIRQ
	if len(u.rx) >= u.rxThresholdLocked() {
		u.raw |= uartRXIM
	}
	if u.oeIRQ {
		u.raw |= uartOEIRQ
	}
	if u.raw&u.imsc != 0 {
		c.PendIRQ(u.irq)
	} else {
		c.ClearPendingIRQ(u.irq)
	}
}

// tick moves queued RX interrupts onto the NVIC from the emulator thread.
func (u *uartBlock) tick() {
	// The common case is no host byte waiting; a plain load is much cheaper
	// than the locked swap, which is only needed when work is pending.
	if !u.irqPending.Load() {
		return
	}
	if !u.irqPending.Swap(false) {
		return
	}
	u.rxMu.Lock()
	u.fillRXLocked()
	u.updateInterruptsLocked(u.chip)
	u.rxMu.Unlock()
}

// AddUART attaches a UART peripheral at the given base address.
func (c *Chip) AddUART(base uint32, id int) {
	u := newUART(c, base, id)
	if id >= 0 && id < len(c.uart) {
		c.uart[id] = u
	}
	c.addRegBlock(u.block)
}
