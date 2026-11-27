package main

import (
	"strings"
	"testing"
)

func TestGroupUART(t *testing.T) {
	var events []UARTEvent
	for _, c := range []byte("abc\ndef") {
		events = append(events, UARTEvent{Bus: 1, Data: c})
	}
	lines := groupUART(events)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if got := string(lines[0].data); got != "abc\n" {
		t.Fatalf("first line = %q", got)
	}
	if got := string(lines[1].data); got != "def" {
		t.Fatalf("second line = %q", got)
	}

	long := make([]UARTEvent, 70)
	for i := range long {
		long[i] = UARTEvent{Bus: 0, Data: 'a'}
	}
	lines = groupUART(long)
	if len(lines) != 2 || len(lines[0].data) != uartLineBytes || len(lines[1].data) != 6 {
		t.Fatalf("64-byte split = %d lines (%d, %d)", len(lines), len(lines[0].data), len(lines[1].data))
	}

	lines = groupUART([]UARTEvent{
		{Bus: 1, Data: 'I'},
		{Bus: 1, In: true, Data: 'R'},
		{Bus: 1, Data: 'I'},
	})
	if len(lines) != 3 {
		t.Fatalf("direction change = %d lines, want 3", len(lines))
	}
}

// The raw ring trims its oldest bytes, so timestamps must be absolute; a
// relative base would shift every visible line when the start moves.
func TestUARTLogUsesAbsoluteTime(t *testing.T) {
	events := []UARTEvent{
		{Bus: 0, Cycle: 10 * cyclesPerMillisecond, Data: 'a'},
		{Bus: 0, Cycle: 10 * cyclesPerMillisecond, Data: '\n'},
		{Bus: 0, Cycle: 25 * cyclesPerMillisecond, Data: 'b'},
		{Bus: 0, Cycle: 25 * cyclesPerMillisecond, Data: '\n'},
	}
	lines := groupUART(events)
	if got := strings.TrimSpace(lines[1].String()); !strings.HasPrefix(got, "25.000ms") {
		t.Fatalf("second line stamp = %q, want 25.000ms", got)
	}
	// Simulate the ring trimming the first line: the visible stamp must not move.
	lines = groupUART(events[2:])
	if got := strings.TrimSpace(lines[0].String()); !strings.HasPrefix(got, "25.000ms") {
		t.Fatalf("stamp after trimming the first line = %q, want 25.000ms", got)
	}
}
