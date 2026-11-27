package main

import (
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestTraceLinesGroupsITMBytes(t *testing.T) {
	var events []rp2350.TraceEvent
	for _, b := range []byte("ab\n") {
		events = append(events, rp2350.TraceEvent{Cycle: 10, Port: 0, Kind: "itm", Text: string([]byte{b})})
	}
	events = append(events, rp2350.TraceEvent{Cycle: 20, Port: 1, Kind: "itm", Text: "c"})

	lines := traceLines(events)
	if len(lines) != 2 {
		t.Fatalf("lines = %+v, want 2", lines)
	}
	if lines[0].text != "ab" || lines[1].text != "c" {
		t.Fatalf("texts = %q, %q", lines[0].text, lines[1].text)
	}
}

func TestCANMode(t *testing.T) {
	for stat, want := range map[byte]string{
		0x00: "normal", 0x20: "sleep", 0x40: "loopback",
		0x60: "listen-only", 0x80: "configuration",
	} {
		if got := canMode(stat); got != want {
			t.Errorf("canMode(%#x) = %q, want %q", stat, got, want)
		}
	}
}

func TestTraceLinesPartialAndCap(t *testing.T) {
	events := []rp2350.TraceEvent{{Cycle: 1, Port: 0, Kind: "itm", Text: "partial"}}
	lines := traceLines(events)
	if len(lines) != 1 || lines[0].text != "partial" {
		t.Fatalf("partial lines = %+v", lines)
	}
	var long []byte
	for range maxTraceLine + 10 {
		long = append(long, 'x')
	}
	events = []rp2350.TraceEvent{{Cycle: 2, Port: 0, Kind: "itm", Text: string(long)}}
	lines = traceLines(events)
	if len(lines) != 1 || len(lines[0].text) != maxTraceLine {
		t.Fatalf("capped line length = %d", len(lines))
	}
}

func TestTraceText(t *testing.T) {
	events := []rp2350.TraceEvent{
		{Cycle: 150_000, Port: 0, Kind: "itm", Text: "hello\n"},
		{Cycle: 300_000, Port: 0, Kind: "pc", PC: 0x10000123},
	}
	got := traceText(events)
	for _, want := range []string{"rp2350sim trace", "Events: 2", "1.00 ms", "port 0", "hello", "pc=10000123"} {
		if !strings.Contains(got, want) {
			t.Fatalf("trace text missing %q:\n%s", want, got)
		}
	}
}

func TestCANText(t *testing.T) {
	info := devices.MCP2515Info{CANSTAT: 0, CANCTRL: 0, CANINTF: 0x05, EFLG: 0}
	frames := []devices.CANFrame{
		{Time: 150_000, ID: 0x123, Data: []byte{0x41}},
		{Time: 300_000, ID: 0x1ABCDE, Ext: true, RTR: true, Data: []byte{0x41, 0x42}},
	}
	got := canText(info, frames)
	for _, want := range []string{"rp2350sim CAN", "Frames: 2", "EXT/RTR", "1ABCDE", "41 42", "2.00"} {
		if !strings.Contains(got, want) {
			t.Fatalf("CAN text missing %q:\n%s", want, got)
		}
	}
}
