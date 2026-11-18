// uart prints a line on UART0 every 5 ms so the UI logic analyzer has
// something to decode.
package main

import (
	"machine"
	"time"
)

func main() {
	uart := machine.UART0
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	for {
		uart.Write([]byte("hello logic\r\n"))
		time.Sleep(5 * time.Millisecond)
	}
}
