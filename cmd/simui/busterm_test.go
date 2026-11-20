package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
)

func TestNeedsBusResponse(t *testing.T) {
	cases := map[string]bool{
		"I2C 0 HELLO 0xA0": false,
		"]":                false,
		"[0xA0":            true,
		"0x00":             true,
		"[0xA1 r:1":        true,
		"r:1":              true,
		"[0x9F":            true,
	}
	for line, want := range cases {
		if got := needsBusResponse(line); got != want {
			t.Errorf("needsBusResponse(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestBusTerminalSendsResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("[0xA0\n"))
		r := bufio.NewReader(conn)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		got <- strings.TrimSpace(line)
	}()

	term, err := NewBusTerminal(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Send("ACK")

	select {
	case v := <-got:
		if v != "ACK" {
			t.Fatalf("terminal sent %q, want ACK", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal did not answer the request")
	}
}

func TestBusTerminalAnswersI2CBridge(t *testing.T) {
	br := devices.NewI2CBridge(0, []uint8{0x50}, "127.0.0.1:0", "")
	br.Timeout = time.Second
	br.Wait = time.Second
	if err := br.Start(); err != nil {
		t.Fatal(err)
	}
	defer br.Close()
	listen, _, _, _ := br.Status()

	term, err := NewBusTerminal(listen)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Send("ACK ACK 0x23 0x34")

	br.SetAddress(0x50)
	br.BeginTransaction()
	if !br.WriteAck(0x00) {
		t.Fatal("I2C address/data was not ACKed")
	}
	v, ok := br.ReadAck()
	if !ok || v != 0x23 {
		t.Fatalf("first read = %#x, %v; want 0x23", v, ok)
	}
	v, ok = br.ReadAck()
	if !ok || v != 0x34 {
		t.Fatalf("second read = %#x, %v; want 0x34", v, ok)
	}
	br.EndTransaction()
}
