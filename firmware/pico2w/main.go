// pico2w initializes the real CYW43439 driver, joins the virtual AP and
// toggles WL_GPIO0, the Pico W's on-board LED. It is the first end-to-end gSPI
// check: the driver resets the radio, downloads firmware and CLM/NVRAM, sends
// the WLC control iovars, joins and drives the LED.
package main

import (
	"time"

	"github.com/soypat/cyw43439"
)

const (
	ssid = "rp2350sim"
	pass = "password"
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
	if err := dev.Join(ssid, cyw43439.JoinOptions{Passphrase: pass}); err != nil {
		println("pico2w: join failed:", err.Error())
	} else {
		println("pico2w: joined", ssid)
	}
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
