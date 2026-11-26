package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHIDFirmwareReports drives the real TinyGo HID firmware through the
// emulated host: enumeration must fetch the report descriptor and route the
// reports to the HID monitor.
func TestHIDFirmwareReports(t *testing.T) {
	cases := []struct {
		file  string
		kinds []string
	}{
		{"hid.elf", []string{"keyboard", "mouse"}},
		{"hidgamepad.elf", []string{"gamepad"}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			fw := filepath.Join("..", "..", "build", tc.file)
			if _, err := os.Stat(fw); err != nil {
				t.Skipf("%s is not built", fw)
			}
			e := NewEmu()
			defer e.SetBoard(nil)
			e.SetBoard(&BoardDef{ID: "pico2", Name: "Pico 2"})
			if err := e.LoadPaused(fw); err != nil {
				t.Fatal(err)
			}
			e.RunFor(400_000_000) // ~2.7 s virtual: enumeration plus report cycles
			snap := e.Snapshot()
			if len(snap.HID) == 0 {
				t.Fatal("no HID interface in the snapshot")
			}
			seen := map[string]bool{}
			for _, ev := range e.HIDEvents() {
				seen[ev.Kind] = true
			}
			for _, kind := range tc.kinds {
				if !seen[kind] {
					t.Fatalf("HID report kinds = %v, want %s", seen, kind)
				}
			}
		})
	}
}
