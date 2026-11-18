package w6300

import "time"

// irqPoll waits for one of the state interrupt bits and acknowledges the ones
// that fired. The W6300 reports interrupts in Sn_IR and clears them by writing
// Sn_IRCLR, unlike the W5500 where writing Sn_IR acknowledges.
func (d *Device) irqPoll(sockn uint8, state uint8, deadline time.Time) uint8 {
	waitTime := 500 * time.Microsecond
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			return sockIntUnknown
		}

		irq := d.readByte(sockInt, sockAddr(sockn)) & 0b00011111
		if got := irq & state; got != 0 {
			d.writeByte(sockIntClear, sockAddr(sockn), got)
			return got
		}

		d.mu.Unlock()

		time.Sleep(waitTime)

		// Exponential backoff for polling.
		waitTime *= 2
		if waitTime > 10*time.Millisecond {
			waitTime = 10 * time.Millisecond
		}

		d.mu.Lock()
	}
}

// sendHeader emits the W6300 frame header: instruction byte (single SPI,
// read/write, block select), 16-bit address, then the fixed 8-bit dummy phase.
func (d *Device) sendHeader(addr uint16, bsb uint8, write bool) {
	buf := d.cmdBuf
	buf[0] = bsb & 0x1F
	if write {
		buf[0] |= 0x20 // RWB
	}
	buf[1] = byte(addr >> 8)
	buf[2] = byte(addr)
	buf[3] = 0 // dummy
	_ = d.bus.Tx(buf[:], nil)
}

func (d *Device) read(addr uint16, bsb uint8, p []byte) {
	if len(p) == 0 {
		return
	}
	d.cs(false)
	d.sendHeader(addr, bsb, false)
	_ = d.bus.Tx(nil, p)
	d.cs(true)
}

func (d *Device) readUint16(addr uint16, bsb uint8) uint16 {
	d.cs(false)
	d.sendHeader(addr, bsb, false)
	buf := d.cmdBuf
	_ = d.bus.Tx(nil, buf[:2])
	d.cs(true)
	return uint16(buf[1]) | uint16(buf[0])<<8
}

func (d *Device) readByte(addr uint16, bsb uint8) byte {
	d.cs(false)
	d.sendHeader(addr, bsb, false)
	r, _ := d.bus.Transfer(0)
	d.cs(true)
	return r
}

func (d *Device) write(addr uint16, bsb uint8, p []byte) {
	if len(p) == 0 {
		return
	}
	d.cs(false)
	d.sendHeader(addr, bsb, true)
	_ = d.bus.Tx(p, nil)
	d.cs(true)
}

func (d *Device) writeUint16(addr uint16, bsb uint8, v uint16) {
	d.cs(false)
	d.sendHeader(addr, bsb, true)
	buf := d.cmdBuf
	buf[0] = byte(v >> 8)
	buf[1] = byte(v & 0xff)
	_ = d.bus.Tx(buf[:2], nil)
	d.cs(true)
}

func (d *Device) writeByte(addr uint16, bsb uint8, b byte) {
	d.cs(false)
	d.sendHeader(addr, bsb, true)
	_, _ = d.bus.Transfer(b)
	d.cs(true)
}
