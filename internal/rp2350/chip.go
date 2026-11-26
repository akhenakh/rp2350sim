// Package rp2350 models the Raspberry Pi RP2350 microcontroller.
package rp2350

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	hidpkg "github.com/akhenakh/rp2350sim/internal/hid"
	"github.com/akhenakh/rp2350sim/internal/m33"
)

// Memory map.
const (
	ROMBase   = 0x00000000
	ROMSize   = 32 * 1024
	FlashBase = 0x10000000
	FlashSize = 16 * 1024 * 1024
	// XIP device windows. Each 64 MiB region holds a flash view followed by a
	// PSRAM view (the RP2350 no longer has the RP2040 cache-as-SRAM window at
	// 0x15000000).
	XIPNoCacheBase     = 0x14000000
	XIPMaintenanceBase = 0x18000000
	XIPNoTranslateBase = 0x1c000000
	XIPDeviceWindow    = 0x01000000
	SRAMBase           = 0x20000000
	SRAMSize           = 520 * 1024
	SIOBase            = 0xD0000000
	PPBBase            = 0xE0000000
	// SCSBase is the start of the System Control Space registers (NVIC, SCB,
	// SysTick). PPBBase also covers the ITM and DWT, which are not modelled.
	SCSBase    = 0xE000E000
	PeriphBase = 0x40000000
)

const (
	sysClkHz = 150_000_000
	// SysClkHz is the exported system clock used by host-side devices.
	SysClkHz = sysClkHz
	// CPU is modelled at one instruction per cycle.
	cyclesPerMicrosecond = sysClkHz / 1_000_000
	cyclesPerMillisecond = sysClkHz / 1_000
	// CyclesPerMicrosecond lets host-side devices schedule periodic hooks on
	// a sub-microsecond polling grid.
	CyclesPerMicrosecond = cyclesPerMicrosecond
)

// Chip is one RP2350 device: two M33 cores, memory and peripherals.
// HangInfo describes a detected tight poll loop.
type HangInfo struct {
	Core       int
	PC         uint32
	Cycles     uint64
	LoopCycles uint64
	MMIOAddr   uint32
	MMIOVal    uint32
}

type hangTrack struct {
	ring       [32]uint32
	pos        int
	count      int
	loopCycles uint64
	mmioAddr   uint32
	mmioVal    uint32
	mmioAt     uint64
	reported   bool
}

type Chip struct {
	Flash []byte
	SRAM  []byte
	ROM   []byte
	// PSRAM is the QMI window-1 backing store; nil when no device is attached.
	PSRAM         []byte
	psramSize     uint32
	psramLatency  uint32
	psramCS       int
	psramWritable bool
	// psramStall guards the engine advance charged for PSRAM latency: a DMA
	// engine ticked from inside the stall may itself access PSRAM, and the
	// outer advance already owns the virtual clock.
	psramStall bool

	CPU0 *m33.CPU
	CPU1 *m33.CPU

	// ppb is the per-core private System Control Space (NVIC/SCB/SysTick).
	// NVIC aliases core 0 so existing single-core callers keep their meaning.
	ppb  [2]*NVIC
	NVIC *NVIC

	// Cycles is the master clock counter, in CPU cycles at 150 MHz.
	Cycles uint64
	// lastMicros is the last microsecond tick applied to the slow
	// peripherals. nextMicros is the cycle count of the next boundary, kept
	// as a multiplication-free fast path for tickPeripherals.
	lastMicros uint64
	nextMicros uint64
	// core is the CPU currently being stepped (0 or 1).
	core int
	// writeCore identifies the core executing a bus write. -1 denotes a host
	// or DMA write, which is remote to both cores.
	writeCore int
	// Paused advances time without executing instructions.
	Paused bool
	// Halted reports that a core stopped on an unexpected condition.
	Halted bool
	// ContinueAfterHalt keeps the clock and peripherals running after a core
	// halts, so PIO, PWM and DMA engines keep producing state. The halted
	// cores themselves stop executing.
	ContinueAfterHalt bool

	// Pin state and per-pin external drive for the board model.
	Pins *PinState
	// packageSel is the SYSINFO.PACKAGE_SEL value: 1 for QFN-60 (RP2350A,
	// 30 GPIOs) and 0 for QFN-80 (RP2350B, 48 GPIOs).
	packageSel uint32

	// Booted flash partition (A/B slots). partSize is 0 for a flat image;
	// runtime XIP addresses below it are translated by partOffset.
	partTable          *PartitionTable
	partOffset         uint32
	partSize           uint32
	preferredPartition int

	// bootselHeld models the BOOTSEL button pulling the QSPI chip select low.
	// The synthetic bootrom does not enter USB boot mode, but firmware can read
	// the line through SIO GPIO_HI_IN bit 27 (QSPI_CSN), as the TinyUSB BSP and
	// the real bootrom do.
	bootselHeld   bool
	bootselAtBoot bool

	io     ioBank
	sio    sioBlock
	tmr    [2]timerBlock
	usb    *usbBlock
	trng   *trngBlock
	sha    *shaBlock
	powman *powmanBlock
	hstx   *hstxBlock
	pwm    *pwmBlock
	adc    *adcBlock
	i2c    [2]*i2cBlock
	pio    [3]*pioBlock
	uart   [2]*uartBlock
	dma    *dmaBlock
	otp    *otpBlock
	wd     *watchdogBlock
	// pioActive has a bit per PIO block with at least one enabled state
	// machine, so execution can skip idle blocks. Dividers and input
	// synchronizers still advance on every system cycle.
	pioActive uint8

	// GPIO input levels are stable for most cycles. Cache the bit-parallel
	// resolution so always-on PIO synchronizers do not rescan all pins.
	pioInputValid       bool
	pioInputOut         uint64
	pioInputOE          uint64
	pioInputExtLevel    uint64
	pioInputExtValid    uint64
	pioInputPulls       uint64
	pioInputPWMPins     uint64
	pioInputPadIE       uint64
	pioInputPadISO      uint64
	pioInputPadOD       uint64
	pioInputCacheLevels uint64

	regs    []*regBlock
	byBase  map[uint32]*regBlock
	qmi     *qmiBlock
	xipCtrl *regBlock
	// regBlockFor's one-entry cache (see regBlockFor).
	lastPage  uint32
	lastBlock *regBlock

	// OnDMA reports DMA transfers (diagnostics).
	OnDMA func(ch int, readAddr, writeAddr, count, ctrl uint32)

	// OnI2C reports hardware I2C phases for the logic analyzer. kind is
	// "addr", "data", "read", "stop" or "nack".
	OnI2C func(bus int, cycles uint64, kind string, addr uint8, data byte, ack bool)

	// OnSPI0 and OnSPI1 are called for every byte clocked out; they return the
	// byte clocked in on MISO.
	OnSPI0 func(tx byte) byte
	OnSPI1 func(tx byte) byte

	// OnUART0 and OnUART1 receive console bytes written by firmware.
	OnUART0 func(b byte)
	OnUART1 func(b byte)
	// OnUSBMIDI receives USB-MIDI event packets (4 bytes per event) sent by
	// firmware on the MIDI IN endpoint.
	OnUSBMIDI func(b []byte)
	// OnUSBHID receives decoded HID input reports (keyboard, mouse, gamepad).
	OnUSBHID func(USBHIDEvent)

	// Logger receives peripheral diagnostics. It is never nil: New installs a
	// discarding logger so callers can log unconditionally.
	Logger *slog.Logger

	// Debug, when non-nil, adds execution breakpoints and watchpoints. Normal
	// runs leave it nil.
	Debug *Debugger

	// IRQCount counts delivered exceptions (diagnostics).
	IRQCount uint64
	usbPoll  int
	pioLogN  int
	rom      *bootROM
	booted   bool

	// Flash programming counters and reset bookkeeping for OTA self-updates.
	flashWrites        uint64
	boots              uint64
	resetReason        string
	bootEntry          uint32
	bootSP             uint32
	pendingReset       bool
	pendingResetAt     uint64
	pendingResetReason string

	// Hang detection: a tight PC loop that stops making progress, optionally
	// polling one MMIO register. Zero disables it.
	HangCycles uint64
	Hang       *HangInfo
	OnHang     func(HangInfo)
	hang       [2]hangTrack

	// profile collects PC samples, opt-in exact coverage, IRQ latency and
	// stack watermarks; profActive gates the per-instruction hook.
	profile    *profileState
	profActive bool

	// memGen invalidates the instruction predecode cache after flash/QMI
	// changes or DMA code writes. CPU SRAM stores clear individual entries.
	memGen uint32

	// Unhandled records accesses to unmapped peripheral addresses.
	Unhandled map[uint32]uint32
	// OnUnhandled is called for each unmapped access (may be nil).
	OnUnhandled func(addr uint32, write bool, val uint32)

	// periodicHooks run on a virtual-cycle grid instead of every cycle.
	// periodicNext caches the earliest deadline so the per-cycle cost stays a
	// single comparison (see AddPeriodicHook).
	periodicHooks []periodicHook
	periodicNext  uint64

	// deadlines are edge-driven one-shot callbacks (see AddDeadline). They do
	// not forbid the idle fast-forward: the jump stops on the earliest one.
	deadlines    []*Deadline
	deadlineNext uint64

	// MMIO trace hook.
	Trace func(addr uint32, write bool, val uint32, pc uint32)
	// OnWrite is called for every memory write when set (debugging).
	OnWrite func(addr uint32, val uint32, size int)
}

