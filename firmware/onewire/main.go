// onewire reads a DS18B20 with a bit-banged 1-Wire master and prints the
// temperature on UART0. Run it with `-onewire 6` or the pico2-onewire board.
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

func low(us int) {
	pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pin.Low()
	time.Sleep(time.Duration(us) * time.Microsecond)
}

func release(us int) {
	pin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	time.Sleep(time.Duration(us) * time.Microsecond)
}

func reset() bool {
	low(500)
	release(70)
	present := !pin.Get()
	release(430)
	return present
}

func writeBit(bit byte) {
	if bit == 0 {
		low(60)
		release(10)
		return
	}
	low(5)
	release(65)
}

func writeByte(v byte) {
	for i := 0; i < 8; i++ {
		writeBit((v >> uint(i)) & 1)
	}
}

func readBit() byte {
	low(2)
	pin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	time.Sleep(10 * time.Microsecond)
	bit := byte(0)
	if pin.Get() {
		bit = 1
	}
	time.Sleep(50 * time.Microsecond)
	return bit
}

func readByte() byte {
	var v byte
	for i := 0; i < 8; i++ {
		v |= readBit() << uint(i)
	}
	return v
}

func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc ^= b
		for range 8 {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8C
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func f2(v int) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	uart.Write([]byte("onewire boot\r\n"))
	if !reset() {
		uart.Write([]byte("onewire: no presence\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	writeByte(0xCC) // skip ROM
	writeByte(0x44) // convert T
	time.Sleep(750 * time.Millisecond)

	if !reset() {
		uart.Write([]byte("onewire: no presence\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	writeByte(0xCC)
	writeByte(0xBE) // read scratchpad
	var s [9]byte
	for i := range s {
		s[i] = readByte()
	}
	if crc8(s[:8]) != s[8] {
		uart.Write([]byte("onewire: crc error\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	temp := int(int16(uint16(s[1])<<8 | uint16(s[0]))) * 100 / 16
	fmt.Fprintf(uart, "onewire: %s C\r\n", f2(temp))
	for {
		time.Sleep(time.Second)
	}
}
