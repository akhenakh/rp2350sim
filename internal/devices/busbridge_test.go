package devices

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func dialBridge(t *testing.T, br *BusBridge) net.Conn {
	t.Helper()
	if err := br.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)
	listen, _, _, _ := br.Status()
	conn, err := net.DialTimeout("tcp", listen, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readLineErr(r *bufio.Reader, want string) error {
	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	line = strings.TrimSpace(line)
	if line != want {
		return fmt.Errorf("line = %q, want %q", line, want)
	}
	return nil
}

func readLine(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	if err := readLineErr(r, want); err != nil {
		t.Fatal(err)
	}
}

func TestI2CBridgeExchange(t *testing.T) {
	br := NewI2CBridge(0, []uint8{0x50}, "127.0.0.1:0", "")
	br.Timeout = 500 * time.Millisecond
	br.Wait = time.Second
	conn := dialBridge(t, br.BusBridge)
	r := bufio.NewReader(conn)
	readLine(t, r, "I2C 0 HELLO 0xA0")

	done := make(chan error, 1)
	go func() {
		if err := readLineErr(r, "[0xA0"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "ACK\n")
		if err := readLineErr(r, "0x00"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "ACK\n")
		if err := readLineErr(r, "[0xA1 r:1"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "0x12\n")
		if err := readLineErr(r, "r:1"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "0x34\n")
		done <- readLineErr(r, "]")
	}()

	br.SetAddress(0x50)
	br.BeginTransaction()
	if !br.WriteAck(0x00) {
		t.Fatal("write was not ACKed")
	}
	// The controller writes TAR again for the repeated-start read.
	br.SetAddress(0x50)
	br.BeginTransaction()
	v, ok := br.ReadAck()
	if !ok || v != 0x12 {
		t.Fatalf("read = %#x, %v; want 0x12, true", v, ok)
	}
	v, ok = br.ReadAck()
	if !ok || v != 0x34 {
		t.Fatalf("second read = %#x, %v; want 0x34, true", v, ok)
	}
	br.EndTransaction()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSPIBridgeExchange(t *testing.T) {
	br := NewSPIBridge(0, 17, "127.0.0.1:0", "")
	br.Timeout = 500 * time.Millisecond
	br.Wait = time.Second
	conn := dialBridge(t, br.BusBridge)
	r := bufio.NewReader(conn)
	readLine(t, r, "SPI 0 HELLO 17")

	done := make(chan error, 1)
	go func() {
		if err := readLineErr(r, "[0x9F"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "0xEF\n")
		if err := readLineErr(r, "0x00"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "0x40\n")
		done <- readLineErr(r, "]")
	}()

	br.SetCS(17, false)
	if got := br.Transfer(0x9f); got != 0xEF {
		t.Fatalf("MISO = %#x, want 0xef", got)
	}
	if got := br.Transfer(0x00); got != 0x40 {
		t.Fatalf("MISO = %#x, want 0x40", got)
	}
	br.SetCS(17, true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBusBridgeNoClient(t *testing.T) {
	br := NewI2CBridge(0, []uint8{0x50}, "127.0.0.1:0", "")
	br.Timeout = 50 * time.Millisecond
	br.Wait = 50 * time.Millisecond
	if err := br.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)

	start := time.Now()
	if br.WriteAck(0) {
		t.Fatal("write ACKed without a client")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("no-client write took %v", elapsed)
	}
	if _, ok := br.ReadAck(); ok {
		t.Fatal("read succeeded without a client")
	}

	spi := NewSPIBridge(0, 17, "127.0.0.1:0", "")
	spi.Timeout = 50 * time.Millisecond
	spi.Wait = 50 * time.Millisecond
	if err := spi.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(spi.Close)
	if got := spi.Transfer(0); got != 0xFF {
		t.Fatalf("missing SPI client returned %#x", got)
	}
}

func TestI2CBridgeConnectMode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		if err := readLineErr(r, "I2C 0 HELLO 0xA0"); err != nil {
			done <- err
			return
		}
		if err := readLineErr(r, "[0xA0"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "ACK\n")
		if err := readLineErr(r, "0x5A"); err != nil {
			done <- err
			return
		}
		_, _ = fmt.Fprint(conn, "NAK\n")
		done <- readLineErr(r, "]")
	}()

	br := NewI2CBridge(0, []uint8{0x50}, "", ln.Addr().String())
	br.Timeout = time.Second
	br.Wait = 2 * time.Second
	if err := br.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)

	br.SetAddress(0x50)
	br.BeginTransaction()
	if br.WriteAck(0x5a) {
		t.Fatal("NAK was reported as ACK")
	}
	br.EndTransaction()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
