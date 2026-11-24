package main

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/midi"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func writeLoopFirmware(t *testing.T) string {
	t.Helper()
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data[0:], rp2350.SRAMBase+0x70000)
	binary.LittleEndian.PutUint32(data[4:], rp2350.FlashBase+8+1)
	binary.LittleEndian.PutUint16(data[8:], 0xe7fe) // b .
	path := filepath.Join(t.TempDir(), "loop.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReloadPreservesPauseAndFailureState(t *testing.T) {
	path := writeLoopFirmware(t)
	e := &Emu{debugger: rp2350.NewDebugger(), chip: rp2350.New()}
	if err := e.Load(path); err != nil {
		t.Fatal(err)
	}
	e.SetRunning(false)
	if err := e.Reload(path); err != nil {
		t.Fatal(err)
	}
	if e.running || !e.debugger.Stopped {
		t.Fatalf("paused reload restarted: running=%v stopped=%v", e.running, e.debugger.Stopped)
	}

	e.SetRunning(true)
	if err := e.Reload(path); err != nil {
		t.Fatal(err)
	}
	if !e.running || e.debugger.Stopped {
		t.Fatalf("running reload did not resume: running=%v stopped=%v", e.running, e.debugger.Stopped)
	}

	e.SetRunning(false)
	oldChip := e.chip
	if err := e.Reload(filepath.Join(t.TempDir(), "missing.bin")); err == nil {
		t.Fatal("missing reload succeeded")
	}
	if e.chip != oldChip || e.running {
		t.Fatal("failed reload changed the running machine")
	}
}

func TestLogicPauseStopsSynchronousRunFor(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	d := rp2350.NewDebugger()
	e := &Emu{chip: c, debugger: d, running: true}
	e.SetLogicTrigger(LogicTrigger{Kind: "uart", Pause: true})
	c.CycleHooks = append(c.CycleHooks, func() {
		e.addUART(0, c.Cycles, false, 'A')
	})

	e.RunFor(1_000_000)
	if !e.frozen.triggered || e.running {
		t.Fatalf("logic trigger did not pause: triggered=%v running=%v", e.frozen.triggered, e.running)
	}
	if c.Cycles >= 1_000_000 {
		t.Fatalf("RunFor continued after trigger: cycles=%d", c.Cycles)
	}
}

func TestBoardSwitchFlushesMIDIBeforeReset(t *testing.T) {
	old := rp2350.New()
	e := &Emu{
		chip: old, debugger: rp2350.NewDebugger(),
		board:    &BoardDef{ID: "old", MIDI: &MIDIDevice{Kind: "uart", UART: 1}},
		midiKind: "uart", midiUART: 1,
		midiPending: []midi.Message{
			{Status: 0x80, Data: [2]byte{60, 0}, Len: 3},
			{Status: 0xb0, Data: [2]byte{123, 0}, Len: 3},
		},
	}
	e.SetBoard(&BoardDef{ID: "new"})
	for i, want := range []byte{0x80, 60, 0, 0xb0, 123, 0} {
		if got := old.Read8(0x40078000); got != want {
			t.Fatalf("old UART byte %d = %#x, want %#x", i, got, want)
		}
	}
	if len(e.midiPending) != 0 || e.midiKind != "" {
		t.Fatalf("MIDI state leaked into new board: pending=%v kind=%q", e.midiPending, e.midiKind)
	}
}

func TestSendInputQueuesUSBUntilReady(t *testing.T) {
	c := rp2350.New()
	e := &Emu{chip: c, debugger: rp2350.NewDebugger()}
	e.SendInput([]byte("abc"))
	if got := string(e.usbInput); got != "abc" {
		t.Fatalf("USB queue = %q, want abc", got)
	}
	if events := e.USBEvents(); len(events) != 0 {
		t.Fatalf("unready USB endpoint recorded events: %+v", events)
	}
	for _, want := range []byte("abc") {
		if got := c.Read8(0x40070000); got != want {
			t.Fatalf("UART input = %#x, want %#x", got, want)
		}
	}
}

func TestPickerRoundTripPreservesFirmwareAndRunState(t *testing.T) {
	path := writeLoopFirmware(t)
	e := &Emu{debugger: rp2350.NewDebugger(), chip: rp2350.New()}
	if err := e.Load(path); err != nil {
		t.Fatal(err)
	}
	e.SetBoard(nil)
	e.SetBoard(&BoardDef{ID: "new"})
	if !e.running || e.debugger.Stopped {
		t.Fatalf("running picker round trip did not resume: running=%v stopped=%v", e.running, e.debugger.Stopped)
	}
	if got := e.chip.BootCount(); got != 1 {
		t.Fatalf("boot count after picker round trip = %d, want 1", got)
	}
	if got := e.chip.Flash[4]; got == 0xff {
		t.Fatal("picker round trip discarded the loaded reset vector")
	}

	e.SetRunning(false)
	e.SetBoard(nil)
	e.SetBoard(&BoardDef{ID: "new"})
	if e.running || !e.debugger.Stopped {
		t.Fatalf("paused picker round trip lost pause: running=%v stopped=%v", e.running, e.debugger.Stopped)
	}
}

func TestSetBoardNilStopsFirmwareAndClosesBridge(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	e := &Emu{
		debugger: rp2350.NewDebugger(), running: true,
		board: &BoardDef{ID: "serial", Serial: []SerialDevice{{UART: 1, Listen: addr}}},
	}
	e.resetChip()
	if len(e.bridges) != 1 {
		t.Fatalf("bridges = %d, want 1", len(e.bridges))
	}
	e.SetBoard(nil)
	if e.running || e.board != nil || len(e.bridges) != 0 {
		t.Fatalf("picker state not idle: running=%v board=%v bridges=%d", e.running, e.board, len(e.bridges))
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("bridge listener survived board exit: %v", err)
	}
	_ = rebound.Close()
}
