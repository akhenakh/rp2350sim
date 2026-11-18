// Package rp2350 models the Raspberry Pi RP2350 microcontroller.
package rp2350

import (
	"fmt"
	"io"
	"sort"

	"github.com/akh/rp2350sim/internal/m33"
)

// Memory map.
const (
	ROMBase   = 0x00000000
	ROMSize   = 32 * 1024
	FlashBase = 0x10000000
	FlashSize = 4 * 1024 * 1024
	SRAMBase  = 0x20000000
	SRAMSize  = 520 * 1024
	SIOBase   = 0xD0000000
	PPBBase   = 0xE0000000
	// SCSBase is the start of the System Control Space registers (NVIC, SCB,
	// SysTick). PPBBase also covers the ITM and DWT, which are not modelled.
	SCSBase    = 0xE000E000
	PeriphBase = 0x40000000
)

const (
	sysClkHz = 150_000_000
	// CPU is modelled at one instruction per cycle.
	cyclesPerMicrosecond = sysClkHz / 1_000_000
)

// Chip is one RP2350 device: two M33 cores, memory and peripherals.
type Chip struct {
	Flash []byte
	SRAM  []byte
	ROM   []byte

	CPU0 *m33.CPU
	CPU1 *m33.CPU

	NVIC *NVIC

	// Cycles is the master clock counter, in CPU cycles at 150 MHz.
	Cycles uint64
	// core is the CPU currently being stepped (0 or 1).
	core int
	// Paused advances time without executing instructions.
	Paused bool
	// Halted reports that a core stopped on an unexpected condition.
	Halted bool

	// Pin state and per-pin external drive for the board model.
	Pins *PinState

	io  ioBank
	sio sioBlock
	tmr [2]timerBlock
	usb *usbBlock
	pwm *pwmBlock
	adc *adcBlock

	regs   []*regBlock
	byBase map[uint32]*regBlock

	// OnDMA reports DMA transfers (diagnostics).
	OnDMA func(ch int, readAddr, writeAddr, count, ctrl uint32)

	// OnSPI0 and OnSPI1 are called for every byte clocked out; they return the
	// byte clocked in on MISO.
	OnSPI0 func(tx byte) byte
	OnSPI1 func(tx byte) byte

	// OnUART0 and OnUART1 receive console bytes written by firmware.
	OnUART0 func(b byte)
	OnUART1 func(b byte)

	// USBLog, when non-nil, receives USB host-state diagnostics.
	USBLog io.Writer

	// IRQCount counts delivered exceptions (diagnostics).
	IRQCount uint64

	// Unhandled records accesses to unmapped peripheral addresses.
	Unhandled map[uint32]uint32
	// OnUnhandled is called for each unmapped access (may be nil).
	OnUnhandled func(addr uint32, write bool, val uint32)

	// MMIO trace hook.
	Trace func(addr uint32, write bool, val uint32, pc uint32)
	// OnWrite is called for every memory write when set (debugging).
	OnWrite func(addr uint32, val uint32, size int)
}

// New creates a chip with zeroed memories.
func New() *Chip {
	c := &Chip{
		Flash:     make([]byte, FlashSize),
		SRAM:      make([]byte, SRAMSize),
		ROM:       make([]byte, ROMSize),
		Unhandled: map[uint32]uint32{},
		byBase:    map[uint32]*regBlock{},
	}
	c.Pins = NewPinState()
	c.io.init(c)
	c.sio.init(c)
	for i := range c.tmr {
		c.tmr[i].init(c, i)
	}

	c.NVIC = &NVIC{chip: c}
	c.CPU0 = m33.New(c)
	c.CPU0.Interrupts = c.NVIC
	c.CPU1 = m33.New(c)
	c.CPU1.Interrupts = c.NVIC

	c.addRegBlock(newResets(c))
	c.addRegBlock(newClocks(c))
	c.addRegBlock(newXOSC(c))
	c.addRegBlock(newPLL(c, 0x40050000))
	c.addRegBlock(newPLL(c, 0x40058000))
	c.addRegBlock(newTicks(c))
	c.addRegBlock(newWatchdog(c))
	c.addRegBlock(newSysInfo(c))
	c.addRegBlock(newROSC(c))
	c.addRegBlock(c.io.bank0)
	c.addRegBlock(c.io.pads)
	c.addRegBlock(c.tmr[0].block)
	c.addRegBlock(c.tmr[1].block)
	c.AddUART(0x40070000, 0)
	c.AddUART(0x40078000, 1)
	c.AddSPI(0x40080000, 0)
	c.AddSPI(0x40088000, 1)
	c.addRegBlock(newDMA(c).block)
	c.usb = newUSB(c)
	c.addRegBlock(c.usb.block)
	c.pwm = newPWM(c)
	c.addRegBlock(c.pwm.block)
	c.adc = newADC(c)
	c.addRegBlock(c.adc.block)
	return c
}

