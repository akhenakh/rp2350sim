package main

import (
	"testing"

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