// fmtHex renders a register value for a log attribute.
func fmtHex(v uint32) string { return fmt.Sprintf("%#08x", v) }

// New creates a chip with zeroed memories.
func New() *Chip {
	flash := make([]byte, FlashSize)
	for i := range flash {
		flash[i] = flashErased
	}
	c := &Chip{
		Flash:              flash,
		SRAM:               make([]byte, SRAMSize),
		ROM:                make([]byte, ROMSize),
		Logger:             slog.New(slog.DiscardHandler),
		Unhandled:          map[uint32]uint32{},
		byBase:             map[uint32]*regBlock{},
		resetReason:        "power-on",
		writeCore:          -1,
		deadlineNext:       noPeriodic,
		memGen:             1,
		packageSel:         1, // QFN-60 / RP2350A
		preferredPartition: -1,
	}
	c.rom = installBootROM(c)
	c.Pins = NewPinState()
	c.io.init(c)
	c.sio.init(c)
	for i := range c.tmr {
		c.tmr[i].init(c, i)
	}

	c.ppb[0] = &NVIC{chip: c, core: 0}
	c.ppb[1] = &NVIC{chip: c, core: 1}
	c.NVIC = c.ppb[0]
	c.CPU0 = m33.New(c)
	c.CPU0.Interrupts = c.ppb[0]
	c.CPU1 = m33.New(c)
	c.CPU1.Interrupts = c.ppb[1]
	predecode := m33.NewPredecodeCache()
	c.CPU0.SetPredecodeCache(predecode)
	c.CPU1.SetPredecodeCache(predecode)

	c.addRegBlock(newResets(c))
	c.addRegBlock(newPSM(c))
	c.addRegBlock(newClocks(c))
	c.addRegBlock(newXOSC(c))
	c.addRegBlock(newPLL(c, 0x40050000))
	c.addRegBlock(newPLL(c, 0x40058000))
	c.addRegBlock(newTicks(c))
	c.addRegBlock(newBootRAM(c))
	c.wd = newWatchdog(c)
	c.addRegBlock(c.wd.block)
	c.addRegBlock(newSysInfo(c))
	c.addRegBlock(newTBMAN(c))
	c.trng = newTRNG()
	c.addRegBlock(c.trng.block)
	c.sha = newSHA()
	c.addRegBlock(c.sha.block)
	c.powman = newPOWMAN()
	c.addRegBlock(c.powman.block)
	c.hstx = newHSTX()
	c.addRegBlock(c.hstx.ctrl)
	c.addRegBlock(c.hstx.fifo)
	c.addRegBlock(newROSC(c))
	c.xipCtrl = newXIPCtrl(c)
	c.addRegBlock(c.xipCtrl)
	c.qmi = newQMI()
	c.addRegBlock(c.qmi.block)
	c.addRegBlock(c.io.bank0)
	c.addRegBlock(c.io.pads)
	c.addRegBlock(c.tmr[0].block)
	c.addRegBlock(c.tmr[1].block)
	c.AddUART(0x40070000, 0)
	c.AddUART(0x40078000, 1)
	c.AddSPI(0x40080000, 0)
	c.AddSPI(0x40088000, 1)
	c.dma = newDMA(c)
	c.addRegBlock(c.dma.block)
	c.usb = newUSB(c)
	c.addRegBlock(c.usb.dpram)
	c.addRegBlock(c.usb.regs)
	c.pwm = newPWM(c)
	c.addRegBlock(c.pwm.block)
	c.Pins.LevelOverride = c.pwm.pinLevel
	c.adc = newADC(c)
	c.addRegBlock(c.adc.block)
	c.i2c[0] = newI2C(c, 0x40090000, 0)
	c.i2c[1] = newI2C(c, 0x40098000, 1)
	c.addRegBlock(c.i2c[0].block)
	c.addRegBlock(c.i2c[1].block)
	c.pio[0] = newPIO(c, pioBase0, 0)
	c.pio[1] = newPIO(c, pioBase1, 1)
	c.pio[2] = newPIO(c, pioBase2, 2)
	for _, p := range c.pio {
		c.addRegBlock(p.block)
	}
	c.otp = newOTP(c)
	c.addRegBlock(c.otp.block)
	c.Reset()
	return c
}

func (c *Chip) addRegBlock(b *regBlock) {
	c.regs = append(c.regs, b)
	c.byBase[b.base] = b
	c.lastBlock = nil
	sort.Slice(c.regs, func(i, j int) bool { return c.regs[i].base < c.regs[j].base })
}

// noPeriodic marks the absence of a periodic hook deadline.
const noPeriodic = ^uint64(0)

// periodicHook is an interval sampler: it is interested in the virtual clock
// only every `every` cycles, so the chip calls it on that grid instead of once
// per cycle and lets an idle fast-forward land on the next grid point.
type periodicHook struct {
	every uint64
	next  uint64
	fn    func()
}

// AddPeriodicHook schedules fn on a fixed virtual-cycle grid. Traces, analog
// sources and other interval samplers use it: a per-cycle call would cost the
// interpreter a function call for every cycle and, worse, forbid fast-forwarding
// an idle core (see canIdleSkip). every is clamped to at least one cycle.
//
// Missed slots are skipped, never replayed, so this is for value samplers, not
// edge-sensitive observers (use a pin listener plus a Deadline for those). A
// callback may add a hook, but the new hook waits for the next dispatch pass;
// callbacks must not remove hooks.
func (c *Chip) AddPeriodicHook(every uint64, fn func()) {
	if fn == nil {
		return
	}
	if every == 0 {
		every = 1
	}
	c.periodicHooks = append(c.periodicHooks, periodicHook{every: every, next: c.Cycles + every, fn: fn})
	c.refreshPeriodicNext()
}

// refreshPeriodicNext recomputes the earliest periodic deadline.
func (c *Chip) refreshPeriodicNext() {
	c.periodicNext = noPeriodic
	for i := range c.periodicHooks {
		if c.periodicHooks[i].next < c.periodicNext {
			c.periodicNext = c.periodicHooks[i].next
		}
	}
}

// rewindPeriodicHooks restores the grid after the virtual clock restarts at
// zero, so a reset cannot leave a sampler waiting on a stale deadline.
func (c *Chip) rewindPeriodicHooks() {
	for i := range c.periodicHooks {
		c.periodicHooks[i].next = c.Cycles + c.periodicHooks[i].every
	}
	c.refreshPeriodicNext()
}

// Deadline is a reschedulable one-shot callback on the virtual clock. Unlike a
// per-cycle hook it does not forbid the idle fast-forward: Run stops on the
// earliest live deadline, fires it, and carries on. Devices use it for events
// whose time is known in advance (a WS2812B reset gap, a scripted input).
type Deadline struct {
	chip *Chip
	fn   func()
	at   uint64
	live bool
}

// AddDeadline registers fn and returns its handle. Arm it with Schedule.
func (c *Chip) AddDeadline(fn func()) *Deadline {
	if fn == nil {
		return nil
	}
	d := &Deadline{chip: c, fn: fn}
	c.deadlines = append(c.deadlines, d)
	return d
}

