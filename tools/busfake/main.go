// busfake is a small external device for the rp2350sim I2C/SPI TCP bridges.
// It connects to a listening bridge (or listens for a bridge in connect mode)
// and answers the text-line protocol: an I2C EEPROM at -i2c-addr and a JEDEC
// SPI flash on -spi-cs.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type fake struct {
	kind    string
	i2cAddr uint8
	spiCS   int
	verbose bool

	mem     [256]byte
	ptr     byte
	first   bool
	reading bool

	spiCmd  int
	spiResp int
}

func main() {
	kind := flag.String("kind", "i2c", "device kind: i2c or spi")
	addr := flag.String("addr", "", "bridge address to connect to")
	listen := flag.String("listen", "", "listen for a bridge in connect mode")
	i2cAddr := flag.Int("i2c-addr", 0x50, "I2C address to emulate")
	spiCS := flag.Int("spi-cs", 17, "SPI chip-select GPIO to emulate")
	verbose := flag.Bool("v", false, "log protocol lines")
	flag.Parse()

	if (*addr == "") == (*listen == "") {
		fmt.Fprintln(os.Stderr, "usage: busfake -kind i2c|spi (-addr host:port | -listen host:port)")
		os.Exit(2)
	}
	if *kind != "i2c" && *kind != "spi" {
		fmt.Fprintln(os.Stderr, "busfake: -kind must be i2c or spi")
		os.Exit(2)
	}

	var conn net.Conn
	var err error
	if *listen != "" {
		ln, lerr := net.Listen("tcp", *listen)
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "busfake:", lerr)
			os.Exit(1)
		}
		defer func() { _ = ln.Close() }()
		fmt.Println("busfake: listening", ln.Addr().String())
		conn, err = ln.Accept()
	} else {
		conn, err = dial(*addr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "busfake:", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	f := &fake{kind: *kind, i2cAddr: uint8(*i2cAddr), spiCS: *spiCS, verbose: *verbose}
	f.mem[0] = 0x12
	f.mem[1] = 0x34
	f.first = true
	f.spiCmd = -1
	if err := f.run(conn); err != nil {
		fmt.Fprintln(os.Stderr, "busfake:", err)
		os.Exit(1)
	}
}

func dial(addr string) (net.Conn, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fake) run(conn net.Conn) error {
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if f.verbose {
			fmt.Fprintln(os.Stderr, "<", line)
		}
		resp := f.handle(line)
		if resp == "" {
			continue
		}
		if f.verbose {
			fmt.Fprintln(os.Stderr, ">", resp)
		}
		if _, err := fmt.Fprintf(conn, "%s\n", resp); err != nil {
			return err
		}
	}
}

func (f *fake) handle(line string) string {
	if strings.Contains(line, "HELLO") {
		return ""
	}
	if line == "]" {
		f.first = true
		f.reading = false
		f.spiCmd = -1
		f.spiResp = 0
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
		n := readCount(tok)
		return f.readData(n)
	}
	v, ok := parseHex(tok)
	if !ok {
		return ""
	}
	if f.kind == "i2c" {
		return f.handleI2C(start, byte(v), fields)
	}
	return f.handleSPI(start, byte(v))
}

func readCount(tok string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(tok, "r:"))
	if err != nil || n <= 0 {
		return 1
	}
	return n
}

func (f *fake) readData(n int) string {
	var parts []string
	for range n {
		parts = append(parts, fmt.Sprintf("0x%02X", f.mem[f.ptr]))
		f.ptr++
	}
	return strings.Join(parts, " ")
}

func (f *fake) handleI2C(start bool, v byte, fields []string) string {
	if start {
		f.reading = v&1 != 0
		if uint8(v>>1) != f.i2cAddr {
			return "NAK"
		}
		if f.reading {
			n := 1
			if len(fields) > 1 && strings.HasPrefix(fields[1], "r:") {
				n = readCount(fields[1])
			}
			return f.readData(n)
		}
		f.first = true
		return "ACK"
	}
	if f.reading {
		return f.readData(1)
	}
	if f.first {
		f.ptr = v
		f.first = false
	} else {
		f.mem[f.ptr] = v
		f.ptr++
	}
	return "ACK"
}

func (f *fake) handleSPI(start bool, v byte) string {
	if start {
		f.spiCmd = -1
		f.spiResp = 0
	}
	if f.spiCmd < 0 {
		f.spiCmd = int(v)
	}
	rx := byte(0xFF)
	if f.spiCmd == 0x9F {
		switch f.spiResp {
		case 0:
			rx = 0xEF
		case 1:
			rx = 0x40
		case 2:
			rx = 0x18
		}
		f.spiResp++
	}
	return fmt.Sprintf("0x%02X", rx)
}

func parseHex(s string) (uint64, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 8)
	return v, err == nil
}
