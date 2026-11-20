// debugvars is the TinyGo counterpart of firmware/c/debug: deterministic
// globals and a noinline function with a known local for debugger tests.
package main

import "time"

var Magic uint32 = 0x12345678
var Wide uint64 = 0x1122334455667788
var Negative int32 = -1234
var MagicPtr = &Magic
var WidePtr = &Wide
var NegativePtr = &Negative

//go:noinline
func marker(arg uint32) uint32 {
	local := arg ^ 0x0f0f0f0f
	for {
		if local == 0xaaaaaaaa {
			time.Sleep(time.Second)
		} else {
			time.Sleep(time.Second)
		}
	}
}

func main() {
	// Keep the globals and their DWARF entries live for the debugger test.
	// Keep the globals and their DWARF entries live for the debugger test.
	println(*MagicPtr, *WidePtr, *NegativePtr)
	marker(*MagicPtr)
}
