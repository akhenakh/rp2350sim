// pico2w initializes the real CYW43439 driver and toggles WL_GPIO0, the
// Pico W's on-board LED. It is the first end-to-end gSPI check: the driver
// resets the radio, downloads firmware and CLM/NVRAM, and sends the LED
// control iovar over the control channel.
package main

import (
	"time"

	"github.com/soypat/cyw43439"
)

func main() {
	time.Sleep(500 * time.Millisecond)
	println("pico2w: init")
	dev := cyw43439.NewPicoWDevice()
	if err := dev.Init(cyw43439.DefaultWifiConfig()); err != nil {
		println("pico2w: init failed:", err.Error())
		for {
			time.Sleep(time.Second)
		}
	}
	println("pico2w: radio ready")
	on := false
	for {
		on = !on
		if err := dev.GPIOSet(0, on); err != nil {
			println("pico2w: led:", err.Error())
		} else if on {
			println("pico2w: led on")
		} else {
			println("pico2w: led off")
		}
		time.Sleep(time.Second)
	}
}
