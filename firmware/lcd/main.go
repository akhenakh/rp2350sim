// Drive the RP2350-GEEK's ST7789 panel. The animation exercises full-frame
// fills and partial updates: three palette bands slide, a white cursor sweeps
// across the panel, and a progress bar grows along the top edge.
// Build with `task lcd`, which also exports a PNG of the framebuffer.
package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
)

var palettes = [][3]color.RGBA{
	{
		{R: 255, G: 56, B: 56, A: 255},
		{R: 255, G: 176, B: 32, A: 255},
		{R: 48, G: 200, B: 120, A: 255},
	},
	{
		{R: 64, G: 132, B: 255, A: 255},
		{R: 168, G: 96, B: 255, A: 255},
		{R: 64, G: 232, B: 232, A: 255},
	},
}

var (
	background = color.RGBA{R: 12, G: 14, B: 20, A: 255}
	white      = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

func main() {
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.UART0_TX_PIN})

	machine.SPI1.Configure(machine.SPIConfig{Frequency: 24 * machine.MHz})
	display := st7789.New(machine.SPI1, machine.GPIO12, machine.GPIO8, machine.GPIO9, machine.GPIO25)
	display.Configure(st7789.Config{Width: 135, Height: 240, Rotation: drivers.Rotation90})

	uart.Write([]byte("lcd example running\r\n"))

	const (
		width  = 240
		height = 135
		band   = 45
	)

	var frame int
	for {
		p := palettes[(frame/150)%len(palettes)]

		_ = display.FillRectangle(0, 0, width, height, background)

		// Bands bounce up and down so no wrap seam is visible.
		phase := frame % 90
		if phase > 45 {
			phase = 90 - phase
		}
		for i, c := range p {
			y0 := int16(-15 + i*band + phase)
			y1 := y0 + band
			if y0 < 0 {
				y0 = 0
			}
			if y1 > height {
				y1 = height
			}
			if y1 > y0 {
				_ = display.FillRectangle(0, y0, width, y1-y0, c)
			}
		}

		// Sweep a cursor across the panel, erasing the previous column.
		x := int16(frame % (width - 4))
		_ = display.FillRectangle(x, 0, 4, height, white)
		_ = display.FillRectangle((x+width-6)%width, 0, 4, height, background)

		// Progress bar along the top edge, redrawn every 30 frames.
		if frame%30 == 0 {
			_ = display.FillRectangle(4, 4, int16(4+frame%232), 3, white)
		}

		frame++
		time.Sleep(40 * time.Millisecond)
	}
}