// Schedule arms the deadline. A time at or before the current cycle moves to
// the next cycle so the callback cannot re-enter the step that scheduled it.
func (d *Deadline) Schedule(at uint64) {
	if d == nil {
		return
	}
	if now := d.chip.Cycles; at <= now {
		at = now + 1
	}
	d.at = at
	d.live = true
	d.chip.refreshDeadlines()
}

// Cancel disarms the deadline without removing it.
func (d *Deadline) Cancel() {
	if d == nil || !d.live {
		return
	}
	d.live = false
	d.chip.refreshDeadlines()
}

// Live reports whether the deadline is armed.
func (d *Deadline) Live() bool { return d != nil && d.live }

func (c *Chip) refreshDeadlines() {
	c.deadlineNext = noPeriodic
	for _, d := range c.deadlines {
		if d.live && d.at < c.deadlineNext {
			c.deadlineNext = d.at
		}
	}
}

// runDeadlines fires every deadline that has come due. A callback may
// reschedule itself; the entry is disarmed first so the callback observes a
// clean state.
func (c *Chip) runDeadlines() {
	if c.Cycles < c.deadlineNext {
		return
	}
	for i := range c.deadlines {
		d := c.deadlines[i]
		if !d.live || d.at > c.Cycles {
			continue
		}
		d.live = false
		d.fn()
	}
	c.refreshDeadlines()
}

// nextEventCycle is the nearest cycle at which a periodic sampler or deadline
// must run. Run caps an idle jump so neither grid is overshot.
func (c *Chip) nextEventCycle() uint64 { return min(c.periodicNext, c.deadlineNext) }

// cancelDeadlines disarms every deadline after the clock rewinds. Devices
// re-arm from their next edge or periodic tick.
func (c *Chip) cancelDeadlines() {
	for _, d := range c.deadlines {
		d.live = false
	}
	c.deadlineNext = noPeriodic
}

// runPeriodicHooks fires every hook whose deadline has been reached and moves
// it to its next grid point. Missed slots (a PSRAM stall or an idle jump) are
// skipped rather than replayed: interval samplers only need the current value.
func (c *Chip) runPeriodicHooks() {
	if c.Cycles < c.periodicNext {
		return
	}
	n := len(c.periodicHooks)
	for i := range n {
		if i >= len(c.periodicHooks) {
			break
		}
		h := &c.periodicHooks[i]
		if h.next > c.Cycles {
			continue
		}
		h.fn()
		// Re-index: the callback may have appended a hook and reallocated the
		// slice, leaving h pointing at the old backing array.
		if i >= len(c.periodicHooks) {
			continue
		}
		h = &c.periodicHooks[i]
		// The hook may have reset the chip and rewound the clock.
		if c.Cycles < h.next {
			h.next = c.Cycles + h.every
			continue
		}
		if missed := (c.Cycles - h.next) / h.every; missed > 0 {
			h.next += missed * h.every
		}
		if h.next <= c.Cycles {
			h.next = c.Cycles + h.every
		}
	}
	c.refreshPeriodicNext()
}

// coreIdle reports whether a core has no instructions to run this cycle.
func coreIdle(cpu *m33.CPU) bool { return cpu.Sleeping || cpu.Parked || cpu.Halted }

// invalidateMonitors applies a write to each core's exclusive monitor. writer
// is the writing core, or -1 for DMA/host writes. A local store clears only its
// own monitor; another master's write signals an exclusive-loss event.
func (c *Chip) invalidateMonitors(addr uint32, writer int) {
	// Two explicit checks: this runs on every store, and a slice literal plus a
	// range loop here shows up in MMIO-heavy firmware.
	if cpu := c.CPU0; cpu != nil && cpu.MonitorCovers(addr) {
		if writer == 0 {
			cpu.ClearMonitor()
		} else {
			cpu.InvalidateMonitor()
		}
	}
	if cpu := c.CPU1; cpu != nil && cpu.MonitorCovers(addr) {
		if writer == 1 {
			cpu.ClearMonitor()
		} else {
			cpu.InvalidateMonitor()
		}
	}
}

// Run executes n cycles across both cores.
// Run executes n cycles across both cores and returns the number of cycles
// actually consumed. A debugger stop ends the call early.
func (c *Chip) Run(n uint64) uint64 {
	// Count completed cycles locally: a reset rewinds Cycles to zero, and an
	// absolute target would then make this call run for the old elapsed time
	// again (a watchdog reboot loop would stretch a 1 ms chunk into minutes).
	var done uint64
	for done < n && (!c.Halted || c.ContinueAfterHalt) {
		if c.Debug != nil && c.Debug.Stopped {
			return done
		}
		// With both cores asleep and nothing that needs per-cycle attention,
		// only timers can make progress; jump to the next microsecond tick
		// instead of executing 150 empty cycles.
		if c.Cycles&7 == 0 && c.canIdleSkip() {
			skip := cyclesPerMicrosecond - c.Cycles%cyclesPerMicrosecond
			skip = min(skip, n-done)
			// A SysTick wrap is the only other reason a sleeping core has to
			// run; land on it exactly instead of overshooting to the next
			// microsecond boundary.
			skip = min(skip, c.ppb[0].cyclesUntilSysTick(c.Cycles))
			skip = min(skip, c.ppb[1].cyclesUntilSysTick(c.Cycles))
			// Interval samplers and one-shot deadlines must be called on
			// their grid, so the jump cannot pass the earliest one.
			if next := c.nextEventCycle(); next > c.Cycles {
				skip = min(skip, next-c.Cycles)
			}
			if skip == 0 {
				skip = 1
			}
			// A due SysTick wrap may have just pended and woken a core.
			if c.canIdleSkip() {
				// Disabled PIO dividers and input synchronizers still run. The
				// final cycle is handled by tickPeripherals below.
				c.advancePIO(skip - 1)
				c.Cycles += skip
				done += skip
				c.usbPoll = 7 // check host input on this tick
				c.tickPeripherals()
				continue
			}
		}
		c.Step()
		done++
	}
	return done
}

// SetHangDetection enables tight-loop detection after cycles of virtual time.
// Zero disables it. It clears any previous report.
func (c *Chip) SetHangDetection(cycles uint64) {
	c.HangCycles = cycles
	c.Hang = nil
	c.hang = [2]hangTrack{}
	c.Paused = false
}

// ClearHang clears a reported hang and resumes the cores.
func (c *Chip) ClearHang() {
	c.Hang = nil
	c.hang = [2]hangTrack{}
	c.Paused = false
}

// noteMMIO records the most recent peripheral read for hang diagnostics.
func (c *Chip) noteMMIO(addr, val uint32) {
	if c.HangCycles == 0 || c.Hang != nil {
		return
	}
	core := c.core
	if core < 0 || core > 1 {
		return
	}
	c.hang[core].mmioAddr = addr &^ 3
	c.hang[core].mmioVal = val
	c.hang[core].mmioAt = c.Cycles
}

const hangSampleCycles = 64

// hangSample checks a sampled PC for a tight loop. It runs on the emulator
// thread from stepCore.
func (c *Chip) hangSample(core int, pc uint32) {
	if c.HangCycles == 0 || c.Hang != nil || c.Cycles&(hangSampleCycles-1) != 0 {
		return
	}
	hs := &c.hang[core]
	if hs.reported {
		return
	}
	hs.ring[hs.pos] = pc
	hs.pos = (hs.pos + 1) % len(hs.ring)
	if hs.count < len(hs.ring) {
		hs.count++
	}
	if hs.count < 16 {
		return
	}
	seen := map[uint32]int{}
	for i := 0; i < hs.count; i++ {
		seen[hs.ring[i]]++
	}
	repeat := seen[pc]
	if len(seen) > 8 || repeat < 4 {
		hs.loopCycles = 0
		return
	}
	// A loop that only reads SRAM and never a peripheral register is a
	// legitimate busy-wait (for example while (!timer_fired)), not a hang.
	// Only a peripheral poll or a pure one/two-instruction spin counts.
	polled := hs.mmioAt != 0 && c.Cycles-hs.mmioAt <= hs.loopCycles+hangSampleCycles
	if !polled && len(seen) > 2 {
		hs.loopCycles = 0
		return
	}
	hs.loopCycles += hangSampleCycles
	if hs.loopCycles < c.HangCycles {
		return
	}
	hs.reported = true
	c.Hang = &HangInfo{
		Core:       core,
		PC:         pc,
		Cycles:     c.Cycles,
		LoopCycles: hs.loopCycles,
		MMIOAddr:   hs.mmioAddr,
		MMIOVal:    hs.mmioVal,
	}
	// Pause execution; peripherals and virtual time keep ticking so the UI can
	// still show the state at the moment of the report.
	c.Paused = true
	if c.OnHang != nil {
		c.OnHang(*c.Hang)
	}
}

