// gps reads NMEA GGA sentences from UART1 and prints the fix.
package main

import (
	"fmt"
	"machine"
	"strconv"
	"strings"
	"time"
)

var uart = machine.UART1

func checksum(line string) bool {
	star := strings.LastIndexByte(line, '*')
	if star < 0 || star+2 >= len(line) {
		return false
	}
	var sum byte
	for i := 1; i < star; i++ {
		sum ^= line[i]
	}
	want, err := strconv.ParseUint(line[star+1:star+3], 16, 8)
	return err == nil && sum == byte(want)
}

func coord(v, hemi string) float64 {
	x, _ := strconv.ParseFloat(v, 64)
	deg := float64(int(x / 100))
	out := deg + (x-deg*100)/60
	if hemi == "S" || hemi == "W" {
		out = -out
	}
	return out
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO8, RX: machine.GPIO9})
	fmt.Fprintf(uart, "gps boot\r\n")
	var line string
	for {
		if uart.Buffered() == 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		b, err := uart.ReadByte()
		if err != nil {
			continue
		}
		if b != '\n' {
			if b != '\r' {
				line += string(b)
			}
			continue
		}
		if strings.HasPrefix(line, "$GPGGA,") && checksum(line) {
			f := strings.Split(strings.TrimSuffix(line, "*"+line[len(line)-2:]), ",")
			if len(f) >= 10 {
				lat := coord(f[2], f[3])
				lon := coord(f[4], f[5])
				alt, _ := strconv.ParseFloat(f[9], 64)
				fmt.Fprintf(uart, "gps: fix=%s lat=%.6f lon=%.6f sats=%s alt=%.1f\r\n",
					f[6], lat, lon, f[7], alt)
			}
		}
		line = ""
	}
}
