package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
)

// Drive the RP2350-GEEK's ST7789 panel: a green band over a red field.
// Build with `task lcd`, which also exports a PNG of the framebuffer.
func main() {
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.UART0_TX_PIN})

	machine.SPI1.Configure(machine.SPIConfig{Frequency: 24 * machine.MHz})
	display := st7789.New(machine.SPI1, machine.GPIO12, machine.GPIO8, machine.GPIO9, machine.GPIO25)
	display.Configure(st7789.Config{Width: 135, Height: 240, Rotation: drivers.Rotation90})

	display.FillRectangle(0, 0, 240, 135, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	display.FillRectangle(0, 0, 240, 34, color.RGBA{R: 0, G: 255, B: 0, A: 255})

	uart.Write([]byte("lcd example running\r\n"))
	for {
		time.Sleep(time.Second)
	}
}
