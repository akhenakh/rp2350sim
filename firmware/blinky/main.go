package main

import (
	"machine"
	"time"
)

// Blink the on-board LED (GP25 on the Pico 2). This is the smallest firmware
// the emulator should always be able to run end to end.
func main() {
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	for {
		led.High()
		time.Sleep(250 * time.Millisecond)
		led.Low()
		time.Sleep(250 * time.Millisecond)
	}
}
