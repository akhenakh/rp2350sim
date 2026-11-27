// pico2wble exercises the CYW43439 Bluetooth shared bus: it downloads the BT
// firmware, opens the HCI transport and sends HCI Reset, expecting the command
// complete event. It is the first end-to-end check of the gSPI BT transport.
package main

import (
	"time"

	"github.com/soypat/cyw43439"
)

func main() {
	time.Sleep(500 * time.Millisecond)
	println("pico2wble: init")
	dev := cyw43439.NewPicoWDevice()
	if err := dev.Init(cyw43439.DefaultBluetoothConfig()); err != nil {
		println("pico2wble: init failed:", err.Error())
		hang()
	}
	println("pico2wble: bt ready")
	hci, err := dev.HCIReadWriter()
	if err != nil {
		println("pico2wble: hci:", err.Error())
		hang()
	}
	// HCI Reset: command packet, opcode 0x0C03, no parameters.
	if _, err := hci.Write([]byte{0x01, 0x03, 0x0C, 0x00}); err != nil {
		println("pico2wble: write:", err.Error())
		hang()
	}
	buf := make([]byte, 256)
	for i := 0; i < 100; i++ {
		if hci.Buffered() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	n, err := hci.Read(buf)
	if err != nil {
		println("pico2wble: read:", err.Error())
		hang()
	}
	println("pico2wble: hci bytes:", n)
	if n >= 3 && buf[0] == 0x04 && buf[1] == 0x0E {
		println("pico2wble: reset complete event")
	} else {
		println("pico2wble: unexpected event")
	}
	// Read BD_ADDR (0x1009): the controller derives it from the peer address.
	if _, err := hci.Write([]byte{0x01, 0x09, 0x10, 0x00}); err != nil {
		println("pico2wble: write:", err.Error())
		hang()
	}
	for i := 0; i < 100; i++ {
		if hci.Buffered() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	n, err = hci.Read(buf)
	if err != nil {
		println("pico2wble: read:", err.Error())
		hang()
	}
	if n >= 3 && buf[0] == 0x04 && buf[1] == 0x0E {
		println("pico2wble: bd_addr event")
	}
	println("pico2wble: done")
	hang()
}

func hang() {
	for {
		time.Sleep(time.Second)
	}
}
