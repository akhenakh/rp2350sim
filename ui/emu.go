package main

import (
	"fmt"
	"image"
	"strings"
	"sync"
	"time"

	"image/color"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/firmware"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
	"go.hasen.dev/shirei"
)

// PinInfo is the UI-facing view of one GPIO pin.
type PinInfo struct {
	Func     uint8
	OE       bool
	Level    bool
	PullUp   bool
	PullDown bool
}

// MachineState is a consistent copy of the machine state for one UI frame.
type MachineState struct {
	Pins       [48]PinInfo
	Cycles     uint64
	PC, SP     uint32
	Halted     bool
	Running    bool
	Firmware   string
	Err        string
	LCD        *image.RGBA
	Console    string
	ConsolePos int64
	WSColors   []color.RGBA
	WSFrames   uint64
	WSEnabled  bool
	EthKind    string
	EthLog     []string
}

// Emu owns the emulated machine and steps it in the background.
type Emu struct {
	mu         sync.Mutex
	chip       *rp2350.Chip
	lcd        *devices.ST7789
	img        *firmware.Image
	fwPath     string
	errText    string
	running    bool
	console    []byte
	consolePos int64

	lcdGen   int
	lcdFrame *image.RGBA

	board *BoardDef

	ws      *devices.WS2812B
	ethKind string
	ethLog  []string

	scopePins  []int
	scope      *devices.Trace
	logic      *devices.Trace
	signals    []*devices.SignalSource
	usbEvents  []USBEvent
	uartEvents []UARTEvent
	spiEvents  []SPIEvent
	i2cEvents  []I2CEvent
}

// UARTEvent is one byte written to (or received from) a hardware UART.
type UARTEvent struct {
	Bus   int
	Cycle uint64
	In    bool // received by the firmware
	Data  byte
}

// SPIEvent is one byte clocked through a hardware SPI controller.
type SPIEvent struct {
	Bus   int
	Cycle uint64
	TX    byte
	RX    byte
}

// I2CEvent is one phase of a hardware I2C transfer.
type I2CEvent struct {
	Bus   int
	Cycle uint64
	Kind  string
	Addr  uint8
	Data  byte
	Ack   bool
}

// USBEvent is one CDC transfer, host-to-device or device-to-host.
type USBEvent struct {
	Cycle uint64
	In    bool // device -> host
	Data  []byte
}

func NewEmu() *Emu {
	e := &Emu{}
	e.mu.Lock()
	e.resetChip()
	e.mu.Unlock()
	go e.loop()
	return e
}

// SetBoard selects the board whose devices are attached on the next boot.
func (e *Emu) SetBoard(b *BoardDef) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.board = b
	e.errText = ""
	if b != nil {
		e.scopePins = b.ScopePins()
	}
	e.loadLocked()
}

// SetScopePins changes the scope channels without resetting the machine.
func (e *Emu) SetScopePins(pins []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scopePins = append([]int(nil), pins...)
	if e.scope != nil {
		e.scope.SetPins(pins)
	}
}

// ScopeData copies the scope trace for rendering.
func (e *Emu) ScopeData() ([]int, []devices.TraceSample, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scope == nil {
		return nil, nil, 0
	}
	pins, samples := e.scope.Snapshot()
	return pins, samples, e.scope.Interval
}

// LogicData copies the logic-analyzer trace for rendering and decoding.
func (e *Emu) LogicData() ([]int, []devices.TraceSample, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.logic == nil {
		return nil, nil, 0
	}
	pins, samples := e.logic.Snapshot()
	return pins, samples, e.logic.Interval
}

// USBEvents returns the recent CDC transfers.
func (e *Emu) USBEvents() []USBEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]USBEvent(nil), e.usbEvents...)
}

// UARTEvents returns the recent hardware UART bytes.
func (e *Emu) UARTEvents() []UARTEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]UARTEvent(nil), e.uartEvents...)
}

// SPIEvents returns the recent hardware SPI bytes.
func (e *Emu) SPIEvents() []SPIEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]SPIEvent(nil), e.spiEvents...)
}

// I2CEvents returns the recent hardware I2C phases.
func (e *Emu) I2CEvents() []I2CEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]I2CEvent(nil), e.i2cEvents...)
}

func (e *Emu) addUART(bus int, cycle uint64, in bool, b byte) {
	e.uartEvents = append(e.uartEvents, UARTEvent{Bus: bus, Cycle: cycle, In: in, Data: b})
	if len(e.uartEvents) > 512 {
		e.uartEvents = e.uartEvents[len(e.uartEvents)-256:]
	}
}

func (e *Emu) addSPI(bus int, cycle uint64, tx, rx byte) {
	e.spiEvents = append(e.spiEvents, SPIEvent{Bus: bus, Cycle: cycle, TX: tx, RX: rx})
	if len(e.spiEvents) > 512 {
		e.spiEvents = e.spiEvents[len(e.spiEvents)-256:]
	}
}

