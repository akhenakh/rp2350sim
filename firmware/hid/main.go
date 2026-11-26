// USB HID keyboard and mouse over the composite CDC+HID descriptor. TinyGo's
// keyboard uses report ID 2 and the mouse report ID 1; the emulator host
// decodes both on the same interrupt endpoint.
package main

import (
	"machine/usb/hid/keyboard"
	"machine/usb/hid/mouse"
	"time"
)

func main() {
	println("hid: keyboard + mouse")
	kb := keyboard.Port()
	ms := mouse.Port()

	line := 0
	for {
		if line%2 == 0 {
			_, _ = kb.Write([]byte("Hi"))
		} else {
			ms.Move(12, -6)
			ms.Click(mouse.Left)
		}
		line++
		time.Sleep(time.Second)
	}
}
