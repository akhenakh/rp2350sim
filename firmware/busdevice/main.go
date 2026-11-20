// busdevice is the second simulator in the I2C/SPI TCP bridge test. It speaks
// the bridge text protocol on UART0 (an EEPROM at 0x50) and UART1 (a JEDEC SPI
// flash). Logs go to the USB CDC console, never on the protocol UARTs.
package main

import (
	"fmt"
	"machine"
	"strconv"
	"strings"
	"time"
)

type i2cState struct {
	mem     [256]byte
	ptr     byte
	first   bool
	reading bool
}

type spiState struct {
	cmd  int
	resp int
}

func main() {
	u0 := machine.UART0
	u0.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	u1 := machine.UART1
	u1.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO4, RX: machine.GPIO5})

	i2c := &i2cState{first: true}
	i2c.mem[0] = 0x12
	i2c.mem[1] = 0x34
	spi := &spiState{cmd: -1}

	println("busdevice: ready")
	go serve(u0, "I2C", i2c, nil)
	go serve(u1, "SPI", nil, spi)
	for {
		time.Sleep(time.Second)
	}
}

func serve(uart *machine.UART, kind string, i2c *i2cState, spi *spiState) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := uart.Read(buf)
		if n == 0 {
			// Read is non-blocking; yield so the other UART gets scheduled.
			time.Sleep(time.Millisecond)
			_ = err
			continue
		}
		b := buf[0]
		if b != '\n' {
			if b != '\r' && len(line) < 128 {
				line = append(line, b)
			}
			continue
		}
		text := strings.TrimSpace(string(line))
		line = line[:0]
		if text == "" {
			continue
		}
		var resp string
		if kind == "I2C" {
			resp = handleI2C(text, i2c)
		} else {
			resp = handleSPI(text, spi)
		}
		if resp != "" {
			_, _ = fmt.Fprintf(uart, "%s\n", resp)
		}
	}
}

func handleI2C(line string, st *i2cState) string {
	if strings.Contains(line, "HELLO") {
		return ""
	}
	if line == "]" {
		st.first = true
		st.reading = false
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	start := strings.HasPrefix(first, "[")
	tok := strings.TrimPrefix(first, "[")
	if strings.HasPrefix(tok, "r:") {
		return i2cData(st, readCount(tok))
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(tok, "0x"), 16, 8)
	if err != nil {
		return ""
	}
	if start {
		st.reading = v&1 != 0
		if uint8(v>>1) != 0x50 {
			return "NAK"
		}
		if st.reading {
			n := 1
			if len(fields) > 1 && strings.HasPrefix(fields[1], "r:") {
				n = readCount(fields[1])
			}
			return i2cData(st, n)
		}
		st.first = true
		return "ACK"
	}
	if st.reading {
		return i2cData(st, 1)
	}
	if st.first {
		st.ptr = byte(v)
		st.first = false
	} else {
		st.mem[st.ptr] = byte(v)
		st.ptr++
	}
	return "ACK"
}

func readCount(tok string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(tok, "r:"))
	if err != nil || n <= 0 {
		return 1
	}
	return n
}

func i2cData(st *i2cState, n int) string {
	parts := make([]string, 0, n)
	for range n {
		parts = append(parts, fmt.Sprintf("0x%02X", st.mem[st.ptr]))
		st.ptr++
	}
	return strings.Join(parts, " ")
}

func handleSPI(line string, st *spiState) string {
	if strings.Contains(line, "HELLO") {
		return ""
	}
	if line == "]" {
		st.cmd = -1
		st.resp = 0
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	start := strings.HasPrefix(first, "[")
	tok := strings.TrimPrefix(first, "[")
	v, err := strconv.ParseUint(strings.TrimPrefix(tok, "0x"), 16, 8)
	if err != nil {
		return ""
	}
	if start {
		st.cmd = -1
		st.resp = 0
	}
	if st.cmd < 0 {
		st.cmd = int(v)
	}
	rx := byte(0xFF)
	if st.cmd == 0x9F {
		switch st.resp {
		case 0:
			rx = 0xEF
		case 1:
			rx = 0x40
		case 2:
			rx = 0x18
		}
		st.resp++
	}
	return fmt.Sprintf("0x%02X", rx)
}