func (c *Chip) addRegBlock(b *regBlock) {
	c.regs = append(c.regs, b)
	c.byBase[b.base] = b
	sort.Slice(c.regs, func(i, j int) bool { return c.regs[i].base < c.regs[j].base })
}

// Run executes n cycles across both cores.
func (c *Chip) Run(n uint64) {
	for i := uint64(0); i < n && !c.Halted; i++ {
		c.Step()
	}
}

// Step advances the machine by one cycle.
func (c *Chip) Step() {
	if c.Halted {
		return
	}
	c.Cycles++

	c.tickPeripherals()
	if c.NVIC.PeekPending() != 0 {
		c.IRQCount++
	}

	if !c.Paused {
		c.stepCore(c.CPU0)
		c.stepCore(c.CPU1)
	} else {
		// Paused: still advance time so timers behave.
		c.advanceSleep(1)
	}

	if c.CPU0.Halted {
		c.Halted = true
	}
	if c.CPU1.Halted {
		c.Halted = true
	}
}

func (c *Chip) stepCore(cpu *m33.CPU) {
	if cpu.Sleeping || cpu.Parked {
		return
	}
	if cpu == c.CPU0 {
		c.core = 0
	} else {
		c.core = 1
	}
	cpu.Bus = c
	cpu.Step()
}

// advanceSleep skips time forward when all cores are asleep.
func (c *Chip) advanceSleep(max uint64) {
	c.Cycles += max
	c.tickPeripherals()
}

func (c *Chip) tickPeripherals() {
	now := c.Cycles / cyclesPerMicrosecond
	for i := range c.tmr {
		c.tmr[i].tick(now)
	}
	if c.usb != nil {
		c.usb.tick(c)
	}
	if c.pwm != nil {
		c.pwm.tick(now)
	}
}

// NVIC delivers interrupts to the cores.

// Reset performs a chip reset and starts both cores from flash.
func (c *Chip) Reset() {
	c.Cycles = 0
	c.Halted = false
	c.core = 0
	c.CPU0.Reset()
	c.CPU1.Reset()
	c.CPU0.Parked = false
	c.CPU1.Parked = false
	c.NVIC.reset()
	c.Pins.reset()
	for i := range c.tmr {
		c.tmr[i].reset()
	}
	c.io.reset()
	c.sio.reset()
}

// Boot vectors core 0 at the image in flash.
func (c *Chip) Boot() {
	c.Reset()
	c.CPU0.SetVectorTable(FlashBase)
	c.CPU1.Parked = true
}

// LoadImage copies a flat binary into flash at offset 0.
func (c *Chip) LoadImage(data []byte) {
	copy(c.Flash, data)
}

// LoadSegment copies image bytes to a physical address, routing flash, SRAM
// and ROM ranges appropriately.
func (c *Chip) LoadSegment(addr uint32, data []byte) {
	switch {
	case addr >= FlashBase && addr < FlashBase+FlashSize:
		copy(c.Flash[addr-FlashBase:], data)
	case addr >= SRAMBase && addr < SRAMBase+SRAMSize:
		copy(c.SRAM[addr-SRAMBase:], data)
	case addr < ROMSize:
		copy(c.ROM[addr:], data)
	default:
		for i, b := range data {
			c.Write8(addr+uint32(i), b)
		}
	}
}