// canIdleSkip reports whether a jump to the next microsecond tick cannot
// change any observable state: both cores are asleep, parked or halted, no DMA
// or PIO engine is running, and the USB device is off. Interval samplers and
// one-shot deadlines do not forbid the jump: Run bounds it by their next
// deadline and tickPeripherals fires them on arrival.
func (c *Chip) canIdleSkip() bool {
	if c.Paused || c.pendingReset || !coreIdle(c.CPU0) || !coreIdle(c.CPU1) {
		return false
	}
	if c.dma != nil && c.dma.busy() {
		return false
	}
	if c.pwm != nil && c.pwm.busy() {
		return false
	}
	if c.pioActive != 0 {
		return false
	}
	if c.usb != nil && !c.usb.idleForSkip() {
		return false
	}
	for _, t := range c.tmr {
		if t.source&1 != 0 {
			return false
		}
	}
	if c.hstx != nil && c.hstx.active() {
		return false
	}
	for _, u := range c.uart {
		if u != nil && u.irqPending.Load() {
			return false
		}
	}
	return true
}

// IdleFastForward reports whether Run may currently jump over idle cycles.
// Devices use it in tests and benchmarks to assert that attaching a decoder or
// bus model did not reintroduce a per-cycle poll.
func (c *Chip) IdleFastForward() bool { return c.canIdleSkip() }

// Step advances the machine by one cycle.
func (c *Chip) Step() {
	if c.pendingReset && c.Cycles >= c.pendingResetAt {
		c.applyReset()
		return
	}
	if c.Halted && !c.ContinueAfterHalt {
		return
	}
	c.Cycles++

	c.tickPeripherals()

	if !c.Paused {
		// Sleeping and parked cores need no work; checking here avoids two
		// function calls per cycle for the common idle case. A debugger stop
		// freezes both cores; StepDebug drives one core directly.
		if c.Debug == nil || !c.Debug.Stopped {
			if !c.CPU0.Sleeping && !c.CPU0.Parked {
				c.stepCore(c.CPU0)
			}
			if !c.CPU1.Sleeping && !c.CPU1.Parked {
				c.stepCore(c.CPU1)
			}
		}
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
	pc := cpu.R[15]
	if c.Debug != nil && c.Debug.checkPC(c.core, pc, c.Cycles) {
		return
	}
	c.writeCore = c.core
	if c.booted && pc < ROMSize {
		if c.rom.exec(c, cpu, pc) {
			c.writeCore = -1
			return
		}
		// The bootrom is a fixed mask ROM, not extendable memory. Falling
		// through would execute the table bytes as instructions.
		cpu.Halt(fmt.Sprintf("execution from unmapped bootrom address %#08x", pc))
		c.writeCore = -1
		return
	}
	if c.profActive && c.profile != nil {
		psp := cpu.IPSR == 0 && cpu.CONTROL&2 != 0
		c.profile.observe(c, c.core, pc, cpu.R[13], psp)
	}
	cpu.Step()
	c.hangSample(c.core, cpu.R[15])
	c.writeCore = -1
}

// SignalEvent implements SEV: the event is visible to every core, and cores
// sleeping in WFE resume. WFI sleepers only wake on an interrupt.
func (c *Chip) SignalEvent() {
	for _, cpu := range []*m33.CPU{c.CPU0, c.CPU1} {
		cpu.EventRegister = true
		if cpu.SleepOnEvent {
			cpu.Sleeping = false
			cpu.SleepOnEvent = false
		}
	}
}

// advanceSleep skips time forward when all cores are asleep.
func (c *Chip) advanceSleep(max uint64) {
	c.Cycles += max
	c.tickPeripherals()
}

func (c *Chip) advancePIO(cycles uint64) {
	if cycles == 0 {
		return
	}
	pioInputs := c.pioInputLevels()
	for _, p := range c.pio {
		if p != nil {
			p.advanceCycles(cycles, pioInputs)
		}
	}
}

func (c *Chip) tickPeripherals() {
	// SysTick is cycle-accurate: the lazy comparison is cheap enough to run on
	// every cycle, and a WFI wake must happen before the cores are stepped.
	// The enable check is the first thing sync() does, so hoisting it only
	// saves a call.
	if c.ppb[0].tick.enabled() {
		c.ppb[0].tickSysTick(c.Cycles)
	}
	if c.ppb[1].tick.enabled() {
		c.ppb[1].tickSysTick(c.Cycles)
	}
	for i := range c.tmr {
		c.tmr[i].tick(c.Cycles)
	}
	if c.pwm != nil && c.pwm.busy() {
		c.pwm.tick()
	}
	if c.adc != nil {
		c.adc.tick()
	}
	// Slow peripherals run once per microsecond. Tick every boundary crossed
	// since the last call: a PSRAM latency charge can advance Cycles
	// mid-instruction, and skipping a boundary would delay an alarm by a whole
	// microsecond.
	for c.Cycles >= c.nextMicros {
		c.lastMicros++
		t := c.lastMicros
		if c.wd != nil {
			c.wd.tick(t)
		}
		if c.powman != nil {
			c.powman.tick(c)
		}
		c.nextMicros += cyclesPerMicrosecond
	}
	if c.dma != nil && c.dma.busy() {
		c.dma.tick()
	}
	// Same predicate as the first line of each tick: hoist it to skip the call
	// on chips that never enable the block (the common case).
	if c.sha != nil && c.sha.pending {
		c.sha.tick(c)
	}
	if c.hstx != nil && c.hstx.csr&1 != 0 {
		c.hstx.tick()
	}
	// Host input and the USB host state machine are asynchronous to the
	// instruction stream; polling every 8 cycles costs nothing observable and
	// keeps the two atomic checks off the per-cycle path.
	c.usbPoll++
	if c.usbPoll >= 8 {
		c.usbPoll = 0
		for _, u := range c.uart {
			if u != nil && u.irqPending.Load() {
				u.tick()
			}
		}
		if c.usb != nil {
			c.usb.tick(c)
		}
	}
	// PIO clock dividers are free-running and GPIO inputs are synchronized
	// even while every state machine is disabled. Both the level computation
	// and the per-block tick are skipped when they provably have nothing to do.
	pioInputs := c.pioInputCacheLevels
	if !c.pioInputStable() {
		pioInputs = c.pioInputLevels()
	}
	for _, p := range c.pio {
		if p == nil {
			continue
		}
		if p.active|p.clocking == 0 && p.inputPending == 0 && p.inputLast == pioInputs {
			continue
		}
		p.tickWithInputs(pioInputs)
	}
	c.runDeadlines()
	if c.Cycles >= c.periodicNext {
		c.runPeriodicHooks()
	}
}

// NVIC delivers interrupts to the cores.

// ppbForCore returns the private System Control Space of the executing core.
func (c *Chip) ppbForCore() *NVIC {
	if c.core == 1 {
		return c.ppb[1]
	}
	return c.ppb[0]
}

// PendIRQ marks a shared peripheral IRQ pending on both cores. Each core's
// NVIC decides independently whether the interrupt is enabled and at what
// priority, exactly like the RP2350's replicated interrupt lines.
func (c *Chip) PendIRQ(irq int) {
	c.ppb[0].Pend(irq)
	c.ppb[1].Pend(irq)
}

// ClearPendingIRQ clears a shared peripheral IRQ on both cores.
func (c *Chip) ClearPendingIRQ(irq int) {
	c.ppb[0].ClearPending(irq)
	c.ppb[1].ClearPending(irq)
}

// pendCoreIRQ pends a per-core interrupt (SIO FIFO and doorbell) on one core.
func (c *Chip) pendCoreIRQ(core, irq int) {
	if core < 0 || core > 1 {
		return
	}
	c.ppb[core].Pend(irq)
}

// clearCorePendingIRQ clears a per-core interrupt on one core.
func (c *Chip) clearCorePendingIRQ(core, irq int) {
	if core < 0 || core > 1 {
		return
	}
	c.ppb[core].ClearPending(irq)
}

// SetBootsel drives the QSPI chip-select line low when held, matching the
// BOOTSEL button. It is visible to firmware as SIO_GPIO_HI_IN.QSPI_CSN. Chip
// reset does not clear it: the real bootrom samples the line during reset.
func (c *Chip) SetBootsel(held bool) { c.bootselHeld = held }

// BootselHeld reports whether the BOOTSEL button is currently pressed.
func (c *Chip) BootselHeld() bool { return c.bootselHeld }

// BootselAtBoot reports whether BOOTSEL was held when the current image
// booted. The real bootrom samples the line at reset; the synthetic bootrom
// records it but does not enter USB picoboot mode.
func (c *Chip) BootselAtBoot() bool { return c.bootselAtBoot }

// Reset performs a chip reset and starts both cores from flash.
// Package identifies the bonded RP2350 variant: QFN-60 is the RP2350A with
// 30 GPIOs, QFN-80 is the RP2350B with 48.
type Package int

const (
	PackageQFN60 Package = iota
	PackageQFN80
)

// SetPackage selects the SYSINFO.PACKAGE_SEL value and the bonded GPIO count.
func (c *Chip) SetPackage(p Package) {
	if p == PackageQFN80 {
		c.packageSel = 0
	} else {
		c.packageSel = 1
	}
}

// Package reports the selected package.
func (c *Chip) Package() Package {
	if c.packageSel == 0 {
		return PackageQFN80
	}
	return PackageQFN60
}

// GPIOs is the number of bonded GPIOs for the selected package.
func (c *Chip) GPIOs() int {
	if c.packageSel == 0 {
		return 48
	}
	return 30
}

// MemoryGeneration is bumped for bulk code-memory or mapping changes.
func (c *Chip) MemoryGeneration() uint32 { return c.memGen }

// invalidateCodeAll drops both cores' predecode caches. Use it for flash/QMI
// changes and DMA writes where many instruction addresses can change.
func (c *Chip) invalidateCodeAll() {
	c.memGen++
	if c.memGen == 0 {
		c.memGen = 1
	}
	if c.CPU0 != nil {
		c.CPU0.InvalidatePredecodeAll(c.memGen)
	}
	if c.CPU1 != nil {
		c.CPU1.InvalidatePredecodeAll(c.memGen)
	}
}

// codeWrite invalidates the predecode entries affected by a CPU/DMA store.
// SRAM self-modifying code is handled per range; flash-sized bulk updates use
// invalidateCodeAll from their write path.
func (c *Chip) codeWrite(addr, size uint32) {
	if size == 0 {
		return
	}
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		if c.CPU0 != nil {
			c.CPU0.InvalidatePredecode(addr, size)
		}
		if c.CPU1 != nil {
			c.CPU1.InvalidatePredecode(addr, size)
		}
	}
}

