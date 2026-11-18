package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers/ws2812"
)

// Drive a three-LED WS2812B strip on GP0 through the TinyGo PIO driver and
// rotate the colours every 500 ms. The emulator decodes the same wire
// protocol with `-ws2812 0`.
func main() {
	leds := ws2812.New(machine.GPIO0)
	frame := []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
	}
	for {
		leds.WriteColors(frame)
		time.Sleep(500 * time.Millisecond)
		first := frame[0]
		copy(frame, frame[1:])
		frame[len(frame)-1] = first
	}
}
