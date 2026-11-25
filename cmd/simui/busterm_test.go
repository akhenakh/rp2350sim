package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
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

func TestBusTerminalsDriveBusmaster(t *testing.T) {
	fw := filepath.Join("..", "..", "build", "busmaster.elf")
	if _, err := os.Stat(fw); err != nil {
		t.Skip("build/busmaster.elf is not built")
	}
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var board *BoardDef
	for i := range boards {
		if boards[i].ID == "pico2-bus" {
			board = &boards[i]
		}
	}
	if board == nil {
		t.Fatal("pico2-bus board is missing")
	}

	e := NewEmu()
	e.SetBoard(board)

	// Connect the bus terminals and queue their answers before the firmware
	// runs: the busmaster example reads both buses immediately at boot, and a
	// faster emulator loop would otherwise complete the first transfer before
	// the answers exist.
	infos := e.BusBridges()
	if len(infos) != 2 {
		t.Fatalf("bridges = %+v, want I2C and SPI", infos)
	}
	var terms []*BusTerminal
	defer func() {
		for _, term := range terms {
			term.Close()
		}
	}()
	for _, info := range infos {
		term, err := NewBusTerminal(info.Listen)
		if err != nil {
			t.Fatal(err)
		}
		terms = append(terms, term)
		switch info.Kind {
		case "I2C":
			term.Send("ACK ACK 0x12 0x34")
		case "SPI":
			term.Send("0xEF 0x40 0x18")
		}
	}

	if err := e.Load(fw); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		snap := e.Snapshot()
		if strings.Contains(snap.Console, "i2c: 12 34") &&
			strings.Contains(snap.Console, "spi: ef 40 18") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("busmaster did not see both devices; console:\n%s", snap.Console)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBusAnswerFlags(t *testing.T) {
	f := busAnswerFlags{}
	if err := f.Set("i2c=ACK ACK 0x12 0x34"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := f["i2c"]; got != "ACK ACK 0x12 0x34" {
		t.Fatalf("i2c answer = %q", got)
	}
	if err := f.Set("spi=0xEF 0x40 0x18"); err != nil {
		t.Fatalf("Set spi: %v", err)
	}
	for _, bad := range []string{"", "i2c", "uart=ACK", "i2c="} {
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) succeeded, want error", bad)
		}
	}
}
