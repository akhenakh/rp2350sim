package devices

import (
	"fmt"
)

// SPIBridge presents a TCP peer as one SPI device using Bus Pirate tokens:
// the first byte of a chip-select frame is "[0x9F", further bytes are "0xNN",
// and "]" releases CS. The peer answers each byte with one MISO byte.
type SPIBridge struct {
	*BusBridge
	CS      int
	started bool
}

// NewSPIBridge creates a bridge for one bus and chip-select pin.
func NewSPIBridge(bus, cs int, listen, connect string) *SPIBridge {
	b := &SPIBridge{CS: cs, BusBridge: NewBusBridge(BusSPI, bus, listen, connect)}
	b.Hello = fmt.Sprintf("SPI %d HELLO %d", bus, cs)
	return b
}

// SetCS waits for the first client on assert and sends the Bus Pirate stop on
// release. The "[" token is sent with the first byte.
func (b *SPIBridge) SetCS(pin int, level bool) {
	if pin != b.CS {
		return
	}
	if level {
		if b.started {
			b.notify("]", false)
			b.started = false
		}
		return
	}
	b.started = false
	_, _, _ = b.session(b.firstWait())
}

// Transfer clocks one byte and returns the MISO byte. The first byte carries
// the Bus Pirate "[" start token.
func (b *SPIBridge) Transfer(tx byte) byte {
	prefix := ""
	if !b.started {
		prefix = "["
		b.started = true
	}
	resp, err := b.request(fmt.Sprintf("%s0x%02X", prefix, tx))
	if err != nil {
		return 0xFF
	}
	v, ok := responseByte(resp)
	if !ok {
		return 0xFF
	}
	return v
}
