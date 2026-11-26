// dht reads a DHT22 on GP6 and prints humidity and temperature on UART0.
package main

import (
	"fmt"
	"machine"
	"time"
)

var (
	pin  = machine.GPIO6
	uart = machine.UART0
)

func spin(us int) {
	start := time.Now()
	for time.Since(start) < time.Duration(us)*time.Microsecond {
	}
}

func wait(level bool, maxUS int) bool {
	start := time.Now()
	for time.Since(start) < time.Duration(maxUS)*time.Microsecond {
		if pin.Get() == level {
			return true
		}
	}
	return false
}

func f1(v int) string { return fmt.Sprintf("%d.%d", v/10, v%10) }

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	uart.Write([]byte("dht boot\r\n"))

	pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pin.Low()
	spin(1000)
	pin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	if !wait(false, 100) {
		uart.Write([]byte("dht: no low1\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	if !wait(true, 100) {
		uart.Write([]byte("dht: no high1\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	if !wait(false, 100) {
		uart.Write([]byte("dht: no low2\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}

	var data [5]byte
	// The last bit has no trailing low period before the bus idles high;
	// read the first 39 bits and derive the final checksum bit.
	for i := range 39 {
		if !wait(true, 100) {
			uart.Write([]byte("dht: bit high timeout\r\n"))
			for {
				time.Sleep(time.Second)
			}
		}
		n := 0
		for pin.Get() && n < 100000 {
			n++
		}
		bit := byte(0)
		if n > 1000 {
			bit = 1
		}
		data[i/8] |= bit << uint(i%8)
		if !wait(false, 100) {
			uart.Write([]byte("dht: bit low timeout\r\n"))
			for {
				time.Sleep(time.Second)
			}
		}
	}
	var sum byte
	for _, b := range data[:4] {
		sum += b
	}
	if data[4]&0x7f != sum&0x7f {
		uart.Write([]byte("dht: checksum\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	data[4] = sum
	hum := int(uint16(data[0])<<8 | uint16(data[1]))
	temp := int(int16(uint16(data[2])<<8 | uint16(data[3])))
	fmt.Fprintf(uart, "dht: %s %% %s C\r\n", f1(hum), f1(temp))
	for {
		time.Sleep(time.Second)
	}
}
