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

// webYield hands the CPU to other goroutines on a desktop build.
func webYield() { runtime.Gosched() }

// webChunk is the emulator step size on a desktop build: 1 ms of virtual time.
func webChunk() uint64 { return 150_000 }
