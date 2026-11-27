//go:build !js

package main

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// webBoot reports the browser demo configuration; a desktop build takes the
// board and firmware from the command line instead.
func webBoot() (boardID, firmware, tab string, ok bool) { return "", "", "", false }

// pluginSource loads a device plugin on the desktop. The URL form is for the
// browser; a board entry must provide a file there.
func pluginSource(file, url string) ([]byte, error) {
	if file == "" {
		return nil, fmt.Errorf("plugin %q has no file path", url)
	}
	return os.ReadFile(file)
}

// webPrepareBoard leaves a desktop board definition untouched.
func webPrepareBoard(*BoardDef) {}

// webFetch is only reachable from the browser boot path.
func webFetch(string) ([]byte, error) {
	return nil, fmt.Errorf("webFetch is not available on this platform")
}

// webHostTools is true on a desktop build: GDB can bind a port.
func webHostTools() bool { return true }

// webOpenFirmware lets the desktop open the Shirei file browser.
func webOpenFirmware() bool { return false }

// webReloadFirmware lets the desktop re-read the path from disk.
func webReloadFirmware() bool { return false }

// webTurboDefault keeps real-time pacing on desktop.
func webTurboDefault() bool { return false }

// webFileOutput lets the desktop write traces, WAVs and PNGs.
func webFileOutput() bool { return true }

// webYield hands the CPU to other goroutines on a desktop build.
func webYield() { runtime.Gosched() }

// webChunk is the emulator step size on a desktop build: 1 ms of virtual time.
func webChunk() uint64 { return 150_000 }

// webFrameInterval keeps the desktop UI at display rate; it runs on its own
// thread, so a slow tab does not starve the emulator.
func webFrameInterval(int) time.Duration { return 16 * time.Millisecond }

// webSetFrameInterval is a no-op on desktop.
func webSetFrameInterval(time.Duration) {}