// codeMapping reports whether addr is an XIP/QMI control register whose write
// can change what instructions fetch.
func codeMapping(addr uint32) bool {
	return addr >= xipBase && addr < xipBase+0x1000 || addr >= qmiBase && addr < qmiBase+0x1000
}

func (c *Chip) Reset() {
	c.Cycles = 0
	c.lastMicros = 0
	c.nextMicros = cyclesPerMicrosecond
	c.Halted = false
	c.core = 0
	c.writeCore = -1
	c.pendingReset = false
	c.Hang = nil
	c.hang = [2]hangTrack{}
	c.Paused = false
	c.usbPoll = 0
	c.pioLogN = 0
	c.rewindPeriodicHooks()
	c.cancelDeadlines()
	c.CPU0.Reset()
	c.CPU1.Reset()
	c.CPU0.Parked = false
	c.CPU1.Parked = false
	c.ppb[0].reset()
	c.ppb[1].reset()
	c.NVIC = c.ppb[0]
	c.Pins.reset()
	c.io.reset()
	c.sio.reset()
	// Every register block returns to its architectural reset state. The
	// hooks restore values that are not zero (PIO/PWM/QMI/OTP/BOOTRAM) and
	// clear the state those registers drive.
	for _, b := range c.regs {
		b.reset(c)
	}
	c.pioActive = 0
	c.pioInputValid = false
	if c.Debug != nil {
		c.Debug.Reset()
	}
	c.resetPSRAM()
	c.invalidateCodeAll()
	c.profileOnReset()
}

// resetCore performs the core-local reset requested by SYSRESETREQ. It must not
// use Chip.Reset: virtual time, shared peripherals and the other core survive.
func (c *Chip) resetCore(core int) {
	if core < 0 || core > 1 {
		return
	}
	cpu := c.CPU0
	n := c.ppb[0]
	if core == 1 {
		cpu = c.CPU1
		n = c.ppb[1]
	}
	cpu.Reset()
	n.reset()
	if core == 0 {
		cpu.Parked = false
		cpu.SetVectorTable(FlashBase)
	} else {
		cpu.Parked = true
		c.sio.resetLaunch()
	}
	c.Halted = c.CPU0.Halted || c.CPU1.Halted
}

// requestReset schedules a chip reset. reason is reported to the UI and CLI;
// delayCycles lets the bootrom's reboot(delay_ms) behave like hardware.
func (c *Chip) requestReset(reason string, delayCycles uint64) {
	at := c.Cycles + delayCycles
	if c.pendingReset && at >= c.pendingResetAt {
		return
	}
	c.pendingReset = true
	c.pendingResetAt = at
	c.pendingResetReason = reason
}

// applyReset performs a pending reset and boots from the vector table in flash.
func (c *Chip) applyReset() {
	reason := c.pendingResetReason
	force := c.wd != nil && c.wd.reason&wdReasonForce != 0
	c.pendingReset = false
	c.Boot()
	c.resetReason = reason
	if c.wd != nil {
		// The reason register reports the cause of this boot, not a stale one
		// from an earlier watchdog reset.
		c.wd.reason = 0
		if reason == "watchdog" {
			if force {
				c.wd.reason |= wdReasonForce
			} else {
				c.wd.reason |= wdReasonTimer
			}
		}
	}
}

// Boot vectors core 0 at the image in flash.
func (c *Chip) Boot() {
	// Record the button state before Reset (which preserves it) so the UI can
	// report a BOOTSEL-held boot, as the bootrom would sample it.
	c.bootselAtBoot = c.bootselHeld
	if c.bootselAtBoot {
		c.Logger.Debug("bootsel held at reset; USB boot mode is not modelled")
	}
	c.selectPartition()
	c.Reset()
	c.booted = true
	c.CPU0.SetVectorTable(FlashBase)
	c.CPU1.Parked = true
	c.boots++
	c.bootSP = c.CPU0.MSP
	c.bootEntry = c.CPU0.R[15]
}

// selectPartition parses a partition table at the flash base and maps the
// chosen slot's storage offset into the 0x10000000 runtime window. A flat
// image (no magic) keeps offset 0.
func (c *Chip) selectPartition() {
	preferred := c.preferredPartition
	c.preferredPartition = -1 // a flash-update preference is one-shot
	c.partTable = ParsePartitionTable(c.Flash)
	c.partOffset, c.partSize = 0, 0
	if c.partTable == nil {
		return
	}
	idx := c.partTable.BootPartition(preferred)
	if idx < 0 {
		return
	}
	c.partOffset = c.partTable.Entries[idx].Offset
	c.partSize = c.partTable.Entries[idx].Size
}

// Partition returns the parsed table, if any, and the booted slot index.
func (c *Chip) Partition() (*PartitionTable, int) {
	if c.partTable == nil {
		return nil, -1
	}
	for i, p := range c.partTable.Entries {
		if p.Offset == c.partOffset {
			return c.partTable, i
		}
	}
	return c.partTable, -1
}

