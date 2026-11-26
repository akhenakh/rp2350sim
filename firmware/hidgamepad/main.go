// USB HID gamepad over TinyGo's CDCJoystick composite descriptor. The default
// report is ID 1: 16 buttons, one hat, then six 16-bit axes
// (X, Y, Z, Rx, Ry, Rz).
package main

import (
	"machine/usb/hid/joystick"
	"time"
)

func main() {
	println("hid: gamepad")
	js := joystick.Port()
	tick := 0
	for {
		js.SetButton(0, tick%2 == 0) // A
		js.SetButton(2, tick%3 == 0) // X
		js.SetAxis(0, 12000*((tick%5)-2))
		js.SetAxis(1, -8000*((tick%3)-1))
		js.SetAxis(2, 0)
		js.SetAxis(3, 0)
		js.SetAxis(4, 0)
		js.SetAxis(5, 0)
		if tick%4 == 0 {
			js.SetHat(0, joystick.HatRight)
		} else {
			js.SetHat(0, joystick.HatCenter)
		}
		js.SendState()
		tick++
		time.Sleep(time.Second)
	}
}
