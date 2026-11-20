package devices

import (
	"fmt"
	"strconv"
	"strings"
)

// I2CBridge presents a TCP peer as one or more I2C targets using Bus Pirate
// style tokens. The controller sends "[0xA0" (start + write address), data
// bytes as "0xNN", "[0xA1 r:1" / "r:1" for reads, and "]" for stop. The peer
// answers ACK/NAK or one data byte.
type I2CBridge struct {
	*BusBridge
	Addrs   []uint8
	cur     uint8
	started bool
}

// NewI2CBridge creates a bridge for the given addresses. listen and connect
// are mutually exclusive; an empty listen defaults to 127.0.0.1:0.
func NewI2CBridge(bus int, addrs []uint8, listen, connect string) *I2CBridge {
	b := &I2CBridge{
		BusBridge: NewBusBridge(BusI2C, bus, listen, connect),
		Addrs:     append([]uint8(nil), addrs...),
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = fmt.Sprintf("0x%02X", uint16(a)<<1)
	}
	b.Hello = fmt.Sprintf("I2C %d HELLO %s", bus, strings.Join(parts, ","))
	return b
}

// SetAddress lets the controller tell the bridge which TAR was latched.
func (b *I2CBridge) SetAddress(addr uint8) { b.cur = addr }

// BeginTransaction starts a new Bus Pirate transaction; the start token itself
// is sent with the first read/write so its direction is known.
func (b *I2CBridge) BeginTransaction() { b.started = false }

// EndTransaction sends the Bus Pirate stop token.
func (b *I2CBridge) EndTransaction() {
	if b.started {
		b.notify("]", false)
	}
	b.started = false
}

// Write implements I2CDevice; the ACK is best-effort for legacy callers.
func (b *I2CBridge) Write(v byte) { _ = b.WriteAck(v) }

// WriteAck sends the start/address on the first byte, then the data byte.
func (b *I2CBridge) WriteAck(v byte) bool {
	if !b.started {
		resp, err := b.request(fmt.Sprintf("[0x%02X", uint16(b.cur)<<1))
		if err != nil || !isAck(resp) {
			return false
		}
		b.started = true
	}
	resp, err := b.request(fmt.Sprintf("0x%02X", v))
	return err == nil && isAck(resp)
}

// Read implements I2CDevice; it returns 0xff when the peer has no data.
func (b *I2CBridge) Read() byte {
	v, _ := b.ReadAck()
	return v
}

// ReadAck requests one byte. The first read sends a repeated start.
func (b *I2CBridge) ReadAck() (byte, bool) {
	var line string
	if !b.started {
		line = fmt.Sprintf("[0x%02X r:1", uint16(b.cur)<<1|1)
		b.started = true
	} else {
		line = "r:1"
	}
	resp, err := b.request(line)
	if err != nil {
		return 0xFF, false
	}
	if isNak(resp) {
		return 0xFF, false
	}
	return responseByte(resp)
}

func responseByte(resp string) (byte, bool) {
	fields := strings.Fields(resp)
	if len(fields) == 0 {
		return 0xFF, false
	}
	tok := strings.TrimPrefix(fields[len(fields)-1], "0x")
	v, err := strconv.ParseUint(tok, 16, 8)
	if err != nil {
		return 0xFF, false
	}
	return byte(v), true
}
