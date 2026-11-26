// can exchanges a classic CAN frame with an MCP2515 on SPI0 and a simulated
// peer on the emulator's CAN bus. It polls the controller registers.
package main

import (
	"fmt"
	"machine"
	"time"
)

var (
	spi  = machine.SPI0
	cs   = machine.GPIO17
	uart = machine.UART0
)

func selectSPI(on bool) {
	if on {
		cs.Low()
	} else {
		cs.High()
	}
}

func transfer(b byte) byte { v, _ := spi.Transfer(b); return v }

func reset() {
	selectSPI(true)
	transfer(0xC0)
	selectSPI(false)
	time.Sleep(10 * time.Millisecond)
}

func write(addr byte, data ...byte) {
	selectSPI(true)
	transfer(0x02)
	transfer(addr)
	for _, b := range data {
		transfer(b)
	}
	selectSPI(false)
}

func read(addr byte, n int) []byte {
	out := make([]byte, n)
	selectSPI(true)
	transfer(0x03)
	transfer(addr)
	for i := range out {
		out[i] = transfer(0)
	}
	selectSPI(false)
	return out
}

func rts(mask byte) {
	selectSPI(true)
	transfer(0x80 | mask)
	selectSPI(false)
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	spi.Configure(machine.SPIConfig{Frequency: 8 * machine.MHz, Mode: 0})
	cs.Configure(machine.PinConfig{Mode: machine.PinOutput})
	cs.High()
	machine.GPIO20.Configure(machine.PinConfig{Mode: machine.PinInputPullup})

	reset()
	write(0x28, 0x03) // CNF3
	write(0x29, 0x90) // CNF2
	write(0x2A, 0x02) // CNF1
	write(0x2B, 0x03) // CANINTE RX0/RX1
	write(0x2C, 0x00) // clear interrupts
	write(0x0F, 0x00) // normal mode

	// TXB0: standard ID 0x123, one data byte 0x41.
	write(0x31, 0x24, 0x60, 0x00, 0x00, 0x01, 0x41)
	write(0x30, 0x08) // TXREQ
	rts(0x01)

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		st := read(0x2C, 1)[0]
		if st&0x01 != 0 { // RX0IF
			break
		}
	}
	if read(0x2C, 1)[0]&0x01 == 0 {
		uart.Write([]byte("can: no reply\r\n"))
		for {
			time.Sleep(time.Second)
		}
	}
	rx := read(0x61, 6)
	id := uint32(rx[0])<<3 | uint32(rx[1])>>5
	fmt.Fprintf(uart, "can: tx=123 rx=%03x data=%02x\r\n", id, rx[5])
	for {
		time.Sleep(time.Second)
	}
}
