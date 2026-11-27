package main

import "testing"

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