// Bus interface implementation.

func (c *Chip) Read8(addr uint32) uint8 {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		return c.SRAM[addr-SRAMBase]
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize {
		return c.Flash[addr-FlashBase]
	}
	if addr < ROMSize {
		return c.ROM[addr]
	}
	if addr >= PeriphBase {
		// Peripherals are word registers; byte and halfword accesses select
		// within the containing word (the DPRAM is byte-addressed this way).
		v := c.readMMIO(addr &^ 3)
		return uint8(v >> ((addr & 3) * 8))
	}
	return c.unhandledRead8(addr)
}

func (c *Chip) Read16(addr uint32) uint16 {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-1 {
		o := addr - SRAMBase
		return uint16(c.SRAM[o]) | uint16(c.SRAM[o+1])<<8
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize-1 {
		o := addr - FlashBase
		return uint16(c.Flash[o]) | uint16(c.Flash[o+1])<<8
	}
	if addr < ROMSize-1 {
		return uint16(c.ROM[addr]) | uint16(c.ROM[addr+1])<<8
	}
	if addr >= PeriphBase {
		v := c.readMMIO(addr &^ 3)
		if addr&2 != 0 {
			return uint16(v >> 16)
		}
		return uint16(v)
	}
	return c.unhandledRead16(addr)
}

func (c *Chip) Read32(addr uint32) uint32 {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-3 {
		o := addr - SRAMBase
		return uint32(c.SRAM[o]) | uint32(c.SRAM[o+1])<<8 | uint32(c.SRAM[o+2])<<16 | uint32(c.SRAM[o+3])<<24
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize-3 {
		o := addr - FlashBase
		return uint32(c.Flash[o]) | uint32(c.Flash[o+1])<<8 | uint32(c.Flash[o+2])<<16 | uint32(c.Flash[o+3])<<24
	}
	if addr < ROMSize-3 {
		o := addr
		return uint32(c.ROM[o]) | uint32(c.ROM[o+1])<<8 | uint32(c.ROM[o+2])<<16 | uint32(c.ROM[o+3])<<24
	}
	if addr >= PeriphBase {
		return c.readMMIO(addr)
	}
	return c.unhandledRead32(addr)
}

func (c *Chip) Write8(addr uint32, v uint8) {
	if c.OnWrite != nil {
		c.OnWrite(addr, uint32(v), 1)
	}
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		c.SRAM[addr-SRAMBase] = v
		return
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize {
		// Flash writes are ignored: XIP is read-only in this model.
		return
	}
	if addr >= PeriphBase {
		c.writeMMIO8(addr, v)
		return
	}
	c.unhandledWrite(addr, uint32(v))
}

func (c *Chip) Write16(addr uint32, v uint16) {
	if c.OnWrite != nil {
		c.OnWrite(addr, uint32(v), 2)
	}
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-1 {
		o := addr - SRAMBase
		c.SRAM[o] = byte(v)
		c.SRAM[o+1] = byte(v >> 8)
		return
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize-1 {
		return
	}
	if addr >= PeriphBase {
		c.writeMMIO16(addr, v)
		return
	}
	c.unhandledWrite(addr, uint32(v))
}

func (c *Chip) Write32(addr uint32, v uint32) {
	if c.OnWrite != nil {
		c.OnWrite(addr, v, 4)
	}
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-3 {
		o := addr - SRAMBase
		c.SRAM[o] = byte(v)
		c.SRAM[o+1] = byte(v >> 8)
		c.SRAM[o+2] = byte(v >> 16)
		c.SRAM[o+3] = byte(v >> 24)
		return
	}
	if addr >= FlashBase && addr < FlashBase+FlashSize-3 {
		return
	}
	if addr >= PeriphBase {
		c.writeMMIO(addr, v)
		return
	}
	c.unhandledWrite(addr, v)
}

func (c *Chip) unhandledRead32(addr uint32) uint32 {
	c.Unhandled[addr]++
	if c.OnUnhandled != nil {
		c.OnUnhandled(addr, false, 0)
	}
	return 0
}

func (c *Chip) unhandledRead8(addr uint32) uint8 {
	return uint8(c.unhandledRead32(addr))
}

func (c *Chip) unhandledRead16(addr uint32) uint16 {
	return uint16(c.unhandledRead32(addr))
}

func (c *Chip) unhandledWrite(addr, v uint32) {
	c.Unhandled[addr]++
	if c.OnUnhandled != nil {
		c.OnUnhandled(addr, true, v)
	}
}

// readMMIO dispatches a 32-bit peripheral read.
func (c *Chip) readMMIO(addr uint32) uint32 {
	if addr >= SIOBase && addr < SIOBase+0x1000 {
		return c.sio.read(c, addr-SIOBase)
	}
	if addr >= SCSBase && addr < SCSBase+0x1000 {
		return c.NVIC.read(addr - SCSBase)
	}
	for _, b := range c.regs {
		if addr >= b.base && addr < b.base+b.size {
			return b.read(c, addr-b.base)
		}
	}
	return c.unhandledRead32(addr)
}

func (c *Chip) writeMMIO(addr, v uint32) {
	if c.Trace != nil {
		pc := c.CPU0.R[15]
		c.Trace(addr, true, v, pc)
	}
	if addr >= SIOBase && addr < SIOBase+0x1000 {
		c.sio.write(c, addr-SIOBase, v)
		return
	}
	if addr >= SCSBase && addr < SCSBase+0x1000 {
		c.NVIC.write(addr-SCSBase, v)
		return
	}
	for _, b := range c.regs {
		if addr >= b.base && addr < b.base+b.size {
			b.write(c, addr-b.base, v)
			return
		}
	}
	c.unhandledWrite(addr, v)
}

func (c *Chip) writeMMIO8(addr uint32, v uint8) {
	for _, b := range c.regs {
		if addr >= b.base && addr < b.base+b.size {
			off := (addr - b.base) &^ 3
			shift := (addr & 3) * 8
			old := b.read(c, off)
			b.write(c, off, old&^(0xFF<<shift)|uint32(v)<<shift)
			return
		}
	}
	c.unhandledWrite(addr, uint32(v))
}

func (c *Chip) writeMMIO16(addr uint32, v uint16) {
	for _, b := range c.regs {
		if addr >= b.base && addr < b.base+b.size {
			off := (addr - b.base) &^ 3
			shift := (addr & 2) * 8
			old := b.read(c, off)
			b.write(c, off, old&^(0xFFFF<<shift)|uint32(v)<<shift)
			return
		}
	}
	c.unhandledWrite(addr, uint32(v))
}

// DescribeUnhandled returns a sorted summary of unmapped accesses.
func (c *Chip) DescribeUnhandled() string {
	type kv struct {
		addr uint32
		n    uint32
	}
	list := make([]kv, 0, len(c.Unhandled))
	for a, n := range c.Unhandled {
		list = append(list, kv{a, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].addr < list[j].addr })
	s := ""
	for _, e := range list {
		s += fmt.Sprintf("%#08x x%d\n", e.addr, e.n)
	}
	return s
}

// SIOPushes reports the number of SIO FIFO words written (diagnostics).
func (c *Chip) SIOPushes() uint64 { return c.sio.Pushes }

// Core1Launches reports how many times core 1 was released (diagnostics).
func (c *Chip) Core1Launches() uint64 { return c.sio.Launches }

// SetUSBCDC installs the callback that receives console bytes written by
// firmware to the USB CDC data endpoint.
func (c *Chip) SetUSBCDC(fn func([]byte)) { c.usb.OnCDC = fn }

// SendUSBCDC delivers data from the host to the device's CDC OUT endpoint.
func (c *Chip) SendUSBCDC(data []byte) { c.usb.SendCDC(data) }

// NowMicros returns the current virtual time in microseconds.
func (c *Chip) NowMicros() uint64 { return c.Cycles / cyclesPerMicrosecond }
