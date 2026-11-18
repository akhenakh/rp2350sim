// Verifies the emulated PIO through the public tinygo-org/pio API.
package main

import (
	"machine"
	"time"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

var asm = pio.AssemblerV0{}

var (
	uart  = machine.UART0
	fails int
)

func report(name string, got, want uint32) {
	if got == want {
		uart.Write([]byte("pio " + name + ": ok\r\n"))
		return
	}
	fails++
	uart.Write([]byte("pio " + name + ": FAIL got="))
	writeHex(got)
	uart.Write([]byte(" want="))
	writeHex(want)
	uart.Write([]byte("\r\n"))
}

func writeHex(v uint32) {
	const digits = "0123456789abcdef"
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = digits[v&0xF]
		v >>= 4
	}
	uart.Write(b[:])
}

func addProgram(p *pio.PIO, prog []uint16) uint8 {
	off, err := p.AddProgram(prog, -1)
	if err != nil {
		uart.Write([]byte("add program failed\r\n"))
		fails++
	}
	return off
}

func waitRx(sm pio.StateMachine) (uint32, bool) {
	for i := 0; i < 200000; i++ {
		if !sm.IsRxFIFOEmpty() {
			return sm.RxGet(), true
		}
	}
	return 0, false
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.UART0_TX_PIN})
	p := pio.PIO0

	// --- 1. pull -> OSR -> X -> ISR -> push -------------------------------
	sm0, err := p.ClaimStateMachine()
	if err != nil {
		uart.Write([]byte("claim failed\r\n"))
		return
	}
	loopback := []uint16{
		asm.Pull(true, true).Encode(),     // pull ifempty block
		asm.Out(pio.OutDestX, 8).Encode(), // out x, 8
		asm.Mov(pio.MovDestISR, pio.MovSrcX).Encode(),
		asm.Push(false, true).Encode(), // push block
	}
	off0 := addProgram(p, loopback)
	cfg0 := asm.DefaultStateMachineConfig(off0, loopback)
	cfg0.SetOutShift(true, false, 0) // shift right, no autopull
	cfg0.SetFIFOJoin(pio.FifoJoinNone)
	cfg0.SetClkDivIntFrac(20, 0)
	sm0.Init(off0, cfg0)
	sm0.SetEnabled(true)
	sm0.TxPut(0x12345678)
	if v, ok := waitRx(sm0); ok {
		report("tx/rx loopback", v, 0x78)
	} else {
		report("tx/rx loopback", 0xDEADBEEF, 0x78)
	}
	sm0.SetEnabled(false)

	// --- 2. autopush at a 32-bit threshold -------------------------------
	sm1 := p.StateMachine(1)
	sm1.TryClaim()
	autopush := []uint16{
		asm.In(pio.InSrcX, 32).Encode(),
	}
	off1 := addProgram(p, autopush)
	cfg1 := asm.DefaultStateMachineConfig(off1, autopush)
	cfg1.SetInShift(true, true, 32) // shift right, autopush, threshold 32
	cfg1.SetClkDivIntFrac(20, 0)
	sm1.Init(off1, cfg1)
	sm1.SetX(0xA1B2C3D4)
	sm1.SetEnabled(true)
	if v, ok := waitRx(sm1); ok {
		report("autopush", v, 0xA1B2C3D4)
	} else {
		report("autopush", 0xDEADBEEF, 0xA1B2C3D4)
	}
	sm1.SetEnabled(false)
	sm1.Unclaim()

	// --- 3. autopull at an 8-bit threshold -------------------------------
	sm2 := p.StateMachine(2)
	sm2.TryClaim()
	autopull := []uint16{
		asm.Out(pio.OutDestY, 8).Encode(),
		asm.Mov(pio.MovDestISR, pio.MovSrcY).Encode(),
		asm.Push(false, true).Encode(),
	}
	off2 := addProgram(p, autopull)
	cfg2 := asm.DefaultStateMachineConfig(off2, autopull)
	cfg2.SetOutShift(true, true, 8) // shift right, autopull, threshold 8
	cfg2.SetClkDivIntFrac(20, 0)
	sm2.Init(off2, cfg2)
	sm2.SetEnabled(true)
	sm2.TxPut(0x11223344)
	sm2.TxPut(0x55667788)
	if v, ok := waitRx(sm2); ok {
		report("autopull 1", v, 0x44)
	} else {
		report("autopull 1", 0xDEADBEEF, 0x44)
	}
	if v, ok := waitRx(sm2); ok {
		report("autopull 2", v, 0x88)
	} else {
		report("autopull 2", 0xDEADBEEF, 0x88)
	}
	sm2.SetEnabled(false)
	sm2.Unclaim()

	// --- 4. IRQ handshake between two state machines ---------------------
	waiter := p.StateMachine(0) // reuse SM0 after disabling
	waiter.ClearFIFOs()
	waitProg := []uint16{
		asm.WaitIRQ(true, false, 0).Encode(), // wait 1 irq 0
		asm.Mov(pio.MovDestISR, pio.MovSrcX).Encode(),
		asm.Push(false, true).Encode(),
	}
	waitOff := addProgram(p, waitProg)
	waitCfg := asm.DefaultStateMachineConfig(waitOff, waitProg)
	waitCfg.SetClkDivIntFrac(20, 0)
	waiter.Init(waitOff, waitCfg)
	waiter.SetX(0x99)
	waiter.Exec(asm.In(pio.InSrcX, 32).Encode())
	waiter.Exec(asm.Push(false, false).Encode())
	if v, ok := waitRx(waiter); ok {
		report("setx-in", v, 0x99)
	} else {
		report("setx-in", 0xDEADBEEF, 0x99)
	}
	waiter.Exec(asm.Mov(pio.MovDestISR, pio.MovSrcX).Encode())
	waiter.Exec(asm.Push(false, false).Encode())
	if v, ok := waitRx(waiter); ok {
		report("setx-mov", v, 0x99)
	} else {
		report("setx-mov", 0xDEADBEEF, 0x99)
	}
	waiter.ClearFIFOs()

	setter := p.StateMachine(3)
	setter.TryClaim()
	setProg := []uint16{asm.IRQSet(false, 0).Encode()}
	setOff := addProgram(p, setProg)
	setCfg := asm.DefaultStateMachineConfig(setOff, setProg)
	setCfg.SetClkDivIntFrac(20, 0)
	setter.Init(setOff, setCfg)

	waiter.SetEnabled(true)
	setter.SetEnabled(true)
	if v, ok := waitRx(waiter); ok {
		report("irq handshake", v, 0x99)
	} else {
		report("irq handshake", 0xDEADBEEF, 0x99)
	}
	waiter.SetEnabled(false)
	setter.SetEnabled(false)
	setter.Unclaim()

	// --- 5. side-set square wave on GP17 --------------------------------
	sgate := p.StateMachine(1)
	sgate.ClearFIFOs()
	sasm := pio.AssemblerV0{SidesetBits: 1}
	sideProg := []uint16{
		sasm.Nop().Side(0).Delay(1).Encode(),
		sasm.Nop().Side(1).Delay(1).Encode(),
	}
	sideOff := addProgram(p, sideProg)
	machine.GPIO17.Configure(machine.PinConfig{Mode: p.PinMode()})
	sideCfg := sasm.DefaultStateMachineConfig(sideOff, sideProg)
	sideCfg.SetSidesetParams(1, false, false)
	sideCfg.SetSidesetPins(machine.GPIO17)
	sideCfg.SetClkDivIntFrac(1000, 0)
	sgate.Init(sideOff, sideCfg)
	sgate.SetPindirsConsecutive(machine.GPIO17, 1, true)
	sgate.SetEnabled(true)
	time.Sleep(2 * time.Millisecond)
	sgate.SetEnabled(false)

	if fails == 0 {
		uart.Write([]byte("pio: all checks passed\r\n"))
	} else {
		uart.Write([]byte("pio: failures\r\n"))
	}
	for {
		time.Sleep(time.Second)
	}
}
