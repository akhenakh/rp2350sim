//go:build !js

package main

import (
	"fmt"
	"runtime"
)

// webBoot reports the browser demo configuration; a desktop build takes the
// board and firmware from the command line instead.
func webBoot() (boardID, firmware, tab string, ok bool) { return "", "", "", false }

// webPrepareBoard leaves a desktop board definition untouched.
func webPrepareBoard(*BoardDef) {}

// webFetch is only reachable from the browser boot path.
func webFetch(string) ([]byte, error) {
	return nil, fmt.Errorf("webFetch is not available on this platform")
}

// webHostTools is true on a desktop build: GDB can bind a port and the Build
// drawer can run a command.
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