// PreferPartition asks the next boot to run this slot (used by rom_reboot's
// flash-update type).
func (c *Chip) PreferPartition(idx int) { c.preferredPartition = idx }

// flashIndex translates a runtime XIP address in the booted partition to a
// storage offset in the flash array. A partitioned runtime window faults at and
// beyond its aperture; only the no-translate alias exposes raw storage there.
func (c *Chip) flashIndex(addr uint32, size int) (uint32, bool) {
	if size < 0 || addr < FlashBase || addr >= FlashBase+FlashSize {
		return 0, false
	}
	off := uint64(addr - FlashBase)
	if c.partSize > 0 {
		if off >= uint64(c.partSize) || uint64(size) > uint64(c.partSize)-off {
			return 0, false
		}
		off += uint64(c.partOffset)
	}
	if off+uint64(size) > uint64(FlashSize) {
		return 0, false
	}
	return uint32(off), true
}

// flashStorage maps an offset in a translated XIP alias to raw flash storage.
func (c *Chip) flashStorage(off uint32, size int) (uint32, bool) {
	if size < 0 || uint64(off) >= FlashSize {
		return 0, false
	}
	o := uint64(off)
	if c.partSize > 0 {
		if o >= uint64(c.partSize) || uint64(size) > uint64(c.partSize)-o {
			return 0, false
		}
		o += uint64(c.partOffset)
	}
	if o+uint64(size) > uint64(FlashSize) {
		return 0, false
	}
	return uint32(o), true
}

// flashAliasIndex maps a read in one of the XIP alias windows. The
// no-translate window exposes raw storage addresses; the other views follow
// the booted partition like the main XIP window.
func (c *Chip) flashAliasIndex(addr uint32, size int) (uint32, bool) {
	off, ok := flashAlias(addr)
	if !ok {
		return 0, false
	}
	if addr >= XIPNoTranslateBase && addr < XIPNoTranslateBase+XIPDeviceWindow {
		if uint64(off)+uint64(size) > uint64(FlashSize) {
			return 0, false
		}
		return off, true
	}
	return c.flashStorage(off, size)
}

// BootCount is the number of times the chip has booted, including self-updates.
func (c *Chip) BootCount() uint64 { return c.boots }

// Booted reports whether the chip has taken a reset vector and has an
// executable image. A board can exist without firmware; that chip must stay
// parked instead of running from an uninitialised PC.
func (c *Chip) Booted() bool { return c != nil && c.booted }

// LastResetReason describes the most recent boot.
func (c *Chip) LastResetReason() string { return c.resetReason }

// BootEntry and BootSP are the vector table values of the current boot.
func (c *Chip) BootEntry() uint32 { return c.bootEntry }
func (c *Chip) BootSP() uint32    { return c.bootSP }

// LoadImage copies a flat binary into flash at offset 0.
func (c *Chip) LoadImage(data []byte) {
	copy(c.Flash, data)
}

// flashAlias maps the RP2350 XIP windows onto the flash array. The third
// return reports whether the address was in a window; a true offset that is
// past the flash is an unmapped access.
func flashAlias(addr uint32) (uint32, bool) {
	switch {
	case addr >= XIPNoCacheBase && addr < XIPNoCacheBase+XIPDeviceWindow:
		return addr - XIPNoCacheBase, true
	case addr >= XIPMaintenanceBase && addr < XIPMaintenanceBase+XIPDeviceWindow:
		return addr - XIPMaintenanceBase, true
	case addr >= XIPNoTranslateBase && addr < XIPNoTranslateBase+XIPDeviceWindow:
		return addr - XIPNoTranslateBase, true
	}
	return 0, false
}

// ValidateSegment checks a physical destination without modifying memory.
func (c *Chip) ValidateSegment(addr uint32, data []byte) error {
	n := uint64(len(data))
	end := uint64(addr) + n
	if end > 1<<32 {
		return fmt.Errorf("firmware segment at %#x wraps the 32-bit address space", addr)
	}
	switch {
	case addr >= FlashBase && addr < FlashBase+FlashSize:
		if end > uint64(FlashBase+FlashSize) {
			return fmt.Errorf("firmware segment at %#x exceeds flash", addr)
		}
	case addr >= SRAMBase && addr < SRAMBase+SRAMSize:
		if end > uint64(SRAMBase+SRAMSize) {
			return fmt.Errorf("firmware segment at %#x exceeds SRAM", addr)
		}
	case addr < ROMSize:
		if end > ROMSize {
			return fmt.Errorf("firmware segment at %#x exceeds ROM", addr)
		}
	default:
		if off, ok := psramAlias(addr); ok {
			if c.psramSize == 0 || uint64(off)+n > uint64(c.psramSize) {
				return fmt.Errorf("firmware segment at %#x exceeds configured PSRAM", addr)
			}
			return nil
		}
		return fmt.Errorf("firmware segment destination %#x is not writable memory", addr)
	}
	return nil
}

// LoadSegment copies image bytes to one physical memory destination. The whole
// range is validated before any byte is copied, so a bad segment cannot leave a
// partially loaded image or escape its destination array.
func (c *Chip) LoadSegment(addr uint32, data []byte) error {
	if err := c.ValidateSegment(addr, data); err != nil {
		return err
	}
	switch {
	case addr >= FlashBase && addr < FlashBase+FlashSize:
		copy(c.Flash[addr-FlashBase:], data)
	case addr >= SRAMBase && addr < SRAMBase+SRAMSize:
		copy(c.SRAM[addr-SRAMBase:], data)
	case addr < ROMSize:
		copy(c.ROM[addr:], data)
	default:
		off, _ := psramAlias(addr)
		copy(c.PSRAM[off:], data)
	}
	c.invalidateCodeAll()
	return nil
}

// Bus interface implementation.

func (c *Chip) Read8(addr uint32) uint8 {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		return c.SRAM[addr-SRAMBase]
	}
	if off, ok := c.flashIndex(addr, 1); ok {
		return c.Flash[off]
	}
	if o, ok := c.flashAliasIndex(addr, 1); ok {
		return c.Flash[o]
	}
	if _, ok := flashAlias(addr); ok {
		return c.unhandledRead8(addr)
	}
	if xipMaintenance(addr) {
		return 0
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize == 0 || off >= c.psramSize {
			return c.unhandledRead8(addr)
		}
		c.psramAccess()
		return c.PSRAM[off]
	}
	if addr < ROMSize {
		return c.ROM[addr]
	}
	if addr >= PeriphBase {
		// Peripherals are word registers; byte and halfword accesses select
		// within the containing word (the DPRAM is byte-addressed this way).
		v := c.readMMIOObserved(addr &^ 3)
		return uint8(v >> ((addr & 3) * 8))
	}
	return c.unhandledRead8(addr)
}

func (c *Chip) Read16(addr uint32) uint16 {
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-1 {
		o := addr - SRAMBase
		return binary.LittleEndian.Uint16(c.SRAM[o:])
	}
	// Flat XIP is the common case for instruction fetch: skip the partition
	// translation call.
	if c.partSize == 0 && addr >= FlashBase && addr < FlashBase+FlashSize-1 {
		return binary.LittleEndian.Uint16(c.Flash[addr-FlashBase:])
	}
	if o, ok := c.flashIndex(addr, 2); ok {
		return binary.LittleEndian.Uint16(c.Flash[o:])
	}
	if o, ok := c.flashAliasIndex(addr, 2); ok {
		return binary.LittleEndian.Uint16(c.Flash[o:])
	}
	if _, ok := flashAlias(addr); ok {
		return c.unhandledRead16(addr)
	}
	if xipMaintenance(addr) {
		return 0
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize < 2 || off >= c.psramSize-1 {
			return c.unhandledRead16(addr)
		}
		c.psramAccess()
		return binary.LittleEndian.Uint16(c.PSRAM[off:])
	}
	if addr < ROMSize-1 {
		return binary.LittleEndian.Uint16(c.ROM[addr:])
	}
	if addr >= PeriphBase {
		v := c.readMMIOObserved(addr &^ 3)
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
		return binary.LittleEndian.Uint32(c.SRAM[o:])
	}
	if o, ok := c.flashIndex(addr, 4); ok {
		return binary.LittleEndian.Uint32(c.Flash[o:])
	}
	if o, ok := c.flashAliasIndex(addr, 4); ok {
		return binary.LittleEndian.Uint32(c.Flash[o:])
	}
	if _, ok := flashAlias(addr); ok {
		return c.unhandledRead32(addr)
	}
	if xipMaintenance(addr) {
		return 0
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize < 4 || off >= c.psramSize-3 {
			return c.unhandledRead32(addr)
		}
		c.psramAccess()
		return binary.LittleEndian.Uint32(c.PSRAM[off:])
	}
	if addr < ROMSize-3 {
		return binary.LittleEndian.Uint32(c.ROM[addr:])
	}
	if addr >= PeriphBase {
		return c.readMMIOObserved(addr)
	}
	return c.unhandledRead32(addr)
}

