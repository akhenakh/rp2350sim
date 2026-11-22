package main

import (
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"image/color"

	"github.com/akhenakh/rp2350sim/internal/debug"
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
	Pins         [48]PinInfo
	Cycles       uint64
	PC, SP       uint32
	Halted       bool
	Running      bool
	Firmware     string
	FlashPath    string
	Boots        uint64
	FlashWrites  uint64
	BootEntry    uint32
	ResetReason  string
	PSRAMBytes   uint32
	PSRAMLatency uint32
	Err          string
	LCD          *image.RGBA
	Console      string
	ConsolePos   int64
	WSColors     []color.RGBA
	WSFrames     uint64
	WSEnabled    bool
	EthKind      string
	Network      *devices.NetworkInfo
	EthLog       []string
	Serial       []string
	Buses        []string

	Touch     *TouchState
	IMU       *IMUState
	Battery   *BatteryState
	Backlight *BacklightState
	SD        *SDState
	OTP       *OTPState
}

// TouchState is the last reported touch point in panel coordinates.
type TouchState struct {
	X, Y    int
	Pressed bool
	Gesture byte
}

// IMUState is the QMI8658 sample shown in the Sensors tab.
type IMUState struct {
	AX, AY, AZ float32 // g
	GX, GY, GZ float32 // deg/s
	Temp       float32
}

// BatteryState is the battery monitor reading.
type BatteryState struct {
	Volts float32
	Raw   uint16
}

// BacklightState is the LCD backlight drive level.
type BacklightState struct {
	Duty float32
}

// SDState summarises the attached card.
type SDState struct {
	Inserted bool
	Blocks   int
	Source   string
}

// OTPPageState is one OTP page as the UI shows it.
type OTPPageState struct {
	Secure         uint8 // 0 read/write, 1 read-only, 2 inaccessible
	NonSecure      uint8
	Programmed     int
	SoftwareLocked bool
}

// OTPState is the whole OTP array plus its page locks. The array is 16 KB,
// small enough to copy per frame.
type OTPState struct {
	Rows       []uint32
	Pages      [rp2350.OTPPages]OTPPageState
	Writes     uint64
	Programmed int
	LastWrite  int
}

// Emu owns the emulated machine and steps it in the background.
type Emu struct {
	mu        sync.Mutex
	chip      *rp2350.Chip
	lcd       *devices.ST7789
	img       *firmware.Image
	debugInfo *debug.Info
	debugger  *rp2350.Debugger
	fwPath    string
	flashPath string
	lastBoots uint64
	// bootCount and flashWrites are cumulative across UI resets, which
	// recreate the chip and therefore restart its own counters.
	bootCount   uint64
	flashWrites uint64
	lastChipFW  uint64
	errText     string
	running     bool
	console     []byte
	consolePos  int64

	lcdGen   int
	lcdFrame *image.RGBA
	lcdDuty  float32
	lcdOn    bool

	board *BoardDef

	ws      *devices.WS2812B
	touch   *devices.CST816D
	imu     *devices.QMI8658
	sd      *devices.SDCard
	blPin   int
	ethKind string
	ethLog  panelLog
	w5500   *devices.W5500
	w6300   *devices.W6300
	ch9120  *devices.CH9120

	scopePins  []int
	scope      *devices.Trace
	logic      *devices.Trace
	signals    []*devices.SignalSource
	usbEvents  []USBEvent
	uartEvents []UARTEvent
	spiEvents  []SPIEvent
	i2cEvents  []I2CEvent

	trigger LogicTrigger
	frozen  frozenLogic

	bridges    []*devices.UARTBridge
	bridgeErrs []string
	serialSig  string

	i2cBuses  []*devices.I2CBridge
	spiBuses  []*devices.SPIBridge
	busAll    []*devices.BusBridge
	busErrors []string
	busSig    string
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

// LogicTrigger arms a stop condition for the logic analyzer. Kind is "",
// "any", "uart", "i2c" or "spi"; Pause also pauses the machine.
type LogicTrigger struct {
	Kind  string
	Pause bool
}

type frozenLogic struct {
	triggered bool
	info      string
	pins      []int
	samples   []devices.TraceSample
	uart      []UARTEvent
	spi       []SPIEvent
	i2c       []I2CEvent
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
	e := &Emu{debugger: rp2350.NewDebugger()}
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
	if e.img != nil || e.fwPath != "" {
		e.rebootLocked()
	} else {
		e.loadLocked()
	}
}

// SetTouch injects a touch point in panel coordinates (the CST816D's own
// 240x320 frame, not the rotated display frame).
func (e *Emu) SetTouch(x, y int, pressed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.touch != nil {
		e.touch.SetTouch(x, y, pressed)
	}
}

// Booted reports whether a firmware has advanced past the driver-init window,
// so host-injected touches reach a controller the firmware has configured.
func (e *Emu) Booted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chip != nil && e.chip.Cycles >= touchBootCycles
}

