package main

import (
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/debug"
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

func TestDebugRegistersText(t *testing.T) {
	s := DebugSnapshot{
		PC: 0x10000123, SP: 0x20001000,
		APSR: 0xF0000000, IPSR: 11,
		CONTROL: 2, MSP: 0x20001000, PSP: 0x20002000,
	}
	s.Registers[0] = 0xdeadbeef
	s.Registers[15] = s.PC
	got := debugRegistersText(s)
	for _, want := range []string{
		"rp2350sim debug registers",
		"R0  deadbeef",
		"R15 10000123",
		"PC 10000123  SP 20001000",
		"APSR f0000000  IPSR 11",
		"CONTROL 2  MSP 20001000  PSP 20002000",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("register text missing %q:\n%s", want, got)
		}
	}
}

func TestDebugMemoryText(t *testing.T) {
	got := debugMemoryText(0x20000000, []byte("AB\x00hello"))
	for _, want := range []string{
		"rp2350sim debug memory",
		"Base: 0x20000000  Bytes: 8",
		"20000000  41 42 00 68 65 6c 6c 6f",
		"AB.hello",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("memory text missing %q:\n%s", want, got)
		}
	}
}

func TestDebugVariablesText(t *testing.T) {
	s := DebugSnapshot{
		Locals: []debug.Variable{
			{Name: "local", Type: "uint32", Value: "0xaaaaaaaa", Available: true},
			{Name: "missing", Available: false, Note: "optimized out"},
		},
		Globals: []debug.Variable{
			{Name: "g_magic", Type: "uint32", Value: "0x12345678", Available: true},
			{Name: "_runtime", Value: "0x1", Available: true},
		},
	}
	got := debugVariablesText(s)
	for _, want := range []string{
		"rp2350sim debug variables",
		"Locals:", "local", "uint32", "0xaaaaaaaa",
		"missing", "optimized out",
		"Globals:", "g_magic", "0x12345678",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("variable text missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "_runtime") {
		t.Fatalf("variable text leaked a runtime global:\n%s", got)
	}
}

func TestDebugVariablesTextEmpty(t *testing.T) {
	got := debugVariablesText(DebugSnapshot{})
	if n := strings.Count(got, "(none)"); n != 2 {
		t.Fatalf("empty variable sections = %d\n%s", n, got)
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
