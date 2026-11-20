// busmaster exercises the I2C and SPI TCP bridges: it reads two EEPROM bytes
// from 0x50 on I2C0 and the JEDEC ID from an SPI flash on SPI0.
package main

import (
	"fmt"
	"machine"
	"time"
)

func main() {
	// UART0 is the console: the bus bridges block the emulator while waiting
	// for their TCP device, so USB CDC delivery is not reliable here.
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	fmt.Fprintf(uart, "busmaster: start\r\n")

	i2c := machine.I2C0
	if err := i2c.Configure(machine.I2CConfig{
		SDA:       machine.GPIO4,
		SCL:       machine.GPIO5,
		Frequency: 100_000,
	}); err != nil {
		println("i2c configure:", err)
	}

	cs := machine.GPIO17
	cs.Configure(machine.PinConfig{Mode: machine.PinOutput})
	cs.High()

	spi := machine.SPI0
	if err := spi.Configure(machine.SPIConfig{
		SCK:       machine.GPIO18,
		SDO:       machine.GPIO19,
		SDI:       machine.GPIO16,
		Frequency: 1_000_000,
	}); err != nil {
		println("spi configure:", err)
	}

	// The first bus operation may race the TCP device connecting; retry.
	var reg [2]byte
	for attempt := 0; attempt < 20; attempt++ {
		if err := i2c.Tx(0x50, []byte{0x00}, nil); err == nil {
			if err := i2c.Tx(0x50, nil, reg[:]); err == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(uart, "i2c: %02x %02x\r\n", reg[0], reg[1])

	var id [3]byte
	for attempt := 0; attempt < 20; attempt++ {
		cs.Low()
		_ = spi.Tx([]byte{0x9f}, id[:])
		cs.High()
		if id[0] == 0xEF {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(uart, "spi: %02x %02x %02x\r\n", id[0], id[1], id[2])

	for {
		time.Sleep(time.Second)
	}
}
