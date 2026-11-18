// serialbridge echoes UART1 so the TCP serial bridge can be exercised: connect
// a TCP client to the listener and every byte is returned by the firmware.
package main

import (
	"machine"
	"time"
)

func main() {
	uart := machine.UART1
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO4, RX: machine.GPIO5})
	println("serial bridge: ready")
	buf := make([]byte, 64)
	for {
		n, _ := uart.Read(buf)
		if n > 0 {
			uart.Write(buf[:n])
		}
		time.Sleep(time.Millisecond)
	}
}
