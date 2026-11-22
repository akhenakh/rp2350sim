// Generate a ~440 Hz square wave on GP0. The audio decoder's PWM/GPIO mode
// averages the pin into 48 kHz PCM, giving the smoke suite a deterministic
// tone without an external codec.
package main

import (
	"machine"
	"time"
)

func main() {
	pin := machine.GPIO0
	pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	half := 1136 * time.Microsecond // ~440 Hz
	for {
		pin.High()
		time.Sleep(half)
		pin.Low()
		time.Sleep(half)
	}
}
