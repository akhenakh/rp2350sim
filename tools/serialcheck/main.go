// serialcheck connects to a TCP UART bridge, sends a message and waits for the
// emulated firmware to echo it back. Used by the smoke test.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "", "bridge address (required)")
	msg := flag.String("send", "serial-check\n", "message to send")
	timeout := flag.Duration("timeout", 4*time.Second, "total timeout")
	flag.Parse()
	if *addr == "" {
		fmt.Fprintln(os.Stderr, "usage: serialcheck -addr host:port [-send text]")
		os.Exit(2)
	}

	deadline := time.Now().Add(*timeout)
	var conn net.Conn
	for {
		c, err := net.DialTimeout("tcp", *addr, 200*time.Millisecond)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "serialcheck:", err)
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer conn.Close()

	want := []byte(strings.TrimSuffix(*msg, "\n"))
	if _, err := conn.Write([]byte(*msg)); err != nil {
		fmt.Fprintln(os.Stderr, "serialcheck:", err)
		os.Exit(1)
	}
	conn.SetReadDeadline(deadline)
	var got []byte
	buf := make([]byte, 256)
	for !bytes.Contains(got, want) {
		n, err := conn.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "serialcheck: echoed %q: %v\n", got, err)
			os.Exit(1)
		}
	}
	fmt.Printf("serialcheck: echoed %q\n", got)
}
