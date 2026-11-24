// presets exercises the built-in I2C/SPI device models: a BME280 and MPU6050
// on I2C0, an SSD1306 command stream on I2C0 and the W25Q16 JEDEC ID on SPI0.
package main

import (
	"fmt"
	"machine"
	"time"
)

func main() {
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	fmt.Fprintf(uart, "presets: start\r\n")

	i2c := machine.I2C0
	if err := i2c.Configure(machine.I2CConfig{
		SDA:       machine.GPIO4,
		SCL:       machine.GPIO5,
		Frequency: 100_000,
	}); err != nil {
		fmt.Fprintf(uart, "i2c configure: %v\r\n", err)
	}

	var bme [1]byte
	if err := i2c.Tx(0x76, []byte{0xD0}, bme[:]); err != nil {
		fmt.Fprintf(uart, "bme280: %v\r\n", err)
	} else {
		fmt.Fprintf(uart, "bme280: %02x\r\n", bme[0])
	}

	var mpu [1]byte
	if err := i2c.Tx(0x68, []byte{0x75}, mpu[:]); err != nil {
		fmt.Fprintf(uart, "mpu6050: %v\r\n", err)
	} else {
		fmt.Fprintf(uart, "mpu6050: %02x\r\n", mpu[0])
	}

	// SSD1306 display-on: control byte 0x00 followed by command 0xAF.
	if err := i2c.Tx(0x3C, []byte{0x00, 0xAF}, nil); err != nil {
		fmt.Fprintf(uart, "ssd1306: %v\r\n", err)
	} else {
		fmt.Fprintf(uart, "ssd1306: ok\r\n")
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
		fmt.Fprintf(uart, "spi configure: %v\r\n", err)
	} else {
		var id [3]byte
		cs.Low()
		_ = spi.Tx([]byte{0x9F}, id[:])
		cs.High()
		fmt.Fprintf(uart, "w25q16: %02x %02x %02x\r\n", id[0], id[1], id[2])
	}

	for {
		time.Sleep(time.Second)
	}
}