func (e *Emu) addI2C(bus int, cycle uint64, kind string, addr uint8, data byte, ack bool) {
	e.i2cEvents = append(e.i2cEvents, I2CEvent{Bus: bus, Cycle: cycle, Kind: kind, Addr: addr, Data: data, Ack: ack})
	if len(e.i2cEvents) > 512 {
		e.i2cEvents = e.i2cEvents[len(e.i2cEvents)-256:]
	}
}

func (e *Emu) addUSBEvent(cycle uint64, in bool, data []byte) {
	if len(data) > 64 {
		data = data[:64]
	}
	e.usbEvents = append(e.usbEvents, USBEvent{Cycle: cycle, In: in, Data: append([]byte(nil), data...)})
	if len(e.usbEvents) > 64 {
		e.usbEvents = e.usbEvents[len(e.usbEvents)-32:]
	}
}

// resetChip builds a fresh machine and attaches the selected board's devices.
func (e *Emu) resetChip() {
	c := rp2350.New()
	e.lcd = nil
	e.ws = nil
	e.ethKind = ""
	e.ethLog = e.ethLog[:0]
	e.usbEvents = e.usbEvents[:0]
	e.uartEvents = e.uartEvents[:0]
	e.spiEvents = e.spiEvents[:0]
	e.i2cEvents = e.i2cEvents[:0]
	e.scope = nil
	e.logic = nil
	e.signals = e.signals[:0]
	logEth := func(format string, args ...any) {
		e.ethLog = append(e.ethLog, fmt.Sprintf(format, args...))
		if len(e.ethLog) > 64 {
			e.ethLog = e.ethLog[len(e.ethLog)-32:]
		}
	}

	if b := e.board; b != nil {
		recordSPI := func(bus int, dev func(byte) byte) func(byte) byte {
			return func(tx byte) byte {
				rx := byte(0xFF)
				if dev != nil {
					rx = dev(tx)
				}
				e.addSPI(bus, c.Cycles, tx, rx)
				return rx
			}
		}
		var spiAttached [2]bool
		if d := b.LCD; d != nil {
			e.lcd = devices.NewST7789(c, d.CS, d.DC, d.RST)
			if d.SPI == 1 {
				c.OnSPI1 = recordSPI(1, e.lcd.Transfer)
				spiAttached[1] = true
			} else {
				c.OnSPI0 = recordSPI(0, e.lcd.Transfer)
				spiAttached[0] = true
			}
		}
		if d := b.Ethernet; d != nil {
			e.ethKind = d.Kind
			var transfer func(byte) byte
			var tick func()
			if d.Kind == "w6300" {
				eth := devices.NewW6300(c, d.CS)
				eth.Logf = logEth
				transfer, tick = eth.Transfer, eth.Tick
			} else {
				eth := devices.NewW5500(c, d.CS)
				eth.Logf = logEth
				transfer, tick = eth.Transfer, eth.Tick
			}
			if d.SPI == 1 {
				c.OnSPI1 = recordSPI(1, transfer)
				spiAttached[1] = true
			} else {
				c.OnSPI0 = recordSPI(0, transfer)
				spiAttached[0] = true
			}
			c.CycleHooks = append(c.CycleHooks, tick)
		}
		if d := b.WS2812; d != nil {
			e.ws = devices.NewWS2812B(c, d.Pin)
			c.CycleHooks = append(c.CycleHooks, e.ws.Tick)
		}

		for _, sd := range b.Analog {
			wave := devices.WaveSine
			switch sd.Wave {
			case "square":
				wave = devices.WaveSquare
			case "triangle":
				wave = devices.WaveTriangle
			}
			hz := sd.Hz
			if hz <= 0 {
				hz = 1000
			}
			amp, off := sd.Amplitude, sd.Offset
			if amp == 0 && off == 0 {
				amp, off = 1.65, 1.65
			}
			src := devices.NewSignalSource(c, sd.Pin, wave, hz, amp, off)
			e.signals = append(e.signals, src)
			c.CycleHooks = append(c.CycleHooks, src.Tick)
		}

		scopePins := e.scopePins
		if len(scopePins) == 0 {
			scopePins = b.ScopePins()
		}
		e.scope = devices.NewTrace(c, scopePins, devices.ScopeInterval, devices.ScopeDepth)
		c.CycleHooks = append(c.CycleHooks, e.scope.Tick)

		if pins := b.LogicPins(); len(pins) > 0 {
			e.logic = devices.NewTrace(c, pins, devices.LogicInterval, devices.LogicDepth)
			c.CycleHooks = append(c.CycleHooks, e.logic.Tick)
		}
		// A protocol without an attached chip still records bytes clocked
		// by firmware through that SPI controller.
		if l := b.Logic; l != nil && l.SPI != nil {
			bus := l.SPI.Bus
			if bus >= 0 && bus < 2 && !spiAttached[bus] {
				if bus == 1 {
					c.OnSPI1 = recordSPI(1, nil)
				} else {
					c.OnSPI0 = recordSPI(0, nil)
				}
			}
		}
		c.OnI2C = func(bus int, cycles uint64, kind string, addr uint8, data byte, ack bool) {
			e.addI2C(bus, cycles, kind, addr, data, ack)
		}
	}

	appendConsole := func(b []byte) {
		e.console = append(e.console, b...)
		e.consolePos += int64(len(b))
		// Keep the tail; the UI resets its view when the length drops.
		if len(e.console) > 64*1024 {
			e.console = append([]byte(nil), e.console[len(e.console)-32*1024:]...)
		}
	}
	c.OnUART0 = func(b byte) {
		appendConsole([]byte{b})
		e.addUART(0, c.Cycles, false, b)
	}
	c.OnUART1 = func(b byte) {
		appendConsole([]byte{b})
		e.addUART(1, c.Cycles, false, b)
	}
	c.SetUSBCDC(func(b []byte) {
		appendConsole(b)
		e.addUSBEvent(c.Cycles, true, b)
	})

	e.chip = c
	e.lcdGen = 0
	e.lcdFrame = nil
}