// TouchAvailable reports whether the selected board has a touch controller.
func (e *Emu) TouchAvailable() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.touch != nil
}

// SetLogicTrigger arms or disarms the analyzer trigger and clears any frozen
// capture.
func (e *Emu) SetLogicTrigger(t LogicTrigger) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.trigger = t
	e.frozen = frozenLogic{}
}

// RearmLogic clears a frozen capture and waits for the next trigger event.
func (e *Emu) RearmLogic() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.frozen = frozenLogic{}
}

// LogicTriggered reports whether a capture is frozen and what caused it.
func (e *Emu) LogicTriggered() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frozen.triggered, e.frozen.info
}

// checkTriggerLocked captures the trace and event rings on a match. The
// caller holds e.mu.
func (e *Emu) checkTriggerLocked(kind, info string) {
	if e.frozen.triggered || e.trigger.Kind == "" {
		return
	}
	if e.trigger.Kind != "any" && e.trigger.Kind != kind {
		return
	}
	e.frozen.triggered = true
	e.frozen.info = info
	if e.logic != nil {
		e.frozen.pins, e.frozen.samples = e.logic.Snapshot()
	}
	e.frozen.uart = append([]UARTEvent(nil), e.uartEvents...)
	e.frozen.spi = append([]SPIEvent(nil), e.spiEvents...)
	e.frozen.i2c = append([]I2CEvent(nil), e.i2cEvents...)
	if e.trigger.Pause {
		e.running = false
	}
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
	if e.frozen.triggered {
		interval := uint64(devices.LogicInterval)
		if e.logic != nil {
			interval = e.logic.Interval
		}
		return e.frozen.pins, e.frozen.samples, interval
	}
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
	if e.frozen.triggered {
		return append([]UARTEvent(nil), e.frozen.uart...)
	}
	return append([]UARTEvent(nil), e.uartEvents...)
}

// SPIEvents returns the recent hardware SPI bytes.
func (e *Emu) SPIEvents() []SPIEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frozen.triggered {
		return append([]SPIEvent(nil), e.frozen.spi...)
	}
	return append([]SPIEvent(nil), e.spiEvents...)
}

// I2CEvents returns the recent hardware I2C phases.
func (e *Emu) I2CEvents() []I2CEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frozen.triggered {
		return append([]I2CEvent(nil), e.frozen.i2c...)
	}
	return append([]I2CEvent(nil), e.i2cEvents...)
}

func (e *Emu) addUART(bus int, cycle uint64, in bool, b byte) {
	e.uartEvents = append(e.uartEvents, UARTEvent{Bus: bus, Cycle: cycle, In: in, Data: b})
	if len(e.uartEvents) > 512 {
		e.uartEvents = e.uartEvents[len(e.uartEvents)-256:]
	}
	if !in {
		e.checkTriggerLocked("uart", fmt.Sprintf("UART TX 0x%02x", b))
	}
}

func (e *Emu) addSPI(bus int, cycle uint64, tx, rx byte) {
	e.spiEvents = append(e.spiEvents, SPIEvent{Bus: bus, Cycle: cycle, TX: tx, RX: rx})
	if len(e.spiEvents) > 512 {
		e.spiEvents = e.spiEvents[len(e.spiEvents)-256:]
	}
	e.checkTriggerLocked("spi", fmt.Sprintf("SPI MOSI 0x%02x", tx))
}

