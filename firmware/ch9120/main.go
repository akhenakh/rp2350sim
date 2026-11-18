// ch9120 configures the CH9120 on the Waveshare RP2350-ETH (UART1 GP21/GP20,
// CFG0 GP18, RST GP19, TCPCS GP17), connects as a TCP client and echoes the
// server's reply to the USB console.
package main

import (
	"machine"
	"time"

	"github.com/akhenakh/rp2350sim/drivers/ch9120"
)

func main() {
	cfg0 := machine.GPIO18
	rst := machine.GPIO19
	tcps := machine.GPIO17
	cfg0.Configure(machine.PinConfig{Mode: machine.PinOutput})
	rst.Configure(machine.PinConfig{Mode: machine.PinOutput})
	tcps.Configure(machine.PinConfig{Mode: machine.PinInput})

	uart := machine.UART1
	reconfigure := func(baud uint32) {
		uart.Configure(machine.UARTConfig{
			BaudRate: baud,
			TX:       machine.GPIO21,
			RX:       machine.GPIO20,
		})
	}
	reconfigure(9600)

	dev := ch9120.New(uart, cfg0, rst, tcps)
	if version, err := dev.Version(); err != nil {
		println("ch9120: version:", err.Error())
	} else {
		println("ch9120 version:", version)
	}
	err := dev.Configure(ch9120.Config{
		Mode:        ch9120.TCPClient,
		LocalIP:     [4]byte{192, 168, 1, 200},
		SubnetMask:  [4]byte{255, 255, 255, 0},
		Gateway:     [4]byte{192, 168, 1, 1},
		TargetIP:    [4]byte{127, 0, 0, 1},
		LocalPort:   2000,
		TargetPort:  9000,
		Baud:        115200,
		Reconfigure: reconfigure,
	})
	if err != nil {
		println("ch9120: configure:", err.Error())
		return
	}
	println("ch9120: configured")

	buf := make([]byte, 64)
	for {
		dev.Write([]byte("hello ch9120\n"))
		time.Sleep(200 * time.Millisecond)
		if n, _ := dev.Read(buf); n > 0 {
			println("echo:", string(buf[:n]))
		}
		time.Sleep(300 * time.Millisecond)
	}
}
