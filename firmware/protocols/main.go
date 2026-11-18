// protocols drives UART0, I2C0 and SPI0 so the UI logic analyzer has traffic
// to decode. The I2C target is absent, so writes NACK; the analyzer still
// shows the address and data phases.
package main

import (
	"machine"
	"time"
)

func main() {
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})

	i2c := machine.I2C0
	i2c.Configure(machine.I2CConfig{SDA: machine.GPIO4, SCL: machine.GPIO5, Frequency: 100_000})

	spi := machine.SPI0
	spi.Configure(machine.SPIConfig{
		SCK:       machine.GPIO18,
		SDO:       machine.GPIO19,
		SDI:       machine.GPIO16,
		Frequency: 1_000_000,
	})

	payload := []byte{0xa5, 0x3c, 0x01}
	for {
		uart.Write([]byte("proto\r\n"))
		i2c.Tx(0x50, []byte{0x00, 0x5a}, nil)
		spi.Tx(payload, nil)
		time.Sleep(5 * time.Millisecond)
	}
}