func (e *Emu) loadLocked() {
	e.resetChip()
	if e.img == nil {
		return
	}
	for _, seg := range e.img.Segments {
		e.chip.LoadSegment(seg.Addr, seg.Data)
	}
	e.chip.Boot()
	e.console = e.console[:0]
	e.consolePos = 0
}

// Load reads a firmware image and starts running it.
func (e *Emu) Load(path string) error {
	img, err := firmware.Load(path)
	if err != nil {
		e.mu.Lock()
		e.errText = err.Error()
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.img = img
	e.fwPath = path
	e.errText = ""
	e.loadLocked()
	e.running = true
	return nil
}

// RunFor synchronously advances the machine by a number of cycles. It is used
// by --png snapshots to render a meaningful frame without a live window.
func (e *Emu) RunFor(cycles uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	const chunk = 500_000
	for cycles > 0 && !e.chip.Halted {
		n := uint64(chunk)
		if n > cycles {
			n = cycles
		}
		e.chip.Run(n)
		cycles -= n
	}
}

// Reset restarts the loaded firmware from its initial state.
func (e *Emu) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loadLocked()
}

// SetRunning starts or pauses execution.
func (e *Emu) SetRunning(run bool) {
	e.mu.Lock()
	e.running = run
	e.mu.Unlock()
}

// ToggleRunning flips between run and pause and returns the new state.
func (e *Emu) ToggleRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.running = !e.running
	return e.running
}

func (e *Emu) loop() {
	// Small chunks keep the mutex hold time low enough for a smooth UI while
	// still running the firmware as fast as the interpreter allows.
	const chunk = 150_000
	for {
		time.Sleep(200 * time.Microsecond)
		if shirei.GetHost().HeadlessRender {
			continue // keep snapshots deterministic
		}
		e.mu.Lock()
		// The picker screen has no board yet; keep the machine idle so an
		// empty reset vector is not executed in the background.
		running := e.running && e.board != nil && e.chip != nil && !e.chip.Halted
		if running {
			e.chip.Run(chunk)
		}
		e.mu.Unlock()
		if running {
			shirei.RequestNextFrame()
		}
	}
}

// Snapshot returns a consistent view of the machine for one frame.
func (e *Emu) Snapshot() MachineState {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s MachineState
	s.Cycles = e.chip.Cycles
	s.PC = e.chip.CPU0.R[15]
	s.SP = e.chip.CPU0.R[13]
	s.Halted = e.chip.Halted
	s.Running = e.running
	s.Firmware = e.fwPath
	s.Err = e.errText
	for i := 0; i < 48; i++ {
		s.Pins[i] = PinInfo{
			Func:     e.chip.Pins.Func[i],
			OE:       e.chip.Pins.OE&(1<<uint(i)) != 0,
			Level:    e.chip.Pins.Level(i),
			PullUp:   e.chip.Pins.PullUp[i],
			PullDown: e.chip.Pins.PullDown[i],
		}
	}
	if e.lcd != nil {
		if g := e.lcd.PixelsWritten(); g != e.lcdGen {
			e.lcdFrame = e.lcd.Framebuffer()
			e.lcdGen = g
		}
		s.LCD = e.lcdFrame
	}
	if len(e.console) > 0 {
		start := len(e.console) - 16*1024
		if start < 0 {
			start = 0
		}
		s.Console = string(e.console[start:])
	}
	s.ConsolePos = e.consolePos
	if e.ws != nil {
		s.WSEnabled = true
		s.WSColors = e.ws.Colors()
		s.WSFrames = e.ws.Frames()
	}
	s.EthKind = strings.ToUpper(e.ethKind)
	s.EthLog = append([]string(nil), e.ethLog...)
	return s
}

// SendInput forwards host bytes to the USB CDC OUT endpoint and UART0.
func (e *Emu) SendInput(data []byte) {
	e.mu.Lock()
	e.chip.SendUSBCDC(data)
	e.chip.SendUART(0, data)
	e.addUSBEvent(e.chip.Cycles, false, data)
	for _, b := range data {
		e.addUART(0, e.chip.Cycles, true, b)
	}
	e.mu.Unlock()
}
