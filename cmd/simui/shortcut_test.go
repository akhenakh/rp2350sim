package main

import (
	"runtime"
	"testing"

	. "go.hasen.dev/shirei"
)

// TestQuitShortcut checks the platform quit chord and that it needs the exact
// primary modifier: Cmd+Q on macOS, Ctrl+Q elsewhere.
func TestQuitShortcut(t *testing.T) {
	host := GetHost()
	defer func() {
		host.FrameInput.Key = KeyCodeNone
		host.Input.Modifiers = 0
	}()

	if runtime.GOOS == "darwin" {
		if PrimaryMod() != ModCmd {
			t.Fatalf("PrimaryMod = %#x, want ModCmd", PrimaryMod())
		}
	} else if PrimaryMod() != ModCtrl {
		t.Fatalf("PrimaryMod = %#x, want ModCtrl", PrimaryMod())
	}

	host.FrameInput.Key = KeyQ
	host.Input.Modifiers = PrimaryMod()
	if !quitRequested() {
		t.Fatal("quit chord not detected")
	}

	host.Input.Modifiers = 0
	if quitRequested() {
		t.Fatal("plain Q must not quit")
	}

	host.Input.Modifiers = PrimaryMod() | ModShift
	if quitRequested() {
		t.Fatal("extra modifiers must not quit")
	}

	host.FrameInput.Key = KeyW
	host.Input.Modifiers = PrimaryMod()
	if quitRequested() {
		t.Fatal("primary+W must not quit")
	}
}