func (e *Emu) addI2C(bus int, cycle uint64, kind string, addr uint8, data byte, ack bool) {
	e.i2cEvents = append(e.i2cEvents, I2CEvent{Bus: bus, Cycle: cycle, Kind: kind, Addr: addr, Data: data, Ack: ack})
	if len(e.i2cEvents) > 512 {
		e.i2cEvents = e.i2cEvents[len(e.i2cEvents)-256:]
	}
	info := fmt.Sprintf("I2C %s", kind)
	if kind == "addr" || kind == "data" {
		info = fmt.Sprintf("I2C %s 0x%02x", kind, data)
	}
	e.checkTriggerLocked("i2c", info)
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
	e.debugger.Reset()
	c.Debug = e.debugger
	if e.sd != nil {
		_ = e.sd.Sync()
	}
	e.lcd = nil
	e.ws = nil
	e.touch = nil
	e.imu = nil
	e.sd = nil
	e.blPin = -1
	e.ch9120 = nil
	e.ethKind = ""
	e.w5500 = nil
	e.w6300 = nil
	e.ethLog.reset()
	e.frozen = frozenLogic{}
	e.usbEvents = e.usbEvents[:0]
	e.uartEvents = e.uartEvents[:0]
	e.spiEvents = e.spiEvents[:0]
	e.i2cEvents = e.i2cEvents[:0]
	e.scope = nil
	e.logic = nil
	e.signals = e.signals[:0]
	logEth := e.ethLog.logger()

	if b := e.board; b != nil {
		if p := b.PSRAM; p != nil {
			c.ConfigurePSRAM(uint32(p.SizeMB)<<20, uint32(p.LatencyCycles()), p.CS)
		}
		type spiClient struct {
			cs int
			fn func(byte) byte
		}
		var spiClients [2][]spiClient
		attachSPI := func(bus, cs int, fn func(byte) byte) {
			if bus != 0 && bus != 1 {
				return
			}
			spiClients[bus] = append(spiClients[bus], spiClient{cs: cs, fn: fn})
		}

		if d := b.LCD; d != nil {
			// boards.yaml carries the logical (landscape) size; the panel's
			// native address space is the swap for a 90/270 rotation.
			w, h := d.Width, d.Height
			if d.Rotation == 90 || d.Rotation == 270 {
				w, h = h, w
			}
			e.lcd = devices.NewST7789(c, d.CS, d.DC, d.RST, w, h)
			e.blPin = d.BacklightPin()
			attachSPI(d.SPI, d.CS, e.lcd.Transfer)
		}
		if d := b.Touch; d != nil {
			e.touch = devices.NewCST816D(c, uint8(d.Addr), d.RSTPin(), d.INT)
			e.touch.Width, e.touch.Height = d.Width, d.Height
			e.touch.TouchRotate = d.Rotation
			c.AttachI2C(d.Bus, uint8(d.Addr), e.touch)
			c.CycleHooks = append(c.CycleHooks, e.touch.Tick)
		}
		if d := b.IMU; d != nil {
			e.imu = devices.NewQMI8658(c, uint8(d.Addr), d.INT, d.INT2Pin())
			c.AttachI2C(d.Bus, uint8(d.Addr), e.imu)
			c.CycleHooks = append(c.CycleHooks, e.imu.Tick)
		}
		if d := b.Battery; d != nil {
			c.Pins.SetAnalog(d.Pin, float32(d.Volts/d.Divider))
		}
		if d := b.SD; d != nil {
			card, err := devices.NewSDCard(c, d.CS, d.Image, d.Dir)
			if err != nil {
				e.errText = err.Error()
			} else {
				e.sd = card
				attachSPI(d.SPI, d.CS, card.Transfer)
				c.CycleHooks = append(c.CycleHooks, card.Tick)
			}
		}
		if w := b.Wifi; w != nil {
			ch := devices.NewCH9120(c, w.UART, w.CFG0, w.RST, w.TCPCS)
			ch.Logger = logEth
			ch.OnByte = func(v byte) {
				e.addUART(w.UART, c.Cycles, false, v)
			}
			e.ch9120 = ch
			e.ethKind = "wifi"
		}
		if d := b.Ethernet; d != nil {
			e.ethKind = d.Kind
			if d.Kind == "ch9120" {
				ch := devices.NewCH9120(c, d.UART, d.CFG0, d.RST, d.TCPCS)
				ch.Logger = logEth
				ch.OnByte = func(v byte) {
					e.addUART(d.UART, c.Cycles, false, v)
				}
				e.ch9120 = ch
			} else {
				var transfer func(byte) byte
				var tick func()
				if d.Kind == "w6300" {
					eth := devices.NewW6300(c, d.CS)
					eth.Logger = logEth
					e.w6300 = eth
					transfer, tick = eth.Transfer, eth.Tick
				} else {
					eth := devices.NewW5500(c, d.CS)
					eth.Logger = logEth
					e.w5500 = eth
					transfer, tick = eth.Transfer, eth.Tick
				}
				attachSPI(d.SPI, d.CS, transfer)
				c.CycleHooks = append(c.CycleHooks, tick)
			}
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

		e.attachBus(c, b, attachSPI)

		// A protocol without an attached chip still records bytes clocked by
		// firmware through that SPI controller.
		spiFallback := [2]bool{}
		if l := b.Logic; l != nil && l.SPI != nil {
			bus := l.SPI.Bus
			if (bus == 0 || bus == 1) && len(spiClients[bus]) == 0 {
				spiFallback[bus] = true
			}
		}
		for bus := range 2 {
			if len(spiClients[bus]) == 0 && !spiFallback[bus] {
				continue
			}
			clients := spiClients[bus]
			handler := func(tx byte) byte {
				rx := byte(0xFF)
				for _, cl := range clients {
					if cl.cs >= 0 && c.Pins.Level(cl.cs) {
						continue
					}
					rx = cl.fn(tx)
					break
				}
				e.addSPI(bus, c.Cycles, tx, rx)
				return rx
			}
			if bus == 1 {
				c.OnSPI1 = handler
			} else {
				c.OnSPI0 = handler
			}
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

	// The module owns UART1, so it must install its hook after the console
	// callbacks above.
	if e.ch9120 != nil {
		e.ch9120.Attach()
	}

	e.attachSerial(c, e.board)

	e.chip = c
	e.lcdGen = 0
	e.lcdFrame = nil
	e.lcdDuty = -1
	e.lcdOn = false
}

// attachBus starts the Bus Pirate TCP bridges and reattaches them to a new
// chip. Bridges survive firmware reloads when the board config is unchanged,
// so the listener port and any connected device script stay alive.
func (e *Emu) attachBus(c *rp2350.Chip, b *BoardDef, attachSPI func(bus, cs int, fn func(byte) byte)) {
	sig := ""
	var i2cDefs []I2CTCPBridge
	var spiDefs []SPITCPBridge
	if b != nil {
		i2cDefs = b.I2CTCP
		spiDefs = b.SPITCP
		sig = fmt.Sprintf("%+v|%+v", i2cDefs, spiDefs)
	}
	if sig != e.busSig || len(e.busAll) != len(i2cDefs)+len(spiDefs) {
		for _, br := range e.busAll {
			br.Close()
		}
		e.busAll = e.busAll[:0]
		e.i2cBuses = e.i2cBuses[:0]
		e.spiBuses = e.spiBuses[:0]
		e.busErrors = e.busErrors[:0]
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		for _, d := range i2cDefs {
			listen, connect := d.Listen, d.Connect
			if listen == "" && connect == "" {
				listen = "127.0.0.1:0"
			}
			addrs := make([]uint8, len(d.Addrs))
			for i, a := range d.Addrs {
				addrs[i] = uint8(a)
			}
			br := devices.NewI2CBridge(d.Bus, addrs, listen, connect)
			br.Logger = logger
			br.Timeout = 500 * time.Millisecond
			br.Wait = 0
			if err := br.Start(); err != nil {
				e.busErrors = append(e.busErrors, fmt.Sprintf("I2C%d %s: %v", d.Bus, listen, err))
				continue
			}
			e.i2cBuses = append(e.i2cBuses, br)
			e.busAll = append(e.busAll, br.BusBridge)
		}
		for _, d := range spiDefs {
			listen, connect := d.Listen, d.Connect
			if listen == "" && connect == "" {
				listen = "127.0.0.1:0"
			}
			br := devices.NewSPIBridge(d.Bus, d.CS, listen, connect)
			br.Logger = logger
			br.Timeout = 500 * time.Millisecond
			br.Wait = 0
			if err := br.Start(); err != nil {
				e.busErrors = append(e.busErrors, fmt.Sprintf("SPI%d CS%d %s: %v", d.Bus, d.CS, listen, err))
				continue
			}
			e.spiBuses = append(e.spiBuses, br)
			e.busAll = append(e.busAll, br.BusBridge)
		}
		e.busSig = sig
	}
	for _, br := range e.i2cBuses {
		for _, addr := range br.Addrs {
			c.AttachI2C(br.Bus, addr, br)
		}
	}
	for _, br := range e.spiBuses {
		attachSPI(br.Bus, br.CS, br.Transfer)
	}
	if len(e.spiBuses) > 0 {
		prev := c.Pins.OnChange
		c.Pins.OnChange = func(pin int, level bool) {
			if prev != nil {
				prev(pin, level)
			}
			for _, br := range e.spiBuses {
				br.SetCS(pin, level)
			}
		}
	}
}

// BusBridgeInfo is the UI view of one I2C/SPI TCP bridge.
type BusBridgeInfo struct {
	Kind      string
	Bus       int
	Addrs     []uint8
	CS        int
	Listen    string
	Connect   string
	Client    string
	Connected bool
}

// BusBridges returns the configured bridge endpoints for the Bus tab.
func (e *Emu) BusBridges() []BusBridgeInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []BusBridgeInfo
	for _, br := range e.i2cBuses {
		listen, connect, client, connected := br.Status()
		out = append(out, BusBridgeInfo{
			Kind: "I2C", Bus: br.Bus, Addrs: append([]uint8(nil), br.Addrs...),
			Listen: listen, Connect: connect, Client: client, Connected: connected,
		})
	}
	for _, br := range e.spiBuses {
		listen, connect, client, connected := br.Status()
		out = append(out, BusBridgeInfo{
			Kind: "SPI", Bus: br.Bus, CS: br.CS,
			Listen: listen, Connect: connect, Client: client, Connected: connected,
		})
	}
	return out
}

// attachSerial keeps the TCP bridges across chip resets when the board's
// serial configuration is unchanged, so the listening port is stable; the
// client connection survives a firmware reload. New UART callbacks are always
// wrapped with the current bridges.
func (e *Emu) attachSerial(c *rp2350.Chip, b *BoardDef) {
	sig := ""
	if b != nil {
		sig = fmt.Sprintf("%+v", b.Serial)
	}
	if sig != e.serialSig || (b != nil && len(e.bridges) != len(b.Serial)) {
		for _, br := range e.bridges {
			br.Close()
		}
		e.bridges = e.bridges[:0]
		e.bridgeErrs = e.bridgeErrs[:0]
		if b != nil {
			for _, sd := range b.Serial {
				br := devices.NewUARTBridge(c, sd.UART, sd.Listen)
				br.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
				if err := br.Start(); err != nil {
					e.bridgeErrs = append(e.bridgeErrs, fmt.Sprintf("UART%d %s: %v", sd.UART, sd.Listen, err))
				}
				e.bridges = append(e.bridges, br)
			}
		}
		e.serialSig = sig
	} else {
		for _, br := range e.bridges {
			br.SetChip(c)
		}
	}

	if b == nil {
		return
	}
	for i, sd := range b.Serial {
		if i >= len(e.bridges) {
			break
		}
		br := e.bridges[i]
		if sd.UART == 0 {
			prev := c.OnUART0
			c.OnUART0 = func(v byte) {
				if prev != nil {
					prev(v)
				}
				br.Write([]byte{v})
			}
		} else {
			prev := c.OnUART1
			c.OnUART1 = func(v byte) {
				if prev != nil {
					prev(v)
				}
				br.Write([]byte{v})
			}
		}
	}
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
	e.lastBoots = e.chip.BootCount()
	e.bootCount = e.lastBoots
	e.flashWrites = 0
	e.lastChipFW = e.chip.FlashWrites()
	e.console = e.console[:0]
	e.consolePos = 0
}

// rebootLocked restarts the current flash image, preserving any self-update.
func (e *Emu) rebootLocked() {
	var flash []byte
	if e.chip != nil {
		flash = e.chip.FlashCopy()
	}
	e.syncFlashStatsLocked()
	e.resetChip()
	if flash != nil {
		copy(e.chip.Flash, flash)
	}
	e.chip.Boot()
	e.lastBoots = e.chip.BootCount()
	e.bootCount++
	e.lastChipFW = e.chip.FlashWrites()
	e.console = e.console[:0]
	e.consolePos = 0
}

// syncFlashStatsLocked folds the current chip's flash-write count into the
// cumulative UI counter. A new chip starts at zero, so a lower value is a
// reset rather than a negative delta.
func (e *Emu) syncFlashStatsLocked() {
	if e.chip == nil {
		return
	}
	n := e.chip.FlashWrites()
	if n >= e.lastChipFW {
		e.flashWrites += n - e.lastChipFW
	} else {
		e.flashWrites += n
	}
	e.lastChipFW = n
}

// SetFlashPath sets the raw flash backing file used to persist self-updates.
func (e *Emu) SetFlashPath(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	e.mu.Lock()
	e.flashPath = path
	e.mu.Unlock()
}

// SaveFlash writes the current flash array to the configured backing file.
func (e *Emu) SaveFlash() {
	e.mu.Lock()
	path := e.flashPath
	if path == "" || e.chip == nil {
		e.mu.Unlock()
		return
	}
	data := e.chip.FlashCopy()
	e.mu.Unlock()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Error("flash save", slog.String("path", path), slog.Any("err", err))
	}
}

// SyncSD flushes a host-backed SD card so writes made just before the window
// closes are not lost.
func (e *Emu) SyncSD() {
	e.mu.Lock()
	sd := e.sd
	e.mu.Unlock()
	if sd != nil {
		_ = sd.Sync()
	}
}

// LoadFlash boots the vector table from a raw flash image without loading an
// ELF. It is used to resume after a previous self-update.
func (e *Emu) LoadFlash(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > rp2350.FlashSize {
		return fmt.Errorf("flash image is %d bytes, limit is %d", len(data), rp2350.FlashSize)
	}
	if len(data) < 8 {
		return fmt.Errorf("flash image is too small to hold a vector table")
	}
	head := append([]byte(nil), data[:8]...)
	check := &firmware.Image{Format: "flash", Segments: []firmware.Segment{{Addr: rp2350.FlashBase, Data: head}}}
	if err := check.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.img = nil
	e.debugInfo = nil
	e.fwPath = path
	e.errText = ""
	e.resetChip()
	e.chip.LoadFlashImage(data)
	e.chip.Boot()
	e.lastBoots = e.chip.BootCount()
	e.bootCount = e.lastBoots
	e.flashWrites = 0
	e.lastChipFW = e.chip.FlashWrites()
	e.console = e.console[:0]
	e.consolePos = 0
	e.running = true
	return nil
}

// Load reads a firmware image and starts running it.
func (e *Emu) Load(path string) error {
	img, err := firmware.Load(path)
	if err == nil {
		err = img.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize)
	}
	if err != nil {
		e.mu.Lock()
		e.errText = err.Error()
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.img = img
	e.debugInfo = nil
	if img.Format == "elf" {
		e.debugInfo, _ = debug.Load(path)
	}
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
	e.debugger.Resume()
	const chunk = 500_000
	for cycles > 0 && !e.chip.Halted && !e.debugger.Stopped {
		n := min(uint64(chunk), cycles)
		e.chip.Run(n)
		cycles -= n
	}
}

// Reset restarts the current flash image, preserving any self-update.
func (e *Emu) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rebootLocked()
}

