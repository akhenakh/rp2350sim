// itm writes a string through the Cortex-M33 ITM stimulus port without using
// UART or USB. Run it with the emulator's -itm flag to see the bytes:
//
//	rp2350sim -fw build/itm.elf -seconds 1 -itm
package main

import (
	"runtime/volatile"
	"time"
	"unsafe"
)

const (
	demcr   = 0xE000EDFC // CoreDebug->DEMCR
	itmBase = 0xE0000000 // ITM->PORT[0] (stimulus ports, 4 bytes each)
	itmTER  = 0xE0000E00 // Trace Enable Register
	itmTCR  = 0xE0000E80 // Trace Control Register
	itmLAR  = 0xE0000FB0 // Lock Access Register
)

func reg(addr uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(addr))
}

func itmInit() {
	reg(demcr).Set(reg(demcr).Get() | (1 << 24)) // TRCENA
	reg(itmLAR).Set(0xC5ACCE55)                  // unlock
	reg(itmTCR).Set(reg(itmTCR).Get() | 1)       // ITMENA
	reg(itmTER).Set(reg(itmTER).Get() | 1)       // enable port 0
}

func itmSendByte(port uint, b byte) {
	p := reg(itmBase + uintptr(port)*4)
	// Stimulus port reads nonzero when FIFO is ready
	for p.Get() == 0 {
	}
	(*volatile.Register8)(unsafe.Pointer(itmBase + uintptr(port)*4)).Set(b)
}

func itmPrint(s string) {
	for i := 0; i < len(s); i++ {
		itmSendByte(0, s[i])
	}
}

func main() {
	itmInit()
	itmPrint("hello from TinyGo\n")
	for {
		time.Sleep(time.Second)
	}
}