func (c *Chip) Write8(addr uint32, v uint8) {
	if c.OnWrite != nil {
		c.OnWrite(addr, uint32(v), 1)
	}
	c.debugWrite(addr, 1)
	c.invalidateMonitors(addr, c.writeCore)
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize {
		c.SRAM[addr-SRAMBase] = v
		c.codeWrite(addr, 1)
		return
	}
	if _, ok := c.flashIndex(addr, 1); ok {
		// Flash writes are ignored: XIP is read-only in this model.
		return
	}
	if _, ok := flashAlias(addr); ok {
		if _, ok := c.flashAliasIndex(addr, 1); !ok {
			c.unhandledWrite(addr, uint32(v))
		}
		// Read-only flash views; the maintenance window accepts cache
		// maintenance writes as no-ops.
		return
	}
	if xipMaintenance(addr) {
		return
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize == 0 || off >= c.psramSize {
			c.unhandledWrite(addr, uint32(v))
			return
		}
		c.psramAccess()
		if c.psramWritable {
			c.PSRAM[off] = v
			c.invalidateCodeAll()
		}
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
	c.debugWrite(addr, 2)
	c.invalidateMonitors(addr, c.writeCore)
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-1 {
		o := addr - SRAMBase
		c.SRAM[o] = byte(v)
		c.SRAM[o+1] = byte(v >> 8)
		c.codeWrite(addr, 2)
		return
	}
	if _, ok := c.flashIndex(addr, 2); ok {
		return
	}
	if _, ok := flashAlias(addr); ok {
		if _, ok := c.flashAliasIndex(addr, 2); !ok {
			c.unhandledWrite(addr, uint32(v))
		}
		return
	}
	if xipMaintenance(addr) {
		return
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize < 2 || off >= c.psramSize-1 {
			c.unhandledWrite(addr, uint32(v))
			return
		}
		c.psramAccess()
		if c.psramWritable {
			c.PSRAM[off] = byte(v)
			c.PSRAM[off+1] = byte(v >> 8)
			c.invalidateCodeAll()
		}
		return
	}
	if addr >= PeriphBase {
		if codeMapping(addr) {
			c.invalidateCodeAll()
		}
		c.writeMMIO16(addr, v)
		return
	}
	c.unhandledWrite(addr, uint32(v))
}

func (c *Chip) Write32(addr uint32, v uint32) {
	if c.OnWrite != nil {
		c.OnWrite(addr, v, 4)
	}
	c.debugWrite(addr, 4)
	c.invalidateMonitors(addr, c.writeCore)
	if addr >= SRAMBase && addr < SRAMBase+SRAMSize-3 {
		o := addr - SRAMBase
		c.SRAM[o] = byte(v)
		c.SRAM[o+1] = byte(v >> 8)
		c.SRAM[o+2] = byte(v >> 16)
		c.SRAM[o+3] = byte(v >> 24)
		c.codeWrite(addr, 4)
		return
	}
	if _, ok := c.flashIndex(addr, 4); ok {
		return
	}
	if _, ok := flashAlias(addr); ok {
		if _, ok := c.flashAliasIndex(addr, 4); !ok {
			c.unhandledWrite(addr, v)
		}
		return
	}
	if xipMaintenance(addr) {
		return
	}
	if off, ok := psramAlias(addr); ok {
		if c.psramSize < 4 || off >= c.psramSize-3 {
			c.unhandledWrite(addr, v)
			return
		}
		c.psramAccess()
		if c.psramWritable {
			c.PSRAM[off] = byte(v)
			c.PSRAM[off+1] = byte(v >> 8)
			c.PSRAM[off+2] = byte(v >> 16)
			c.PSRAM[off+3] = byte(v >> 24)
			c.invalidateCodeAll()
		}
		return
	}
	if addr >= PeriphBase {
		if codeMapping(addr) {
			c.invalidateCodeAll()
		}
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

// regBlockFor finds the block containing addr. The common case is an access
// on the block's first 4 KB page, which byBase resolves without scanning.
// lastPage/lastBlock cache the previous hit: firmware bursts (SPI bytes, GPIO
// writes) hit the same block thousands of times in a row, and a map lookup per
// access is expensive under wasm.
func (c *Chip) regBlockFor(addr uint32) *regBlock {
	page := addr &^ 0xFFF
	if page == c.lastPage {
		if b := c.lastBlock; b != nil && addr >= b.base && addr < b.base+b.size {
			return b
		}
	}
	if b := c.byBase[page]; b != nil && addr >= b.base && addr < b.base+b.size {
		c.lastPage, c.lastBlock = page, b
		return b
	}
	for _, b := range c.regs {
		if addr >= b.base && addr < b.base+b.size {
			c.lastPage, c.lastBlock = page, b
			return b
		}
	}
	return nil
}

// readMMIO dispatches a 32-bit peripheral read.
func (c *Chip) readMMIOObserved(addr uint32) uint32 {
	v := c.readMMIO(addr)
	c.noteMMIO(addr, v)
	return v
}

func (c *Chip) readMMIO(addr uint32) uint32 {
	if addr >= SIOBase && addr < SIOBase+0x1000 {
		return c.sio.read(c, addr-SIOBase)
	}
	if addr >= SCSBase && addr < SCSBase+0x1000 {
		return c.ppbForCore().read(addr - SCSBase)
	}
	if c.otp != nil {
		if v, ok := c.otp.readWindow(addr); ok {
			return v
		}
	}
	if b := c.regBlockFor(addr); b != nil {
		return b.read(c, addr-b.base)
	}
	if off, b, ok := c.aliasTarget(addr); ok {
		return b.read(c, off)
	}
	return c.unhandledRead32(addr)
}

// aliasTarget resolves the RP2350 atomic register aliases: XOR at +0x1000,
// SET at +0x2000 and CLR at +0x3000 of a 4 KB register block.
func (c *Chip) aliasTarget(addr uint32) (uint32, *regBlock, bool) {
	for _, b := range c.regs {
		if b.noAliases || b.size > 0x1000 {
			continue
		}
		if addr >= b.base+0x1000 && addr < b.base+0x1000+b.size {
			return addr - (b.base + 0x1000), b, true
		}
		if addr >= b.base+0x2000 && addr < b.base+0x2000+b.size {
			return addr - (b.base + 0x2000), b, true
		}
		if addr >= b.base+0x3000 && addr < b.base+0x3000+b.size {
			return addr - (b.base + 0x3000), b, true
		}
	}
	return 0, nil, false
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
		c.ppbForCore().write(addr-SCSBase, v)
		return
	}
	if b := c.regBlockFor(addr); b != nil {
		b.write(c, addr-b.base, v)
		return
	}
	// Atomic aliases: SET/CLR/XOR.
	if off, b, ok := c.aliasTarget(addr); ok {
		switch {
		case addr >= b.base+0x1000 && addr < b.base+0x2000: // XOR
			b.write(c, off, b.peek(off)^v)
		case addr >= b.base+0x2000 && addr < b.base+0x3000: // SET
			if b.setFn != nil {
				b.setFn(c, off, v)
			} else {
				b.write(c, off, b.peek(off)|v)
			}
		default: // CLR
			if b.clearFn != nil {
				b.clearFn(c, off, v)
			} else {
				b.write(c, off, b.peek(off)&^v)
			}
		}
		return
	}
	c.unhandledWrite(addr, v)
}

// writeBootRAMAlias applies byte-strobed atomic writes to boot RAM. The SDK
// updates FLASH_DEVINFO through a halfword XOR alias, so the operation must be
// performed on the selected lanes rather than routed through the word policy.
func (c *Chip) writeBootRAMAlias(addr, v uint32, size int) bool {
	off, b, ok := c.aliasTarget(addr)
	if !ok || b.base != bootramBase || (size != 1 && size != 2) {
		return false
	}
	wordOff := off &^ 3
	shift := (addr & 3) * 8
	mask := uint32(0xff)
	if size == 2 {
		mask = 0xffff
		shift = (addr & 2) * 8
	}
	next := b.read(c, wordOff)
	switch {
	case addr >= b.base+0x1000 && addr < b.base+0x2000:
		next ^= (v & mask) << shift
	case addr >= b.base+0x2000 && addr < b.base+0x3000:
		next |= (v & mask) << shift
	default:
		next &^= (v & mask) << shift
	}
	b.write(c, wordOff, next)
	return true
}

func (c *Chip) writeMMIO8(addr uint32, v uint8) {
	c.writeMMIONarrow(addr, uint32(v), 1)
}

func (c *Chip) writeMMIO16(addr uint32, v uint16) {
	c.writeMMIONarrow(addr, uint32(v), 2)
}

// replicatedMMIO implements the RP2350 sub-word register-write rule: a byte
// write is replicated into all four byte lanes, and a halfword write into both
// halfword lanes of the containing 32-bit register.
func replicatedMMIO(v uint32, size int) uint32 {
	if size == 1 {
		return v * 0x01010101
	}
	return v * 0x00010001
}

// narrowZeroFill reports whether addr selects the RP2350 +0x4000 alias and
// returns the register address it targets. Bit 14 is a narrow-write qualifier
// on APB/AHB (datasheet 2.1.5): the addressed lanes carry the value and the
// remaining lanes are driven to zero instead of being replicated across the
// 32-bit data bus.
func (c *Chip) narrowZeroFill(addr uint32) (uint32, bool) {
	if addr < PeriphBase || addr >= 0x60000000 || addr&0x4000 == 0 {
		return addr, false
	}
	norm := addr &^ 0x4000
	if c.regBlockFor(norm) != nil {
		return norm, true
	}
	if _, _, ok := c.aliasTarget(norm); ok {
		return norm, true
	}
	// Not a register alias. Keep the address so genuinely bit-14 targets such
	// as the OTP raw data view still decode, but zero-fill the data bus.
	return addr, true
}

func (c *Chip) writeMMIONarrow(addr uint32, v uint32, size int) {
	addr, zeroFill := c.narrowZeroFill(addr)
	shift := (addr & 3) * 8
	if size == 2 {
		shift = (addr & 2) * 8
	}
	mask := uint32(0xFF) << shift
	if size == 2 {
		mask = uint32(0xFFFF) << shift
	}
	// RP2350 IO registers sample the whole write bus, so SIO narrow writes
	// broadcast replicated data just like APB/AHB ones. CMSIS still needs the
	// lane-oriented merge for SCS (NVIC priority bytes).
	if addr >= SIOBase && addr < SIOBase+0x1000 {
		off := (addr - SIOBase) &^ 3
		c.sio.write(c, off, replicatedMMIO(v, size))
		return
	}
	if addr >= SCSBase && addr < SCSBase+0x1000 {
		off := (addr - SCSBase) &^ 3
		n := c.ppbForCore()
		old := n.peek(off)
		n.write(off, old&^mask|v<<shift)
		return
	}
	word := replicatedMMIO(v, size)
	if zeroFill {
		word = v << shift
	}
	if c.writeBootRAMAlias(addr, v, size) {
		return
	}
	if b := c.regBlockFor(addr); b != nil {
		if b.writeSizeFn != nil && b.writeSizeFn(c, addr, v, size) {
			return
		}
		off := (addr - b.base) &^ 3
		if b.byteAddressed {
			old := b.peek(off)
			b.write(c, off, old&^mask|v<<shift)
			return
		}
		b.write(c, off, word)
		return
	}
	if off, b, ok := c.aliasTarget(addr); ok {
		switch {
		case addr >= b.base+0x1000 && addr < b.base+0x2000: // XOR
			b.write(c, off, b.peek(off)^word)
		case addr >= b.base+0x2000 && addr < b.base+0x3000: // SET
			if b.setFn != nil {
				b.setFn(c, off, word)
			} else {
				b.write(c, off, b.peek(off)|word)
			}
		default: // CLR
			if b.clearFn != nil {
				b.clearFn(c, off, word)
			} else {
				b.write(c, off, b.peek(off)&^word)
			}
		}
		return
	}
	c.unhandledWrite(addr, v)
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
	var b strings.Builder
	for _, e := range list {
		fmt.Fprintf(&b, "%#08x x%d\n", e.addr, e.n)
	}
	return b.String()
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

// USBCDCReady reports whether another host-to-device CDC packet can be queued.
func (c *Chip) USBCDCReady() bool { return c.usb != nil && c.usb.CDCReady() }

// EnableUSBMIDI routes endpoint 3 to the MIDI class instead of the CDC
// console. It must be called before the firmware enumerates.
func (c *Chip) EnableUSBMIDI() {
	if c.usb != nil {
		c.usb.midi = true
	}
}

// SendUSBMIDI queues host-to-device USB-MIDI event packets (4 bytes each).
// It reports false while the device has not armed the MIDI OUT endpoint.
func (c *Chip) SendUSBMIDI(data []byte) bool {
	return c.usb != nil && c.usb.SendMIDI(data)
}

// USBMIDIReady reports whether the firmware has armed its MIDI OUT endpoint.
func (c *Chip) USBMIDIReady() bool { return c.usb != nil && c.usb.MIDIReady() }

// SetUSBMIDI installs the callback that receives USB-MIDI packets from the
// firmware's MIDI IN endpoint.
func (c *Chip) SetUSBMIDI(fn func([]byte)) { c.OnUSBMIDI = fn }

// SetUSBHID installs the callback that receives HID input reports from the
// firmware (keyboard, mouse, gamepad).
func (c *Chip) SetUSBHID(fn func(USBHIDEvent)) { c.OnUSBHID = fn }

// USBHIDInterfaces reports the HID interfaces the firmware enumerated.
func (c *Chip) USBHIDInterfaces() []USBHIDInterface {
	if c.usb == nil {
		return nil
	}
	out := make([]USBHIDInterface, 0, len(c.usb.hids))
	for i := range c.usb.hids {
		h := &c.usb.hids[i]
		kinds := h.kinds()
		names := make([]string, len(kinds))
		for j, k := range kinds {
			names[j] = string(k)
		}
		out = append(out, USBHIDInterface{
			Interface:        h.number,
			Kinds:            names,
			Subclass:         h.subclass,
			Protocol:         h.protocol,
			InEndpoint:       h.inEP,
			OutEndpoint:      h.outEP,
			ReportLength:     h.reportLen,
			ReportID:         h.device.PrimaryReportID(),
			KeyboardReportID: h.device.ReportIDForKind(hidpkg.Keyboard),
			OutputReportID:   h.device.OutputReportID(),
		})
	}
	return out
}

// SendUSBHID publishes one host-to-device HID report (keyboard LEDs or gamepad
// feedback) on the first HID OUT endpoint.
func (c *Chip) SendUSBHID(data []byte) bool { return c.usb != nil && c.usb.SendHID(data) }

// USBHIDReady reports whether a HID OUT endpoint is armed.
func (c *Chip) USBHIDReady() bool { return c.usb != nil && c.usb.HIDReady() }

// AttachUART attaches an external device to a hardware UART. The device
// receives firmware TX bytes and can answer through SendUART.
func (c *Chip) AttachUART(idx int, dev UARTDevice) {
	if idx < 0 || idx >= len(c.uart) || c.uart[idx] == nil {
		return
	}
	c.uart[idx].dev = dev
}

// SendUART delivers host bytes to a UART receive FIFO (0 or 1).
func (c *Chip) SendUART(idx int, data []byte) {
	if idx < 0 || idx >= len(c.uart) || c.uart[idx] == nil {
		return
	}
	c.uart[idx].send(data)
}

// NowMicros returns the current virtual time in microseconds.
func (c *Chip) NowMicros() uint64 { return c.Cycles / cyclesPerMicrosecond }