// SetRunning starts or pauses execution.
func (e *Emu) SetRunning(run bool) {
	e.mu.Lock()
	e.running = run
	if run {
		e.debugger.Resume()
	} else if e.chip != nil {
		e.debugger.Pause(e.chip.CPU0.R[15], e.chip.Cycles)
	}
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
		boots := e.lastBoots
		if running {
			e.chip.Run(chunk)
			boots = e.chip.BootCount()
			if e.debugger.Stopped {
				e.running = false
			}
		}
		e.mu.Unlock()
		if running {
			shirei.RequestNextFrame()
		}
		if boots != e.lastBoots {
			if boots > e.lastBoots {
				e.bootCount += boots - e.lastBoots
			} else {
				e.bootCount++
			}
			e.lastBoots = boots
			e.SaveFlash()
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
	s.FlashPath = e.flashPath
	e.syncFlashStatsLocked()
	s.Boots = e.bootCount
	s.FlashWrites = e.flashWrites
	s.BootEntry = e.chip.BootEntry()
	s.ResetReason = e.chip.LastResetReason()
	s.PSRAMBytes = e.chip.PSRAMSize()
	s.PSRAMLatency = e.chip.PSRAMLatency()
	s.Err = e.errText
	for i := range 48 {
		s.Pins[i] = PinInfo{
			Func:     e.chip.Pins.Func[i],
			OE:       e.chip.Pins.OE&(1<<uint(i)) != 0,
			Level:    e.chip.Pins.Level(i),
			PullUp:   e.chip.Pins.PullUp[i],
			PullDown: e.chip.Pins.PullDown[i],
		}
	}
	if e.lcd != nil {
		duty := float32(1)
		if e.blPin >= 0 {
			duty = e.chip.PinBrightness(e.blPin)
		}
		on := e.lcd.On()
		if g := e.lcd.PixelsWritten(); g != e.lcdGen || duty != e.lcdDuty || on != e.lcdOn {
			e.lcd.Brightness = duty
			e.lcdFrame = e.lcd.Framebuffer()
			e.lcdGen = g
			e.lcdDuty = duty
			e.lcdOn = on
		}
		s.LCD = e.lcdFrame
		if e.blPin >= 0 {
			s.Backlight = &BacklightState{Duty: duty}
		}
	}
	if e.touch != nil {
		x, y, pressed := e.touch.Last()
		s.Touch = &TouchState{X: x, Y: y, Pressed: pressed, Gesture: e.touch.Gesture()}
	}
	if e.imu != nil {
		ax, ay, az := e.imu.Accel()
		gx, gy, gz := e.imu.Gyro()
		s.IMU = &IMUState{AX: ax, AY: ay, AZ: az, GX: gx, GY: gy, GZ: gz, Temp: e.imu.Temperature()}
	}
	if b := e.board; b != nil && b.Battery != nil {
		v := float32(b.Battery.Volts)
		raw := min(uint16(4095*(v/float32(b.Battery.Divider))/3.3), uint16(4095))
		s.Battery = &BatteryState{Volts: v, Raw: raw}
	}
	if e.sd != nil {
		s.SD = &SDState{Inserted: e.sd.Inserted(), Blocks: e.sd.Blocks(), Source: e.sd.Source()}
	}
	otp := &OTPState{
		Rows:      e.chip.OTPRowsCopy(),
		Writes:    e.chip.OTPWrites(),
		LastWrite: e.chip.OTPLastWriteRow(),
	}
	for i, raw := range otp.Rows {
		if raw == 0 {
			continue
		}
		otp.Programmed++
		otp.Pages[i/rp2350.OTPPageRows].Programmed++
	}
	for page := range otp.Pages {
		sec, nsec := e.chip.OTPPageLock(page)
		otp.Pages[page].Secure = sec
		otp.Pages[page].NonSecure = nsec
		otp.Pages[page].SoftwareLocked = e.chip.OTPSoftwareLocked(page)
	}
	s.OTP = otp
	if len(e.console) > 0 {
		start := max(len(e.console)-16*1024, 0)
		s.Console = string(e.console[start:])
	}
	s.ConsolePos = e.consolePos
	if e.ws != nil {
		s.WSEnabled = true
		s.WSColors = e.ws.Colors()
		s.WSFrames = e.ws.Frames()
	}
	s.EthKind = strings.ToUpper(e.ethKind)
	switch {
	case e.w5500 != nil:
		info := e.w5500.NetworkInfo()
		s.Network = &info
	case e.w6300 != nil:
		info := e.w6300.NetworkInfo()
		s.Network = &info
	case e.ch9120 != nil:
		info := e.ch9120.NetworkInfo()
		if e.ethKind == "wifi" {
			info.Kind = "Wi-Fi (virtual)"
		}
		s.Network = &info
	}
	s.EthLog = e.ethLog.snapshot()
	s.Serial = append([]string(nil), e.bridgeErrs...)
	for _, br := range e.bridges {
		listen, client, connected := br.Status()
		if connected {
			s.Serial = append(s.Serial, fmt.Sprintf("UART%d tcp %s ← %s", br.UART, listen, client))
		} else {
			s.Serial = append(s.Serial, fmt.Sprintf("UART%d tcp %s · listening", br.UART, listen))
		}
	}
	s.Buses = append([]string(nil), e.busErrors...)
	for _, br := range e.i2cBuses {
		listen, connect, client, connected := br.Status()
		where := listen
		if connect != "" {
			where = connect
		}
		if connected {
			s.Buses = append(s.Buses, fmt.Sprintf("I2C%d tcp %s ← %s", br.Bus, where, client))
		} else {
			s.Buses = append(s.Buses, fmt.Sprintf("I2C%d tcp %s · listening", br.Bus, where))
		}
	}
	for _, br := range e.spiBuses {
		listen, connect, client, connected := br.Status()
		where := listen
		if connect != "" {
			where = connect
		}
		if connected {
			s.Buses = append(s.Buses, fmt.Sprintf("SPI%d CS%d tcp %s ← %s", br.Bus, br.CS, where, client))
		} else {
			s.Buses = append(s.Buses, fmt.Sprintf("SPI%d CS%d tcp %s · listening", br.Bus, br.CS, where))
		}
	}
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
