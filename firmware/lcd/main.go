// Drive the RP2350-GEEK's ST7789 panel with a TinyGo font splash. The title
// color and the accent bars cycle through the palette, so full-frame fills,
// text rendering and partial updates are all exercised.
// Build with `task lcd`, which also exports a PNG of the framebuffer.
package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freesans"
)

var (
	background = color.RGBA{R: 12, G: 14, B: 20, A: 255}
	white      = color.RGBA{R: 240, G: 244, B: 248, A: 255}
	muted      = color.RGBA{R: 126, G: 138, B: 154, A: 255}
	accent     = [3]color.RGBA{
		{R: 255, G: 56, B: 56, A: 255},
		{R: 255, G: 176, B: 32, A: 255},
		{R: 48, G: 200, B: 120, A: 255},
	}
)

func main() {
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.UART0_TX_PIN})

	machine.SPI1.Configure(machine.SPIConfig{Frequency: 24 * machine.MHz})
	display := st7789.New(machine.SPI1, machine.GPIO12, machine.GPIO8, machine.GPIO9, machine.GPIO25)
	display.Configure(st7789.Config{Width: 135, Height: 240, Rotation: drivers.Rotation90})

	uart.Write([]byte("lcd example running\r\n"))

	var frame int
	for {
		p := accent[(frame/90)%len(accent)]

		_ = display.FillRectangle(0, 0, 240, 135, background)

		// A thin accent line and three palette swatches keep the animation
		// visible without hiding the text.
		_ = display.FillRectangle(0, 0, 240, 3, p)
		for i, c := range accent {
			_ = display.FillRectangle(int16(12+i*14), 116, 10, 6, c)
		}

		tinyfont.WriteLine(&display, &freesans.Bold18pt7b, 12, 38, "rp2350sim", white)
		tinyfont.WriteLine(&display, &freesans.Regular9pt7b, 12, 66, "RP2350-GEEK", p)
		tinyfont.WriteLine(&display, &freesans.Regular9pt7b, 12, 90, "ST7789 240x135", muted)
		tinyfont.WriteLine(&display, &freesans.Regular9pt7b, 12, 110, "TinyGo fonts", white)

		frame++
		time.Sleep(50 * time.Millisecond)
	}
}
