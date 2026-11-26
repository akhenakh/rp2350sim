// ultrasonic triggers an HC-SR04 and prints the measured distance.
package main

import (
	"fmt"
	"machine"
	"time"
)

var (
	trig = machine.GPIO6
	echo = machine.GPIO7
	uart = machine.UART0
)

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	uart.Write([]byte("ultrasonic boot\r\n"))
	trig.Configure(machine.PinConfig{Mode: machine.PinOutput})
	echo.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	trig.Low()

	for {
		trig.High()
		time.Sleep(10 * time.Microsecond)
		trig.Low()

		start := time.Now()
		for !echo.Get() && time.Since(start) < 10*time.Millisecond {
		}
		if !echo.Get() {
			uart.Write([]byte("ultrasonic: no echo\r\n"))
			time.Sleep(time.Second)
			continue
		}
		rise := time.Now()
		for echo.Get() && time.Since(rise) < 30*time.Millisecond {
		}
		width := time.Since(rise)
		cm := width.Microseconds() / 58
		fmt.Fprintf(uart, "ultrasonic: %d cm\r\n", cm)
		time.Sleep(time.Second)
	}
}
