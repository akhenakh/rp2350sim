package main

import (
	"fmt"
	"image"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"image/color"

	"github.com/akhenakh/rp2350sim/internal/coverage"
	"github.com/akhenakh/rp2350sim/internal/debug"
	"github.com/akhenakh/rp2350sim/internal/deviceplugin"
	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/devices/ble"
	"github.com/akhenakh/rp2350sim/internal/devices/cyw43439"
	"github.com/akhenakh/rp2350sim/internal/firmware"
	"github.com/akhenakh/rp2350sim/internal/hang"
	"github.com/akhenakh/rp2350sim/internal/midi"
	"github.com/akhenakh/rp2350sim/internal/otpfile"
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
	Pins    [48]PinInfo
	GPIOs   int
	Package string
	DVI     *image.RGBA
	Cycles  uint64
	PC, SP  uint32
	Halted  bool
	Running bool
	// Speed is the virtual/wall ratio since the run started; Slow reports
	// that the machine cannot keep up with real time under pacing.
	Speed         float64
	Pacing        bool
	Slow          bool
	Firmware      string
	FlashPath     string
	Boots         uint64
	FlashWrites   uint64
	BootEntry     uint32
	ResetReason   string
	PSRAMBytes    uint32
	PSRAMLatency  uint32
	Err           string
	LCD           *image.RGBA
	Console       string
	ConsolePos    int64
	WSColors      []color.RGBA
	WSFrames      uint64
	WSEnabled     bool
	AudioRate     int
	AudioChannels int
	AudioSamples  int
	EthKind       string
	Network       *devices.NetworkInfo
	EthLog        []string
	Serial        []string
	Buses         []string
	DeviceLogs    []string

	Touch     *TouchState
	IMU       *IMUState
	Battery   *BatteryState
	Backlight *BacklightState
	SD        *SDState
	OTP       *OTPState
	HID       []HIDDeviceState
	BLE       *BLEState
	CYW       *CYWState
	Hang      string

	// Inputs is a copy of the interactive board-control state, keyed by
	// InputDef.ID.
	Inputs        map[string]InputValue
	BootselHeld   bool
	BootselAtBoot bool
}

// InputValue is the host-side state of one interactive board input.
type InputValue struct {
	// On is the pressed/closed state of a button, switch or BOOTSEL input.
	On bool
	// Volts is the wiper voltage of an ADC potentiometer/slider.
	Volts float32
	// Phase is the quadrature phase (0..3) of a rotary encoder.
	Phase uint8
}

func (e *Emu) addDeviceLog(line string) {
	e.deviceLog = append(e.deviceLog, line)
	if len(e.deviceLog) > 500 {
		e.deviceLog = e.deviceLog[len(e.deviceLog)-500:]
	}
}

// BLEState is the virtual controller view for the BLE tab.
type BLEState struct {
	Address       string
	Connected     bool
	Peer          string
	MTU           uint16
	Notifications bool
	Pending       int
	Events        []string
}

// CYWState is the CYW43439 bring-up state shown in the Wi-Fi panel.
type CYWState struct {
	FirmwareUp bool
	LinkUp     bool
	SSID       string
	LED        bool
	MAC        string
	Pins       string
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
	SeedSource string
	SeedRows   int
	SeedPage   int
}

// Emu owns the emulated machine and steps it in the background.
type Emu struct {
	mu        sync.Mutex
	chip      *rp2350.Chip
	lcd       *devices.ST7789
	audio     *devices.AudioDecoder
	img       *firmware.Image
	debugInfo *debug.Info
	ble       *ble.Controller
	bleEvents []string
	deviceLog []string
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
	// frameEvery is the visible tab's UI rebuild period. The browser loop
	// yields at least this often so the page can paint and sample input.
	frameEvery time.Duration
	// picker* preserves the user's execution state while the board picker owns
	// the window, so selecting a board again resumes exactly as before.
	pickerResume bool
	pickerPaused bool
	atPicker     bool
	console      []byte
	consolePos   int64
	// usbInput holds host console bytes until the emulated CDC host is ready.
	// UART input is delivered immediately; USB input is drained on run chunks.
	usbInput []byte

	lcdGen   uint64
	lcdFrame *image.RGBA
	lcdDuty  float32
	otpGen   uint64
	otpRows  []uint32

	// OTP seeding and persistence. otpFresh is set when a board is selected so
	// resetChip applies the board's seed instead of carrying the previous
	// chip's rows (Reset, Load and watch reloads keep the fuses).
	otpSeed           []otpfile.SeedRow
	otpSeedSource     string
	otpSeedPage       int
	otpSeeded         int
	otpOverride       []otpfile.SeedRow
	otpOverrideSet    bool
	otpOverrideSource string
	otpPersist        string
	otpFresh          bool
	otpPersistWrites  uint64
	otpPersistVersion uint64
	lastOTPVersion    uint64

	board *BoardDef

	// inputs holds the interactive board controls; bootselHeld survives chip
	// resets and is shared by the panel and the board diagram.
	inputs      map[string]InputValue
	bootselHeld bool
	// encQueue holds scripted encoder steps, applied one per interval so the
	// firmware can sample the quadrature transitions after boot.
	encQueue     map[string]int
	encNextCycle uint64

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
	cyw     *cyw43439.Device
	gpsDev  *devices.GPS
	canBus  *devices.CANBus
	canDev  *devices.MCP2515

	scopePins []int
	scope     *devices.Trace
	logic     *devices.Trace
	// logicHook is the analyzer's interval sampler. It is paused unless the
	// Logic tab is visible or a trigger is armed, so a board with a logic:
	// block does not pay for capture while another tab is open.
	logicHook   *rp2350.PeriodicHook
	logicTab    bool
	logicActive bool
	signals     []*devices.SignalSource
	usbEvents   []USBEvent
	uartEvents  []UARTEvent
	spiEvents   []SPIEvent
	i2cEvents   []I2CEvent

	midiKind    string
	midiUART    int
	midiEvents  []MIDIEvent
	midiPending []midi.Message
	midiParser  midi.Parser
	midiNotes   map[int]bool

	hidEvents []HIDEvent
	hidQueue  [][]byte

	hang     *rp2350.HangInfo
	hangText string

	// Profile tab state: the chip owns raw collection; the report is rebuilt
	// at most every few virtual milliseconds and frozen once stopped.
	plugins     []*deviceplugin.Plugin
	pluginSlots []string
	pluginLangs []string
	pluginErrs  []string

	traceMu     sync.Mutex
	traceEvents []rp2350.TraceEvent

	profWanted      bool
	profileCache    *coverage.Report
	profileCacheAt  uint64
	profileCacheEnd bool

	trigger LogicTrigger
	frozen  frozenLogic

	scopeTrig   ScopeTriggerConfig
	frozenScope frozenScope
	dviGen      uint64
	dviFrame    *image.RGBA

	bridges    []*devices.UARTBridge
	bridgeErrs []string
	serialSig  string

	// Real-time pacing: the loop keeps virtual time level with wall time and
	// sleeps when it runs ahead. turbo disables that entirely.
	turbo       bool
	paceStart   time.Time
	paceCycles  uint64
	speedStart  time.Time
	speedCycles uint64
	speed       float64
	slow        bool

	i2cBuses  []*devices.I2CBridge
	spiBuses  []*devices.SPIBridge
	busAll    []*devices.BusBridge
	busErrors []string
	presetErr []string
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

// MIDIEvent is one parsed MIDI message seen on the selected MIDI source.
type MIDIEvent struct {
	Cycle uint64
	Text  string
	Bytes string
}

// HIDEvent is one decoded USB HID input report from the firmware.
type HIDEvent struct {
	Cycle     uint64
	Interface int
	Kind      string
	ReportID  byte
	Text      string
	Bytes     string
}

// HIDDeviceState is one HID interface the firmware enumerated.
type HIDDeviceState struct {
	Interface        int
	Kinds            []string
	Protocol         uint8
	Subclass         uint8
	InEndpoint       int
	OutEndpoint      int
	ReportID         byte
	KeyboardReportID byte
	OutputReportID   byte
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
	e := &Emu{debugger: rp2350.NewDebugger(), otpSeedPage: -1}
	e.mu.Lock()
	e.resetChip()
	e.mu.Unlock()
	go e.loop()
	return e
}

// SetOTPOverride replaces the selected board's otp.file for this session.
// Persistence is disabled so an alternate device image is not written into the
// board's persist target.
func (e *Emu) SetOTPOverride(rows []otpfile.SeedRow, source string) {
	e.mu.Lock()
	e.otpOverride = rows
	e.otpOverrideSet = true
	e.otpOverrideSource = source
	e.mu.Unlock()
}

// SetBoard selects the board whose devices are attached on the next boot.
// Passing nil returns to the picker and releases the running firmware and
// host sockets while retaining the loaded image for the next selection.
func (e *Emu) SetBoard(b *BoardDef) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Delivers queued Note-Off/All-Notes-Off messages to the old transport
	// before resetChip drops its endpoints.
	e.flushMIDILocked()
	// The old board's persist target (if any) owns anything burned so far.
	e.saveOTPLocked()
	if b == nil {
		e.stopLocked()
		return
	}
	resume, paused, fromPicker := e.pickerResume, e.pickerPaused, e.atPicker
	e.pickerResume, e.pickerPaused, e.atPicker = false, false, false
	e.board = b
	e.errText = ""
	// A board switch starts from a fresh fuse array and applies that board's
	// seed. Reset/Load/reload keep the current rows instead.
	e.otpFresh = true
	e.otpSeed, e.otpSeedSource, e.otpPersist = nil, "", ""
	e.otpSeedPage = -1
	if e.otpOverrideSet {
		e.otpSeed = e.otpOverride
		e.otpSeedSource = e.otpOverrideSource
	} else if b.OTP != nil {
		e.otpSeed = b.OTP.Seed
		e.otpSeedSource = b.OTP.Source
		e.otpPersist = b.OTP.Persist
	}
	for _, r := range e.otpSeed {
		if page := r.Row / rp2350.OTPPageRows; e.otpSeedPage < 0 || page < e.otpSeedPage {
			e.otpSeedPage = page
		}
	}
	e.scopePins = b.ScopePins()
	e.initInputsLocked()
	if e.img != nil || e.fwPath != "" {
		e.rebootLocked()
	} else if err := e.loadLocked(); err != nil {
		e.errText = err.Error()
	}
	if fromPicker {
		e.running = resume
		if resume {
			e.debugger.Resume()
		} else if paused {
			e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
		}
	}
}

// stopLocked releases host resources and leaves an idle, boardless chip for
// the picker. The caller holds e.mu.
func (e *Emu) stopLocked() {
	e.pickerResume = e.running
	e.pickerPaused = e.debugger != nil && e.debugger.Stopped
	e.atPicker = true
	var flash []byte
	if e.chip != nil {
		flash = e.chip.FlashCopy()
	}
	e.running = false
	if e.debugger != nil && e.chip != nil {
		e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
	}
	for _, br := range e.bridges {
		br.Close()
	}
	e.bridges = nil
	e.bridgeErrs = nil
	e.serialSig = ""
	for _, br := range e.busAll {
		br.Close()
	}
	e.busAll = nil
	e.i2cBuses = nil
	e.spiBuses = nil
	e.busErrors = nil
	e.busSig = ""
	if e.w5500 != nil {
		e.w5500.Close()
	}
	if e.w6300 != nil {
		e.w6300.Close()
	}
	if e.ch9120 != nil {
		e.ch9120.Close()
	}
	e.board = nil
	e.scopePins = nil
	e.errText = ""
	e.usbInput = nil
	e.initInputsLocked()
	e.resetChip()
	if flash != nil {
		copy(e.chip.Flash, flash)
	}
	e.applyInputsLocked()
	e.console = e.console[:0]
	e.consolePos = 0
}

// initInputsLocked resets the interactive controls to their board defaults.
func (e *Emu) initInputsLocked() {
	e.inputs = map[string]InputValue{}
	e.encQueue = map[string]int{}
	e.encNextCycle = 0
	e.bootselHeld = false
	if e.board == nil {
		return
	}
	for _, in := range e.board.Inputs {
		v := InputValue{On: in.Initial}
		if in.Kind == "adc" {
			v.Volts = float32(in.DefaultVolts())
		}
		e.inputs[in.ID] = v
	}
}

// inputDefLocked finds an input definition on the current board.
func (e *Emu) inputDefLocked(id string) *InputDef {
	if e.board == nil {
		return nil
	}
	for i := range e.board.Inputs {
		if e.board.Inputs[i].ID == id {
			return &e.board.Inputs[i]
		}
	}
	return nil
}

// SetInput updates one interactive board control and drives the chip pins.
// Values outside an ADC input's range are clamped.
func (e *Emu) SetInput(id string, v InputValue) {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := e.inputDefLocked(id)
	if in == nil {
		return
	}
	switch in.Kind {
	case "adc":
		v.Volts = min(max(v.Volts, float32(in.Min)), float32(in.Max))
		v.On, v.Phase = false, 0
	case "encoder":
		return
	case "bootsel":
		e.bootselHeld = v.On
	default:
		v.Volts, v.Phase = 0, 0
	}
	e.inputs[id] = v
	e.applyInputsLocked()
}

// StepEncoder advances a rotary encoder by one quadrature phase (+1 clockwise,
// -1 counter-clockwise) immediately; the UI uses this per click/frame.
func (e *Emu) StepEncoder(id string, delta int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stepEncoderLocked(id, delta)
}

func (e *Emu) stepEncoderLocked(id string, delta int) {
	in := e.inputDefLocked(id)
	if in == nil || in.Kind != "encoder" {
		return
	}
	v := e.inputs[id]
	v.Phase = uint8((int(v.Phase) + delta) & 3)
	e.inputs[id] = v
	e.applyInputsLocked()
}

// QueueEncoder schedules steps for a scripted run. They are delivered one per
// 20 ms of virtual time so firmware that samples the quadrature lines after
// boot sees each transition (a pre-boot phase jump would count nothing).
func (e *Emu) QueueEncoder(id string, steps int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := e.inputDefLocked(id)
	if in == nil || in.Kind != "encoder" || steps == 0 {
		return
	}
	if e.encNextCycle == 0 && e.chip != nil {
		e.encNextCycle = e.chip.Cycles + 20*cyclesPerMillisecond
	}
	e.encQueue[id] += steps
}

const cyclesPerMillisecond = 150_000

// advanceScheduledInputsLocked delivers at most one queued encoder step per
// interval. Called after each run chunk so UI and headless runs behave alike.
func (e *Emu) advanceScheduledInputsLocked() {
	if e.chip == nil || len(e.encQueue) == 0 {
		return
	}
	if e.encNextCycle == 0 {
		e.encNextCycle = e.chip.Cycles + 20*cyclesPerMillisecond
	}
	if e.chip.Cycles < e.encNextCycle {
		return
	}
	ids := make([]string, 0, len(e.encQueue))
	for id := range e.encQueue {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := e.encQueue[id]
		if n == 0 {
			delete(e.encQueue, id)
			continue
		}
		delta := 1
		if n < 0 {
			delta = -1
		}
		e.stepEncoderLocked(id, delta)
		e.encQueue[id] = n - delta
		if e.encQueue[id] == 0 {
			delete(e.encQueue, id)
		}
		e.encNextCycle = e.chip.Cycles + 20*cyclesPerMillisecond
		return
	}
}

// SetBootsel presses or releases the BOOTSEL button, pulling the QSPI chip
// select low while held. A bootsel input on the board is kept in sync.
func (e *Emu) SetBootsel(held bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bootselHeld = held
	if e.board != nil {
		for i := range e.board.Inputs {
			in := &e.board.Inputs[i]
			if in.Kind == "bootsel" {
				v := e.inputs[in.ID]
				v.On = held
				e.inputs[in.ID] = v
			}
		}
	}
	if e.chip != nil {
		e.chip.SetBootsel(held)
	}
}

// applyInputsLocked drives every board input onto the chip. Called after a
// reset (which clears the pin model) and after an input changes.
func (e *Emu) applyInputsLocked() {
	c := e.chip
	if c == nil || e.board == nil {
		return
	}
	for _, in := range e.board.Inputs {
		v := e.inputs[in.ID]
		low := in.IsActiveLow()
		switch in.Kind {
		case "button", "switch":
			level := v.On
			if low {
				level = !level
			}
			c.Pins.SetExternal(in.Pin, level)
		case "encoder":
			a, b := quadraturePhase(v.Phase)
			if low {
				a, b = !a, !b
			}
			c.Pins.SetExternal(in.PinA, a)
			c.Pins.SetExternal(in.PinB, b)
		case "adc":
			c.Pins.SetAnalog(in.Pin, v.Volts)
		case "bootsel":
			e.bootselHeld = v.On
		}
	}
	c.SetBootsel(e.bootselHeld)
}

// quadraturePhase returns the asserted (closed-to-active) A/B pattern of a
// standard rotary encoder at a phase: 00 (detent) -> 10 -> 11 -> 01 -> 00.
// Active-low inputs (pull-ups) invert these to get the electrical levels.
func quadraturePhase(phase uint8) (a, b bool) {
	switch phase & 3 {
	case 1:
		return true, false
	case 2:
		return true, true
	case 3:
		return false, true
	default:
		return false, false
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
	e.applyLogicCaptureLocked()
}

// RearmLogic clears a frozen capture and waits for the next trigger event.
func (e *Emu) RearmLogic() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.frozen = frozenLogic{}
	e.applyLogicCaptureLocked()
}

// SetLogicTabActive tells the emulator whether the Logic tab is on screen.
// Capture runs while it is, or from boot when a trigger is armed; otherwise
// the 2 MHz sampler is paused and does not bound the idle fast-forward.
func (e *Emu) SetLogicTabActive(active bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.logicTab == active {
		return
	}
	e.logicTab = active
	e.applyLogicCaptureLocked()
}

func (e *Emu) logicCaptureWantedLocked() bool {
	return !e.frozen.triggered && (e.logicTab || e.trigger.Kind != "")
}

// applyLogicCaptureLocked pauses or resumes the analyzer sampler. Reactivating
// starts a fresh window; the previous ring may predate the pause.
func (e *Emu) applyLogicCaptureLocked() {
	want := e.logicCaptureWantedLocked()
	if e.logic == nil || e.logicHook == nil {
		e.logicActive = false
		return
	}
	if want == e.logicActive {
		return
	}
	e.logicActive = want
	if want {
		e.logic.Reset()
		e.logic.Tick()
	}
	e.logicHook.SetActive(want)
}

// LogicTriggered reports whether a capture is frozen and what caused it.
func (e *Emu) LogicTriggered() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frozen.triggered, e.frozen.info
}

// checkTriggerLocked captures the trace and event rings on a match. The
// caller holds e.mu.
// triggerArmed reports whether a trigger of this kind is still waiting. Bus taps
// build a message per byte; when nothing is armed that formatting is pure
// overhead, so they check this first.
func (e *Emu) triggerArmed(kind string) bool {
	return !e.frozen.triggered && e.trigger.Kind != "" &&
		(e.trigger.Kind == "any" || e.trigger.Kind == kind)
}

func (e *Emu) checkTriggerLocked(kind, info string) {
	if !e.triggerArmed(kind) {
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
	// The capture is frozen; no further samples are needed until rearm.
	e.applyLogicCaptureLocked()
}

// SetScopePins changes the scope channels without resetting the machine.
func (e *Emu) SetScopePins(pins []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scopePins = append([]int(nil), pins...)
	if e.scope != nil {
		e.scope.SetPins(pins)
	}
	e.frozenScope = frozenScope{}
}

// ScopeTriggerConfig selects a pin-edge trigger for the oscilloscope.
type ScopeTriggerConfig struct {
	// Mode is "", "rise", "fall", "high" or "low".
	Mode string
	Pin  int
}

type frozenScope struct {
	triggered bool
	info      string
	pins      []int
	samples   []devices.TraceSample
	index     int
	seen      uint64
	scanInit  bool
	lastBit   bool
	haveLast  bool
	found     bool
	trigT     uint64
	// post counts the samples after trigT; the capture freezes once a
	// quarter-window is available without copying the ring every chunk.
	post int
}

// SetScopeTrigger arms the scope trigger and clears any frozen capture.
func (e *Emu) SetScopeTrigger(cfg ScopeTriggerConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scope != nil && cfg.Pin >= 0 && cfg.Pin <= 47 {
		pins := e.scope.Pins()
		found := slices.Contains(pins, cfg.Pin)
		if !found && len(pins) < devices.MaxTraceChannels {
			pins = append(pins, cfg.Pin)
			e.scopePins = pins
			e.scope.SetPins(pins)
		} else if len(e.scopePins) == 0 {
			e.scopePins = append([]int(nil), pins...)
		}
	}
	e.scopeTrig = cfg
	e.frozenScope = frozenScope{}
	e.armScopeScanLocked()
}

// RearmScope clears a frozen capture and waits for the next trigger.
func (e *Emu) RearmScope() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.frozenScope = frozenScope{}
	e.armScopeScanLocked()
}

// armScopeScanLocked starts the trigger scan at the newest sample so arming
// and rearming wait for a future edge instead of replaying the ring.
func (e *Emu) armScopeScanLocked() {
	if e.scope == nil {
		return
	}
	e.frozenScope.seen = e.scope.LatestT()
	e.frozenScope.scanInit = true
}

// ScopeTriggered reports whether the scope capture is frozen and why.
func (e *Emu) ScopeTriggered() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frozenScope.triggered, e.frozenScope.info
}

// ScopeTriggerIndex is the index of the trigger sample in the frozen window.
func (e *Emu) ScopeTriggerIndex() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frozenScope.index
}

// checkScopeTriggerLocked scans the new scope samples for the configured edge
// or level, then keeps a quarter-window of post-trigger data before freezing.
// The caller holds e.mu.
func (e *Emu) checkScopeTriggerLocked() {
	if e.scope == nil || e.scopeTrig.Mode == "" || e.frozenScope.triggered {
		return
	}
	ch := -1
	for i, pin := range e.scope.Pins() {
		if pin == e.scopeTrig.Pin {
			ch = i
			break
		}
	}
	if ch < 0 {
		return
	}
	fs := &e.frozenScope
	if !fs.scanInit {
		// Arm on the next sample. Replaying the ring would trigger on history
		// that may already be leaving the window.
		fs.scanInit = true
		fs.seen = e.scope.LatestT()
		if fs.seen > 0 {
			return
		}
	}
	// Walk only the samples added since the last call. While the trigger is
	// merely armed this costs no allocation; the full ring is copied once,
	// when the post-trigger window is complete.
	scan := func(s devices.TraceSample) bool {
		fs.seen = s.T
		if fs.found {
			if s.T > fs.trigT {
				fs.post++
			}
			if fs.post >= devices.ScopeDepth/4 {
				pins, samples := e.scope.Snapshot()
				e.freezeScopeLocked(pins, samples)
				return false
			}
			return true
		}
		bit := s.Bit(ch)
		if !fs.haveLast {
			fs.haveLast = true
			fs.lastBit = bit
			if (e.scopeTrig.Mode == "high" && bit) ||
				(e.scopeTrig.Mode == "low" && !bit) {
				fs.trigT = s.T
				fs.found = true
			}
			return true
		}
		match := false
		switch e.scopeTrig.Mode {
		case "rise":
			match = !fs.lastBit && bit
		case "fall":
			match = fs.lastBit && !bit
		case "high":
			match = bit
		case "low":
			match = !bit
		}
		fs.lastBit = bit
		if match {
			fs.trigT = s.T
			fs.found = true
		}
		return true
	}
	if !fs.haveLast && fs.seen == 0 {
		// Include the attach-instant sample at cycle 0; ScanSince(0) would
		// skip it and lose an edge from the baseline to the first new sample.
		e.scope.ScanAll(scan)
		return
	}
	e.scope.ScanSince(fs.seen, scan)
}

func (e *Emu) freezeScopeLocked(pins []int, samples []devices.TraceSample) {
	idx := 0
	for i, s := range samples {
		if s.T >= e.frozenScope.trigT {
			idx = i
			break
		}
	}
	e.frozenScope.triggered = true
	e.frozenScope.pins = pins
	e.frozenScope.samples = samples
	e.frozenScope.index = idx
	e.frozenScope.info = fmt.Sprintf("%s GP%d", e.scopeTrig.Mode, e.scopeTrig.Pin)
}

// ScopeData copies the scope trace for rendering.
func (e *Emu) ScopeData() ([]int, []devices.TraceSample, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frozenScope.triggered {
		interval := uint64(devices.ScopeInterval)
		if e.scope != nil {
			interval = e.scope.Interval
		}
		return e.frozenScope.pins, e.frozenScope.samples, interval
	}
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

// AudioInfo reports the decoder's format and sample count.
func (e *Emu) AudioInfo() (rate, channels, samples int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.audio == nil {
		return 0, 0, 0
	}
	cfg := e.audio.Config()
	return cfg.Rate, cfg.Channels, e.audio.Len()
}

// AudioWave returns the most recent maxSamples, so the waveform view shows a
// short live window instead of collapsing a whole capture into stripes.
func (e *Emu) AudioWave(maxSamples int) []int16 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.audio == nil || maxSamples <= 0 {
		return nil
	}
	all := e.audio.Samples()
	if len(all) <= maxSamples {
		return all
	}
	return append([]int16(nil), all[len(all)-maxSamples:]...)
}

// AudioWAV returns the current capture as a PCM WAV file.
func (e *Emu) AudioWAV() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.audio == nil {
		return nil
	}
	return e.audio.WAV()
}

// SaveAudio writes the current capture to path.
func (e *Emu) SaveAudio(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.audio == nil {
		return fmt.Errorf("no audio source")
	}
	return e.audio.WriteWAV(path)
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

// trimRing keeps the newest keep elements of s in place once it grows past
// max. Reslicing the tail instead moves the start forward, which forces a
// reallocation on almost every append; a per-byte tap cannot afford that.
func trimRing[T any](s []T, max, keep int) []T {
	if len(s) <= max {
		return s
	}
	n := copy(s, s[len(s)-keep:])
	return s[:n]
}

func (e *Emu) addUART(bus int, cycle uint64, in bool, b byte) {
	e.uartEvents = append(e.uartEvents, UARTEvent{Bus: bus, Cycle: cycle, In: in, Data: b})
	e.uartEvents = trimRing(e.uartEvents, 512, 256)
	if !in && e.triggerArmed("uart") {
		e.checkTriggerLocked("uart", fmt.Sprintf("UART TX 0x%02x", b))
	}
}

func (e *Emu) addSPI(bus int, cycle uint64, tx, rx byte) {
	e.spiEvents = append(e.spiEvents, SPIEvent{Bus: bus, Cycle: cycle, TX: tx, RX: rx})
	e.spiEvents = trimRing(e.spiEvents, 512, 256)
	if e.triggerArmed("spi") {
		e.checkTriggerLocked("spi", fmt.Sprintf("SPI MOSI 0x%02x", tx))
	}
}

func (e *Emu) addI2C(bus int, cycle uint64, kind string, addr uint8, data byte, ack bool) {
	e.i2cEvents = append(e.i2cEvents, I2CEvent{Bus: bus, Cycle: cycle, Kind: kind, Addr: addr, Data: data, Ack: ack})
	e.i2cEvents = trimRing(e.i2cEvents, 512, 256)
	if !e.triggerArmed("i2c") {
		return
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
	e.usbEvents = trimRing(e.usbEvents, 64, 32)
}

// FlashUF2 parses a UF2 image with the BOOTSEL policy (any family id, flash
// targets only), erases the flash, programs the image and boots it.
func (e *Emu) FlashUF2(name string, data []byte) error {
	img, err := firmware.ParseUF2Loose(name, data)
	if err != nil {
		e.mu.Lock()
		e.errText = err.Error()
		e.mu.Unlock()
		return err
	}
	if err := img.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize); err != nil {
		e.mu.Lock()
		e.errText = err.Error()
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.img = img
	e.fwPath = name
	e.debugInfo = nil
	e.errText = ""
	e.resetChip()
	e.chip.LoadFlashImage(nil)
	for _, seg := range img.Segments {
		if err := e.chip.LoadSegment(seg.Addr, seg.Data); err != nil {
			e.errText = err.Error()
			return err
		}
	}
	e.applyInputsLocked()
	e.chip.Boot()
	e.applyInputsLocked()
	if err := e.attachBLELocked(); err != nil {
		return err
	}
	e.lastBoots = e.chip.BootCount()
	e.bootCount = e.lastBoots
	e.flashWrites = 0
	e.lastChipFW = e.chip.FlashWrites()
	e.console = e.console[:0]
	e.consolePos = 0
	e.running = true
	return nil
}

// resetChip builds a fresh machine and attaches the selected board's devices.
// Programmed OTP rows are fuses: unless a board was just selected, they are
// carried into the new chip so Reset, Load and watch reloads behave like a
// power cycle. The seed is applied once, on the fresh array.
func (e *Emu) resetChip() {
	if e.otpPersist != "" {
		e.saveOTPLocked()
	}
	var carried []uint32
	if e.chip != nil && !e.otpFresh {
		carried = e.chip.OTPRowsCopy()
	}
	c := rp2350.New()
	if e.otpFresh {
		e.otpSeeded = otpfile.Apply(c, e.otpSeed)
		e.otpFresh = false
	} else {
		for i, raw := range carried {
			if raw != 0 {
				c.SetOTPRow(i, raw)
			}
		}
	}
	// The seed/carried rows are a baseline, not a firmware burn: do not
	// rewrite the persist target until the firmware changes something.
	e.otpPersistVersion = c.OTPVersion()
	e.otpPersistWrites = c.OTPWrites()
	e.lastOTPVersion = c.OTPVersion()
	if e.board != nil && e.board.Package == "qfn80" {
		c.SetPackage(rp2350.PackageQFN80)
	}
	e.ble = nil
	e.bleEvents = nil
	e.deviceLog = nil
	e.debugger.Reset()
	c.Debug = e.debugger
	if e.sd != nil {
		_ = e.sd.Sync()
	}
	e.lcd = nil
	e.audio = nil
	e.ws = nil
	e.touch = nil
	e.imu = nil
	e.sd = nil
	e.blPin = -1
	if e.ch9120 != nil {
		e.ch9120.Close()
	}
	if e.cyw != nil {
		e.cyw.Close()
	}
	if e.w5500 != nil {
		e.w5500.Close()
	}
	if e.w6300 != nil {
		e.w6300.Close()
	}
	e.ch9120 = nil
	e.cyw = nil
	e.ethKind = ""
	e.w5500 = nil
	e.w6300 = nil
	e.ethLog.reset()
	e.frozen = frozenLogic{}
	e.usbEvents = e.usbEvents[:0]
	e.uartEvents = e.uartEvents[:0]
	e.spiEvents = e.spiEvents[:0]
	e.i2cEvents = e.i2cEvents[:0]
	e.hidEvents = e.hidEvents[:0]
	e.hidQueue = e.hidQueue[:0]
	e.hang = nil
	e.hangText = ""
	e.scope = nil
	e.logic = nil
	e.logicHook = nil
	e.logicActive = false
	e.signals = e.signals[:0]
	e.closePlugins()
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
			c.AddPeriodicHook(rp2350.CyclesPerMicrosecond, e.touch.Tick)
		}
		if d := b.IMU; d != nil {
			e.imu = devices.NewQMI8658(c, uint8(d.Addr), d.INT, d.INT2Pin())
			c.AttachI2C(d.Bus, uint8(d.Addr), e.imu)
			// The sample interval is milliseconds: a microsecond grid is
			// plenty and keeps the idle fast-forward available.
			c.AddPeriodicHook(rp2350.CyclesPerMicrosecond, e.imu.Tick)
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
				// The write-back timer is 50 ms; polling it every
				// microsecond matches the previous behaviour closely enough.
				c.AddPeriodicHook(rp2350.CyclesPerMicrosecond, card.Tick)
			}
		}
		if w := b.Wifi; w != nil {
			if w.Kind == "cyw43439" {
				mac, _ := parseMAC(w.MAC)
				e.cyw = cyw43439.Attach(c, cyw43439.Config{
					RegOn:    w.RegOn,
					Data:     w.Data,
					CS:       w.CS,
					CLK:      w.CLK,
					MAC:      mac,
					SSID:     w.SSID,
					Password: w.Pass,
					Network:  &cyw43439.NetConfig{Hosts: w.Hosts, Disable: w.NoNet},
				})
			} else {
				ch := devices.NewCH9120(c, w.UART, w.CFG0, w.RST, w.TCPCS)
				ch.Logger = logEth
				ch.OnByte = func(v byte) {
					e.addUART(w.UART, c.Cycles, false, v)
				}
				e.ch9120 = ch
				e.ethKind = "wifi"
			}
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
				if d.Kind == "w6300" {
					eth := devices.NewW6300(c, d.CS)
					eth.Logger = logEth
					e.w6300 = eth
					transfer = eth.Transfer
				} else {
					eth := devices.NewW5500(c, d.CS)
					eth.Logger = logEth
					e.w5500 = eth
					transfer = eth.Transfer
				}
				// A raised chip select is observed by the next Transfer; there
				// is no per-cycle state to poll.
				attachSPI(d.SPI, d.CS, transfer)
			}
		}
		if d := b.WS2812; d != nil {
			e.ws = devices.NewWS2812B(c, d.Pin)
		}
		if d := b.Audio; d != nil {
			kind := devices.AudioPWM
			switch d.Kind {
			case "i2s":
				kind = devices.AudioI2S
			case "pdm":
				kind = devices.AudioPDM
			}
			e.audio = devices.NewAudio(c, devices.AudioConfig{
				Kind: kind, BCLK: d.BCLK, LRCLK: d.LRCLK, Data: d.Data,
				Clock: d.Clock, Pin: d.Pin, Bits: d.Bits, Delay: 1,
				Decimation: d.Decimation, Rate: d.Rate,
			})
		}

		for _, sd := range b.Analog {
			wave := devices.WaveSine
			switch sd.Wave {
			case "square":
				wave = devices.WaveSquare
			case "triangle":
				wave = devices.WaveTriangle
			case "ramp", "saw":
				wave = devices.WaveRamp
			case "noise":
				wave = devices.WaveNoise
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
			c.AddPeriodicHook(src.Interval, src.Tick)
		}

		if b.GPS != nil {
			g := b.GPS
			dev := devices.NewGPS(c, g.UART, devices.GPSConfig{
				Lat: g.Lat, Lon: g.Lon, Altitude: g.Altitude,
				SpeedMPS: g.SpeedMPS, Course: g.Course,
				Satellites: g.Satellites,
				Fix:        g.Fix != nil && *g.Fix, FixSet: g.Fix != nil,
				Rate: time.Duration(g.RateMS) * time.Millisecond,
			})
			c.AttachUART(g.UART, dev)
			e.gpsDev = dev
		}
		if b.CAN != nil {
			can := b.CAN
			bus := devices.NewCANBus(c)
			ext := can.PeerID > 0x7FF || can.ReplyID > 0x7FF
			devices.NewCANPeer(bus, can.PeerID, can.ReplyID, ext)
			intPin := -1
			if can.INT != nil {
				intPin = *can.INT
			}
			m := devices.NewMCP2515(c, bus, can.CS, intPin)
			attachSPI(can.Bus, can.CS, m.Transfer)
			c.Pins.AddChangeListener(func(pin int, level bool) {
				if pin == can.CS && !level {
					m.Select()
				}
			})
			e.canBus, e.canDev = bus, m
		}
		e.attachBus(c, b, attachSPI)
		e.attachBusPresets(c, b, attachSPI)
		for _, d := range b.OneWire {
			w := devices.NewWire(c, d.Pin)
			devices.NewDS18B20(w, devices.DS18B20Config{
				TemperatureC: d.Temperature,
				OnRead: func(temperature float64, raw []byte) {
					e.addDeviceLog(fmt.Sprintf("%8.2f ms 1-Wire DS18B20 %.2f C raw=% X",
						float64(e.chip.Cycles)/150_000, temperature, raw))
				},
			})
		}
		for _, d := range b.DHT {
			devices.NewDHT(devices.NewWire(c, d.Pin), devices.DHTConfig{
				Kind: d.Kind, Humidity: d.Humidity, Temperature: d.Temperature,
				OnRead: func(kind string, humidity, temperature float64, raw []byte) {
					e.addDeviceLog(fmt.Sprintf("%8.2f ms %s %.1f %% %.1f C raw=% X",
						float64(e.chip.Cycles)/150_000, strings.ToUpper(kind), humidity, temperature, raw))
				},
			})
		}
		for _, d := range b.Ultrasonic {
			us := devices.NewHCSR04(devices.NewWire(c, d.Trig), devices.NewWire(c, d.Echo), d.DistanceCM)
			us.OnMeasure = func(distance float64, echoUS uint64) {
				e.addDeviceLog(fmt.Sprintf("%8.2f ms HC-SR04 %.0f cm raw=0x%X (%d us)",
					float64(e.chip.Cycles)/150_000, distance, echoUS, echoUS))
			}
		}
		pluginSelects := e.attachPlugins(c, b, attachSPI)
		if len(pluginSelects) > 0 {
			prev := c.Pins.OnChange
			c.Pins.OnChange = func(pin int, level bool) {
				if prev != nil {
					prev(pin, level)
				}
				if f := pluginSelects[pin]; f != nil {
					f(!level)
				}
			}
		}

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
		// Sample the attach instant so the window opens at boot, not one
		// interval later.
		e.scope.Tick()
		c.AddPeriodicHook(e.scope.Interval, e.scope.Tick)

		if pins := b.LogicPins(); len(pins) > 0 {
			e.logic = devices.NewTrace(c, pins, devices.LogicInterval, devices.LogicDepth)
			e.logic.Digital = true
			e.logic.Tick()
			e.logicHook = c.AddPeriodicHook(e.logic.Interval, e.logic.Tick)
			e.logicActive = true
			e.applyLogicCaptureLocked()
		}
		c.OnI2C = func(bus int, cycles uint64, kind string, addr uint8, data byte, ack bool) {
			e.addI2C(bus, cycles, kind, addr, data, ack)
		}
	}

	trimConsole := func() {
		// Keep the tail in place; the UI resets its view when the length drops.
		if len(e.console) > 64*1024 {
			n := copy(e.console, e.console[len(e.console)-32*1024:])
			e.console = e.console[:n]
		}
	}
	appendConsole := func(b []byte) {
		e.console = append(e.console, b...)
		e.consolePos += int64(len(b))
		trimConsole()
	}
	appendConsoleByte := func(b byte) {
		e.console = append(e.console, b)
		e.consolePos++
		trimConsole()
	}
	c.OnUART0 = func(b byte) {
		appendConsoleByte(b)
		e.addUART(0, c.Cycles, false, b)
	}
	c.OnUART1 = func(b byte) {
		appendConsoleByte(b)
		e.addUART(1, c.Cycles, false, b)
	}
	c.SetUSBCDC(func(b []byte) {
		appendConsole(b)
		e.addUSBEvent(c.Cycles, true, b)
	})
	c.SetHangDetection(30_000_000)
	c.OnHang = func(info rp2350.HangInfo) {
		e.hang = &info
		e.hangText = hang.Describe(info, e.debugInfo, c.PeekRegister)
		e.running = false
	}
	c.SetUSBHID(func(ev rp2350.USBHIDEvent) {
		e.hidEvents = append(e.hidEvents, HIDEvent{
			Cycle:     c.Cycles,
			Interface: ev.Interface,
			Kind:      ev.Kind,
			ReportID:  ev.ReportID,
			Text:      ev.Text,
			Bytes:     fmt.Sprintf("% x", ev.Data),
		})
		e.hidEvents = trimRing(e.hidEvents, 256, 128)
	})

	// The module owns UART1, so it must install its hook after the console
	// callbacks above.
	if e.ch9120 != nil {
		e.ch9120.Attach()
	}

	e.attachSerial(c, e.board)
	e.attachMIDI(c, e.board)

	c.OnTrace = e.recordTrace
	e.chip = c
	e.profileCache = nil
	e.profileCacheAt = 0
	e.profileCacheEnd = false
	if e.profWanted {
		c.StartProfile(rp2350.ProfileConfig{SampleCycles: profileSampleCycles, Coverage: true})
	}
	e.lcdGen = 0
	e.lcdFrame = nil
	e.otpGen, e.otpRows = 0, nil
	e.lcdDuty = -1
}

// PluginInfo is the UI view of one loaded Lua/WASM device model.
type PluginInfo struct {
	// Index is the loaded plugin's position, or -1 for a load error.
	Index int
	Name  string
	Lang  string
	Kind  string
	Slot  string
	Logs  []string
	Error string
}

// Plugins returns the loaded plug-ins for the current board.
func (e *Emu) Plugins() []PluginInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]PluginInfo, 0, len(e.plugins)+len(e.pluginErrs))
	for i, p := range e.plugins {
		info := PluginInfo{Index: i, Name: p.Name(), Logs: p.Logs()}
		if i < len(e.pluginSlots) {
			info.Kind, info.Slot = pluginKindSlot(e.pluginSlots[i])
		}
		if i < len(e.pluginLangs) {
			info.Lang = e.pluginLangs[i]
		}
		if err := p.Err(); err != nil {
			info.Error = err.Error()
		}
		out = append(out, info)
	}
	for _, msg := range e.pluginErrs {
		out = append(out, PluginInfo{Index: -1, Name: "plugin", Error: msg})
	}
	return out
}

// ClearPluginLogs drops one loaded plugin's buffered output.
func (e *Emu) ClearPluginLogs(index int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if index >= 0 && index < len(e.plugins) {
		e.plugins[index].ClearLogs()
	}
}

func pluginKindSlot(slot string) (kind, name string) {
	kind, name, _ = strings.Cut(slot, " ")
	return kind, name
}

func (e *Emu) closePlugins() {
	for _, p := range e.plugins {
		_ = p.Close()
	}
	e.plugins = e.plugins[:0]
	e.pluginSlots = e.pluginSlots[:0]
	e.pluginLangs = e.pluginLangs[:0]
	e.pluginErrs = e.pluginErrs[:0]
}

// attachPlugins loads the board's Lua/WASM models and binds them to the bus.
// The returned callbacks receive SPI chip-select edges.
func (e *Emu) attachPlugins(c *rp2350.Chip, b *BoardDef, attachSPI func(bus, cs int, fn func(byte) byte)) map[int]func(bool) {
	selects := map[int]func(bool){}
	if b == nil {
		return selects
	}
	// add loads one plugin. uart is its UART index, or -1 for I2C/SPI models.
	add := func(def PluginDef, slot, kind string, pins map[string]int, uart int) *deviceplugin.Plugin {
		name := def.Name
		if name == "" {
			name = kind + " plugin"
		}
		srcName := def.File
		if srcName == "" {
			srcName = def.URL
		}
		lang, err := deviceplugin.LanguageForPath(srcName)
		if err != nil {
			e.pluginErrs = append(e.pluginErrs, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		source, err := pluginSource(def.File, def.URL)
		if err != nil {
			e.pluginErrs = append(e.pluginErrs, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		cfgMap := maps.Clone(def.Config)
		if len(pins) > 0 {
			if cfgMap == nil {
				cfgMap = map[string]any{}
			}
			pinMap := map[string]any{}
			for k, v := range pins {
				pinMap[k] = v
			}
			cfgMap["pins"] = pinMap
		}
		config, err := deviceplugin.ConfigBytes(lang, cfgMap)
		if err != nil {
			e.pluginErrs = append(e.pluginErrs, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		allowed := map[int]bool{}
		for _, pin := range pins {
			allowed[pin] = true
		}
		cfg := deviceplugin.Config{
			Name:   name,
			Lang:   lang,
			Source: source,
			Config: config,
			GPIORead: func(pin int) int {
				if len(allowed) > 0 && !allowed[pin] {
					return 0
				}
				if c.Pins.Level(pin) {
					return 1
				}
				return 0
			},
			GPIOWrite: func(pin int, level bool) {
				if len(allowed) > 0 && !allowed[pin] {
					return
				}
				c.Pins.SetExternal(pin, level)
			},
		}
		if uart >= 0 {
			cfg.UARTWrite = func(data []byte) { c.SendUART(uart, data) }
		}
		p, err := deviceplugin.Load(cfg)
		if err != nil {
			e.pluginErrs = append(e.pluginErrs, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		e.plugins = append(e.plugins, p)
		e.pluginSlots = append(e.pluginSlots, kind+" "+slot)
		langName := "Lua"
		if lang == deviceplugin.WASM {
			langName = "WASM"
		}
		e.pluginLangs = append(e.pluginLangs, langName)
		// Plugins are device models, not cycle-level hardware: a 1 ms tick is
		// enough for serial events and keeps idle fast-forward cheap.
		c.AddPeriodicHook(rp2350.CyclesPerMicrosecond*1000, func() {
			p.Tick(c.NowMicros())
		})
		return p
	}
	for _, d := range b.I2CPlugins {
		p := add(d.PluginDef, fmt.Sprintf("i2c%d 0x%02x", d.Bus, d.Addr), "i2c", nil, -1)
		if p != nil {
			c.AttachI2C(d.Bus, uint8(d.Addr), deviceplugin.NewI2C(p, uint8(d.Addr)))
		}
	}
	for _, d := range b.SPIPlugins {
		p := add(d.PluginDef, fmt.Sprintf("spi%d cs%d", d.Bus, d.CS), "spi", nil, -1)
		if p != nil {
			spi := deviceplugin.NewSPI(p)
			attachSPI(d.Bus, d.CS, spi.Transfer)
			selects[d.CS] = spi.Select
		}
	}
	for _, d := range b.UARTPlugins {
		p := add(d.PluginDef, fmt.Sprintf("%d", d.UART), "uart", d.Pins, d.UART)
		if p != nil {
			c.AttachUART(d.UART, deviceplugin.NewUART(p))
		}
	}
	return selects
}

// attachBusPresets attaches the board's built-in I2C/SPI device presets to a
// fresh chip. SPI presets also reset their command state when their chip
// select is asserted, so every frame starts at a command byte.
func (e *Emu) attachBusPresets(c *rp2350.Chip, b *BoardDef, attachSPI func(bus, cs int, fn func(byte) byte)) {
	e.presetErr = e.presetErr[:0]
	if b == nil {
		return
	}
	for _, d := range b.I2CPresets {
		dev, addr, err := devices.NewI2CPreset(d.Preset, d.Addr)
		if err != nil {
			e.presetErr = append(e.presetErr, fmt.Sprintf("I2C%d %s: %v", d.Bus, d.Preset, err))
			continue
		}
		c.AttachI2C(d.Bus, addr, dev)
	}
	selects := map[int]func(){}
	for _, d := range b.SPIPresets {
		p, err := devices.NewSPIPreset(d.Preset)
		if err != nil {
			e.presetErr = append(e.presetErr, fmt.Sprintf("SPI%d CS%d %s: %v", d.Bus, d.CS, d.Preset, err))
			continue
		}
		attachSPI(d.Bus, d.CS, p.Transfer)
		selects[d.CS] = p.Select
	}
	if len(selects) == 0 {
		return
	}
	prev := c.Pins.OnChange
	c.Pins.OnChange = func(pin int, level bool) {
		if prev != nil {
			prev(pin, level)
		}
		if !level {
			if f := selects[pin]; f != nil {
				f()
			}
		}
	}
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

// attachMIDI routes the board's MIDI source. USB-MIDI enables the class
// endpoint; UART-MIDI taps the selected UART after the console and bridge
// callbacks are in place.
func (e *Emu) attachMIDI(c *rp2350.Chip, b *BoardDef) {
	e.midiKind = ""
	e.midiUART = -1
	e.midiPending = e.midiPending[:0]
	e.midiEvents = e.midiEvents[:0]
	e.midiNotes = map[int]bool{}
	e.midiParser.Reset()
	if b == nil || b.MIDI == nil {
		return
	}
	switch b.MIDI.Kind {
	case "usb":
		e.midiKind = "usb"
		c.EnableUSBMIDI()
		c.SetUSBMIDI(func(data []byte) {
			for _, m := range midi.DecodeUSB(data) {
				e.addMIDI(c.Cycles, m)
			}
		})
	case "uart":
		e.midiKind = "uart"
		e.midiUART = b.MIDI.UART
		prev := c.OnUART0
		if b.MIDI.UART == 1 {
			prev = c.OnUART1
		}
		tap := func(v byte) {
			if prev != nil {
				prev(v)
			}
			for _, m := range e.midiParser.Feed(v) {
				e.addMIDI(c.Cycles, m)
			}
		}
		if b.MIDI.UART == 0 {
			c.OnUART0 = tap
		} else {
			c.OnUART1 = tap
		}
	}
}

// addMIDI records a message for the UI monitor and tracks sounding notes so
// the keyboard can light keys the firmware plays.
func (e *Emu) addMIDI(cycle uint64, m midi.Message) {
	e.midiEvents = append(e.midiEvents, MIDIEvent{
		Cycle: cycle,
		Text:  m.String(),
		Bytes: midi.ParseText(m.Bytes()),
	})
	e.midiEvents = trimRing(e.midiEvents, 256, 128)
	if e.midiNotes == nil {
		e.midiNotes = map[int]bool{}
	}
	switch m.Status & 0xF0 {
	case 0x90:
		if m.Data[1] == 0 {
			delete(e.midiNotes, int(m.Data[0]))
		} else {
			e.midiNotes[int(m.Data[0])] = true
		}
	case 0x80:
		delete(e.midiNotes, int(m.Data[0]))
	case 0xB0:
		if m.Data[0] == 120 || m.Data[0] == 123 { // all sound / notes off
			e.midiNotes = map[int]bool{}
		}
	}
}

// SoundingNotes returns the notes the firmware is currently playing.
func (e *Emu) SoundingNotes() map[int]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[int]bool, len(e.midiNotes))
	for note := range e.midiNotes {
		out[note] = true
	}
	return out
}

// MIDIEvents returns the recent parsed MIDI messages.
func (e *Emu) MIDIEvents() []MIDIEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]MIDIEvent(nil), e.midiEvents...)
}

// SignalInfo is the UI view of one generated analog source.
type SignalInfo struct {
	Pin       int
	Wave      string
	Hz        float64
	Amplitude float32
	Offset    float32
	Enabled   bool
}

// Signals returns the board's analog generators.
func (e *Emu) Signals() []SignalInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]SignalInfo, 0, len(e.signals))
	for _, s := range e.signals {
		out = append(out, SignalInfo{
			Pin: s.Pin, Wave: s.Wave.String(), Hz: s.Hz,
			Amplitude: s.Amplitude, Offset: s.Offset, Enabled: s.Enabled,
		})
	}
	return out
}

// SetSignalEnabled mutes or enables one generator.
func (e *Emu) SetSignalEnabled(pin int, on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.signals {
		if s.Pin == pin {
			s.Enabled = on
		}
	}
}

// SetSignalWave cycles one generator's waveform. name is sine, square,
// triangle, ramp or noise.
func (e *Emu) SetSignalWave(pin int, name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var wave devices.SignalWave
	switch name {
	case "square":
		wave = devices.WaveSquare
	case "triangle":
		wave = devices.WaveTriangle
	case "ramp":
		wave = devices.WaveRamp
	case "noise":
		wave = devices.WaveNoise
	default:
		wave = devices.WaveSine
	}
	for _, s := range e.signals {
		if s.Pin == pin {
			s.Wave = wave
		}
	}
}

// profileSampleCycles is the UI PC sampling period: about 14 us at 150 MHz,
// dense enough for a readable profile with modest overhead.
const profileSampleCycles = 2048

// Profiling reports whether the Profile tab is collecting.
func (e *Emu) Profiling() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.profWanted
}

// SetProfiling starts or stops profile collection. Starting discards the
// previous run; stopping keeps its report frozen until the next start.
func (e *Emu) SetProfiling(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profWanted = on
	e.profileCache = nil
	e.profileCacheAt = 0
	e.profileCacheEnd = false
	if e.chip == nil {
		return
	}
	if on {
		e.chip.StartProfile(rp2350.ProfileConfig{SampleCycles: profileSampleCycles, Coverage: true})
	} else {
		e.chip.StopProfile()
	}
}

// ProfileReport returns the built profile view, rebuilding it when enough new
// virtual time has passed. It returns nil when nothing has been collected yet
// or the firmware has no ELF debug info.
func (e *Emu) ProfileReport() *coverage.Report {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.chip == nil {
		return nil
	}
	data, ok := e.chip.ProfileData()
	if !ok {
		return nil
	}
	active := e.chip.Profiling()
	if e.profileCache != nil {
		if !active && e.profileCacheEnd {
			return e.profileCache
		}
		if active && data.ElapsedCycles >= e.profileCacheAt && data.ElapsedCycles-e.profileCacheAt < 5_000_000 {
			return e.profileCache
		}
	}
	in := coverage.Input{
		Counts:        data.Coverage,
		Samples:       data.Samples,
		SampleCycles:  data.SampleCycles,
		ElapsedCycles: data.ElapsedCycles,
		IRQHist:       data.IRQHist[:],
		IRQSlowest:    data.IRQSlowest,
		IRQCount:      data.IRQCount,
	}
	for i := range data.Stack {
		in.Stack[i] = coverage.StackWater{
			MSPMin: data.Stack[i].MSPMin, PSPMin: data.Stack[i].PSPMin,
			MSPValid: data.Stack[i].MSPValid, PSPValid: data.Stack[i].PSPValid,
		}
	}
	var src coverage.Symbolizer
	if e.debugInfo != nil {
		src = e.debugInfo
	}
	rep := coverage.Build(src, in, func(addr uint32, size int) ([]byte, bool) {
		return e.chip.PeekBytes(addr, size)
	})
	e.profileCache = rep
	e.profileCacheAt = data.ElapsedCycles
	e.profileCacheEnd = !active
	return rep
}

// HIDEvents returns the recent decoded USB HID reports.
func (e *Emu) HIDEvents() []HIDEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]HIDEvent(nil), e.hidEvents...)
}

// ClearHIDEvents drops the HID monitor history.
func (e *Emu) ClearHIDEvents() {
	e.mu.Lock()
	e.hidEvents = e.hidEvents[:0]
	e.mu.Unlock()
}

// SendHID queues a host-to-device HID report (keyboard LEDs, gamepad
// feedback). It is flushed on the emulator thread when the OUT endpoint is
// armed.
func (e *Emu) SendHID(data []byte) {
	if len(data) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.hidQueue) < 64 {
		e.hidQueue = append(e.hidQueue, append([]byte(nil), data...))
	}
}

// flushHIDLocked delivers queued host-to-device HID reports. The caller holds
// e.mu.
func (e *Emu) flushHIDLocked() {
	for len(e.hidQueue) > 0 && e.chip != nil && e.chip.USBHIDReady() {
		if !e.chip.SendUSBHID(e.hidQueue[0]) {
			return
		}
		e.hidQueue = e.hidQueue[1:]
	}
}

// SendMIDI queues a host-to-device message. It is flushed on the emulator
// thread so the USB DPRAM is only touched while the chip is locked there.
func (e *Emu) SendMIDI(m midi.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.midiPending) < 64 {
		e.midiPending = append(e.midiPending, m)
	}
}

// FlushMIDI delivers queued host-to-device messages immediately. Board changes
// use it so Note-Off messages cannot be delivered to the newly selected board.
func (e *Emu) FlushMIDI() {
	e.mu.Lock()
	e.flushMIDILocked()
	e.mu.Unlock()
}

// flushMIDILocked delivers queued messages. The caller holds e.mu.
func (e *Emu) flushMIDILocked() {
	if len(e.midiPending) == 0 || e.chip == nil {
		return
	}
	pending := e.midiPending
	e.midiPending = nil
	for _, m := range pending {
		switch e.midiKind {
		case "usb":
			pkt := midi.USBMessage(m)
			if !e.chip.SendUSBMIDI(pkt[:]) {
				// The firmware has not armed the endpoint yet; retry later.
				e.midiPending = append(e.midiPending, m)
			}
		case "uart":
			e.chip.SendUART(e.midiUART, m.Bytes())
		}
	}
}

// attachBLELocked binds the virtual controller to the loaded image. It runs
// after Boot so the CYW43 hooks sit on top of the final firmware image.
func (e *Emu) attachBLELocked() error {
	if e.board == nil || e.board.BLE == nil || e.debugInfo == nil {
		return nil
	}
	d := e.board.BLE
	addr, err := ble.ParseAddress(d.Addr)
	if err != nil {
		return err
	}
	ctrl := ble.New(e.chip, ble.Options{
		HostWake: d.HostWake,
		Address:  addr,
		Seed:     d.Seed,
	})
	if err := ble.BindCYW43(e.chip, e.debugInfo, ctrl); err != nil {
		return err
	}
	ctrl.SetObserver(func(dir string, pkt []byte) {
		if len(pkt) == 0 {
			return
		}
		what := "HCI"
		switch pkt[0] {
		case 0x01:
			what = fmt.Sprintf("cmd %#04x", uint16(pkt[1])|uint16(pkt[2])<<8)
		case 0x04:
			what = fmt.Sprintf("evt %#02x", pkt[1])
		case 0x02:
			what = "acl"
		}
		e.bleEvents = append(e.bleEvents, fmt.Sprintf("%8.2f ms %s %s (%d B)",
			float64(e.chip.Cycles)/150_000, dir, what, len(pkt)))
		if len(e.bleEvents) > 500 {
			e.bleEvents = e.bleEvents[len(e.bleEvents)-500:]
		}
	})
	e.ble = ctrl
	return nil
}

func (e *Emu) loadLocked() error {
	var backing []byte
	if e.flashPath != "" {
		data, err := os.ReadFile(e.flashPath)
		switch {
		case err == nil:
			if len(data) > rp2350.FlashSize {
				return fmt.Errorf("flash image is %d bytes, limit is %d", len(data), rp2350.FlashSize)
			}
			backing = data
		case os.IsNotExist(err) && e.img != nil:
			// A missing backing file is a fresh erased card when firmware is
			// supplied, matching the headless runner.
		default:
			return fmt.Errorf("flash load: %w", err)
		}
	}

	e.resetChip()
	if backing != nil {
		e.chip.LoadFlashImage(backing)
	}
	if e.img == nil {
		e.applyInputsLocked()
		return nil
	}
	for _, seg := range e.img.Segments {
		if err := e.chip.ValidateSegment(seg.Addr, seg.Data); err != nil {
			return err
		}
	}
	for _, seg := range e.img.Segments {
		if err := e.chip.LoadSegment(seg.Addr, seg.Data); err != nil {
			return err
		}
	}
	// Apply before Boot so a held BOOTSEL is sampled by the boot path, then
	// again because Boot resets the pin model.
	e.applyInputsLocked()
	e.chip.Boot()
	e.applyInputsLocked()
	if err := e.attachBLELocked(); err != nil {
		return err
	}
	e.lastBoots = e.chip.BootCount()
	e.bootCount = e.lastBoots
	e.flashWrites = 0
	e.lastChipFW = e.chip.FlashWrites()
	e.console = e.console[:0]
	e.consolePos = 0
	return nil
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
	e.applyInputsLocked()
	e.chip.Boot()
	e.applyInputsLocked()
	if err := e.attachBLELocked(); err != nil {
		e.errText = err.Error()
	}
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

// saveOTPLocked writes the current fuse rows to the board's persist target
// when a fuse or lock changed. The caller holds e.mu.
func (e *Emu) saveOTPLocked() {
	path := e.otpPersist
	if path == "" || e.chip == nil {
		return
	}
	version := e.chip.OTPVersion()
	writes := e.chip.OTPWrites()
	if version == e.otpPersistVersion && writes == e.otpPersistWrites {
		return
	}
	contents := otpfile.ContentsOf(e.chip)
	e.otpPersistVersion = version
	e.otpPersistWrites = writes
	if err := otpfile.Write(path, contents); err != nil {
		slog.Error("otp persist", slog.String("path", path), slog.Any("err", err))
	}
}

// SaveOTP persists the current fuse rows.
func (e *Emu) SaveOTP() {
	e.mu.Lock()
	e.saveOTPLocked()
	e.mu.Unlock()
}

// SyncSD flushes a host-backed SD card so writes made just before the window
// closes are not lost.
func (e *Emu) SyncSD() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sd != nil {
		_ = e.sd.Sync()
	}
}

// LoadFlash boots the vector table from a raw flash image without loading an
// ELF. It is used to resume after a previous self-update.
func (e *Emu) LoadFlash(path string) error {
	return e.loadFlash(path, true, false)
}

// LoadFlashPaused loads a raw flash image without starting execution.
func (e *Emu) LoadFlashPaused(path string) error {
	return e.loadFlash(path, false, false)
}

func (e *Emu) loadFlash(path string, start, preservePause bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > rp2350.FlashSize {
		return fmt.Errorf("flash image is %d bytes, limit is %d", len(data), rp2350.FlashSize)
	}
	head, err := rp2350.BootVector(data)
	if err != nil {
		return err
	}
	check := &firmware.Image{Format: "flash", Segments: []firmware.Segment{{Addr: rp2350.FlashBase, Data: head}}}
	if err := check.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	wasRunning, paused := e.runStateLocked()
	e.img = nil
	e.debugInfo = nil
	e.fwPath = path
	e.errText = ""
	e.resetChip()
	e.chip.LoadFlashImage(data)
	e.applyInputsLocked()
	e.chip.Boot()
	e.applyInputsLocked()
	if err := e.attachBLELocked(); err != nil {
		return err
	}
	e.lastBoots = e.chip.BootCount()
	e.bootCount = e.lastBoots
	e.flashWrites = 0
	e.lastChipFW = e.chip.FlashWrites()
	e.console = e.console[:0]
	e.consolePos = 0
	e.running = start
	if preservePause {
		e.running = wasRunning && !paused
		if !e.running && e.chip != nil && e.debugger != nil {
			e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
		}
	}
	return nil
}

// Load reads a firmware image and starts running it.
func (e *Emu) Load(path string) error {
	return e.loadFirmware(path, true, false)
}

// LoadPaused reads a firmware image without starting execution. Startup
// breakpoints are installed through this path before the first instruction.
func (e *Emu) LoadPaused(path string) error {
	return e.loadFirmware(path, false, false)
}

func (e *Emu) loadFirmware(path string, start, preservePause bool) error {
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
	var info *debug.Info
	if img.Format == "elf" {
		info, _ = debug.Load(path)
	}
	return e.installImage(img, path, info, start, preservePause)
}

// LoadBytes installs a firmware image already held in memory. The browser build
// has no filesystem, so the page fetches the image and hands it over.
func (e *Emu) LoadBytes(name string, data []byte, start bool) error {
	img, err := firmware.Parse(name, data)
	if err == nil {
		err = img.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize)
	}
	if err != nil {
		e.mu.Lock()
		e.errText = err.Error()
		e.mu.Unlock()
		return err
	}
	// The browser has no file path for debug.Load; parse the same bytes so a
	// picked ELF keeps its symbols and DWARF.
	var info *debug.Info
	if img.Format == "elf" {
		info, _ = debug.LoadBytes(name, data)
	}
	return e.installImage(img, name, info, start, false)
}

// installImage swaps in a parsed image and restarts the machine.
func (e *Emu) installImage(img *firmware.Image, name string, info *debug.Info, start, preservePause bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	wasRunning, paused := e.runStateLocked()
	e.img = img
	e.debugInfo = info
	e.fwPath = name
	e.errText = ""
	if err := e.loadLocked(); err != nil {
		e.errText = err.Error()
		return err
	}
	e.running = start
	if preservePause {
		e.running = wasRunning && !paused
		if !e.running && e.chip != nil && e.debugger != nil {
			e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
		}
	}
	return nil
}

func (e *Emu) runStateLocked() (running, paused bool) {
	running = e.running
	paused = e.chip != nil && e.chip.Debug != nil && e.chip.Debug.Stopped
	return running, paused
}

// Reload reads the firmware again and restarts it, preserving an intentional
// pause. The state decision and image swap share one lock, so observers never
// see a half-loaded machine temporarily running.
func (e *Emu) Reload(path string) error {
	return e.loadFirmware(path, false, true)
}

// ReloadFlash reloads a raw flash image, preserving an intentional pause.
func (e *Emu) ReloadFlash(path string) error {
	return e.loadFlash(path, false, true)
}

// RunFor synchronously advances the machine by a number of cycles. It is used
// by --png snapshots to render a meaningful frame without a live window.
func (e *Emu) RunFor(cycles uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.chip == nil || !e.chip.Booted() {
		return
	}
	e.debugger.Resume()
	const chunk = 500_000
	for cycles > 0 && !e.chip.Halted && !e.debugger.Stopped &&
		(!e.frozen.triggered || !e.trigger.Pause) {
		n := min(uint64(chunk), cycles)
		e.chip.Run(n)
		e.advanceScheduledInputsLocked()
		e.flushMIDILocked()
		e.flushUSBInputLocked()
		e.flushHIDLocked()
		e.checkScopeTriggerLocked()
		if e.chip.Hang != nil {
			e.running = false
			break
		}
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
		if e.chip != nil {
			e.chip.ClearHang()
			e.hang = nil
			e.hangText = ""
		}
		e.debugger.Resume()
	} else if e.chip != nil {
		e.debugger.Pause(0, e.chip.CPU0.R[15], e.chip.Cycles)
		e.slow = false
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

// runnableLocked reports whether the selected board has an executable image.
// A board can be inspected without firmware; the CPU then has no reset vector
// and must not execute the uninitialised boot ROM (or the empty flash mapping).
func (e *Emu) runnableLocked() bool {
	return e.board != nil && e.chip != nil && e.chip.Booted()
}

func (e *Emu) loop() {
	// Small chunks keep the mutex hold time low enough for a smooth UI while
	// still running the firmware as fast as the interpreter allows.
	chunk := webChunk()
	// A render request makes the UI thread take the emulator lock for a full
	// snapshot. Asking once per chunk (hundreds of times a second) starves
	// stepping without making the picture smoother, so cap repaints at
	// display rate. The browser page shim paces its own rAF loop by tab.
	const frameInterval = 16 * time.Millisecond
	var frameAt time.Time
	var lastYield time.Time
	for {
		started := time.Now()
		if shirei.GetHost().HeadlessRender {
			// Keep snapshots deterministic without spinning.
			time.Sleep(time.Millisecond)
			continue
		}
		e.mu.Lock()
		// The picker screen has no board yet; keep the machine idle so an
		// empty reset vector is not executed in the background. A debugger
		// stop (GDB breakpoint/watchpoint) also parks the chip.
		stopped := e.chip != nil && e.chip.Debug != nil && e.chip.Debug.Stopped
		running := e.running && e.runnableLocked() && !e.chip.Halted && !stopped
		frameEvery := e.frameEvery
		boots := e.lastBoots
		var sleepFor time.Duration
		if running {
			done := e.chip.Run(chunk)
			e.advanceScheduledInputsLocked()
			e.flushMIDILocked()
			e.flushUSBInputLocked()
			e.flushHIDLocked()
			e.checkScopeTriggerLocked()
			if e.chip.Hang != nil {
				e.running = false
			}
			boots = e.chip.BootCount()
			if e.debugger.Stopped {
				e.running = false
			}
			sleepFor = e.paceLocked(done, started)
		} else {
			e.paceStart = time.Time{}
			e.speedStart = time.Time{}
			e.speedCycles = 0
			e.slow = false
		}
		saveFlash := false
		if boots != e.lastBoots {
			if boots > e.lastBoots {
				e.bootCount += boots - e.lastBoots
			} else {
				e.bootCount++
			}
			e.lastBoots = boots
			saveFlash = true
		}
		saveOTP := false
		if e.chip != nil {
			if v := e.chip.OTPVersion(); v != e.lastOTPVersion {
				e.lastOTPVersion = v
				saveOTP = true
			}
		}
		e.mu.Unlock()
		yieldEvery := min(frameEvery, 50*time.Millisecond)
		if yieldEvery <= 16*time.Millisecond {
			yieldEvery = 8 * time.Millisecond
		}
		if running && time.Since(frameAt) >= frameInterval {
			frameAt = time.Now()
			shirei.RequestNextFrame()
		}
		if saveFlash {
			e.SaveFlash()
		}
		if saveOTP {
			e.SaveOTP()
		}
		switch {
		case sleepFor > 0:
			// The machine can outrun the real part: wait so virtual time
			// tracks wall time.
			time.Sleep(sleepFor)
		case !running:
			// Paused, halted, or the board picker: nothing to step, so idle
			// instead of spinning.
			time.Sleep(2 * time.Millisecond)
		case time.Since(lastYield) >= yieldEvery:
			// Hand control back regularly. On a desktop this just yields the
			// goroutine; in the browser the runtime must return to the page's
			// event loop or nothing paints.
			lastYield = time.Now()
			webYield()
		}
	}
}

// cycleDuration converts virtual cycles to wall-clock duration at the system
// clock.
func cycleDuration(cycles uint64) time.Duration {
	return time.Duration(float64(cycles) / float64(rp2350.SysClkHz) * float64(time.Second))
}

// paceSleep returns how long the loop must wait to keep virtual time level
// with wall time: positive when the machine is ahead, zero when it is behind.
func paceSleep(virtual, wall time.Duration) time.Duration {
	const maxSleep = 20 * time.Millisecond
	if virtual <= wall {
		return 0
	}
	return min(virtual-wall, maxSleep)
}

// paceLocked accounts one stepped chunk and returns the sleep that keeps the
// virtual clock level with wall time. The speed ratio is tracked in both modes;
// turbo only skips the wait. The caller holds e.mu.
func (e *Emu) paceLocked(cycles uint64, started time.Time) time.Duration {
	if e.paceStart.IsZero() {
		e.paceStart = started
		e.paceCycles = 0
		e.speedStart = started
		e.speedCycles = 0
		e.speed = 1
		e.slow = false
	}
	e.paceCycles += cycles
	e.speedCycles += cycles
	virtual := cycleDuration(e.paceCycles)
	wall := time.Since(e.paceStart)
	// The indicator uses a short window, not the lifetime average: a GC pause
	// or a heavy phase must stop affecting it once the machine catches up.
	if window := time.Since(e.speedStart); window >= time.Second {
		e.speed = cycleDuration(e.speedCycles).Seconds() / window.Seconds()
		e.speedStart = time.Now()
		e.speedCycles = 0
	}
	if e.turbo {
		return 0
	}
	return paceSleep(virtual, wall)
}

// SetFrameInterval records the visible tab's UI rebuild period.
func (e *Emu) SetFrameInterval(d time.Duration) {
	e.mu.Lock()
	e.frameEvery = d
	e.mu.Unlock()
}

// SetTurbo disables real-time pacing: the machine runs as fast as the host
// allows instead of keeping virtual time level with wall time.
func (e *Emu) SetTurbo(on bool) {
	e.mu.Lock()
	e.turbo = on
	e.paceStart = time.Time{}
	e.speedStart = time.Time{}
	e.speedCycles = 0
	e.speed = 1
	e.slow = false
	e.mu.Unlock()
}

// Snapshot returns a consistent view of the machine for one frame.
// GPSInfo returns the attached GPS state, if any.
func (e *Emu) GPSInfo() *devices.GPSInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gpsDev == nil {
		return nil
	}
	info := e.gpsDev.Info()
	return &info
}

// SetGPSField changes one GPS value without disturbing the others.
func (e *Emu) SetGPSField(field string, value float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gpsDev == nil {
		return fmt.Errorf("no GPS attached")
	}
	return e.gpsDev.SetField(field, value)
}

// ClearGPSLog drops the NMEA sentence history.
func (e *Emu) ClearGPSLog() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gpsDev != nil {
		e.gpsDev.ClearHistory()
	}
}

// ClearDeviceLogs drops the Bus tab's direct-device transaction log.
func (e *Emu) ClearDeviceLogs() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deviceLog = nil
}

// CANInfo returns the controller and recent bus frames, if attached.
func (e *Emu) CANInfo() (devices.MCP2515Info, []devices.CANFrame, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.canDev == nil || e.canBus == nil {
		return devices.MCP2515Info{}, nil, false
	}
	return e.canDev.Info(), e.canBus.Frames(), true
}

// TraceEvents returns a copy of the ITM/DWT trace ring.
func (e *Emu) TraceEvents() []rp2350.TraceEvent {
	e.traceMu.Lock()
	defer e.traceMu.Unlock()
	return append([]rp2350.TraceEvent(nil), e.traceEvents...)
}

// ClearTrace drops the current trace ring.
func (e *Emu) ClearTrace() {
	e.traceMu.Lock()
	e.traceEvents = e.traceEvents[:0]
	e.traceMu.Unlock()
}

func (e *Emu) recordTrace(ev rp2350.TraceEvent) {
	e.traceMu.Lock()
	e.traceEvents = append(e.traceEvents, ev)
	if len(e.traceEvents) > 2000 {
		e.traceEvents = e.traceEvents[len(e.traceEvents)-2000:]
	}
	e.traceMu.Unlock()
}

func formatBLEAddr(a [6]byte) string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", a[0], a[1], a[2], a[3], a[4], a[5])
}

func (e *Emu) Snapshot() MachineState {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s MachineState
	s.Cycles = e.chip.Cycles
	s.PC = e.chip.CPU0.R[15]
	s.SP = e.chip.CPU0.R[13]
	s.Halted = e.chip.Halted
	s.Running = e.running && e.runnableLocked() && e.chip.Hang == nil
	if e.chip.Debug != nil && e.chip.Debug.Stopped {
		s.Running = false
	}
	s.Pacing = !e.turbo
	s.Speed = e.speed
	// Report "too slow" only once the run has had time to settle, with
	// hysteresis so the status line does not flicker around the threshold.
	if s.Pacing && s.Running && !e.paceStart.IsZero() && time.Since(e.paceStart) > time.Second {
		if !e.slow && e.speed < 0.90 {
			e.slow = true
		} else if e.slow && e.speed > 0.97 {
			e.slow = false
		}
		s.Slow = e.slow
	} else if !s.Running {
		e.slow = false
	}
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
	if gen := e.chip.HSTXSerial(); gen != e.dviGen {
		e.dviGen = gen
		e.dviFrame = e.chip.HSTXFrame()
	}
	s.DVI = e.dviFrame
	s.GPIOs = e.chip.GPIOs()
	s.Package = "RP2350A"
	if e.chip.Package() == rp2350.PackageQFN80 {
		s.Package = "RP2350B"
	}
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
		if g := e.lcd.VisualGeneration(); g != e.lcdGen || duty != e.lcdDuty {
			e.lcd.Brightness = duty
			e.lcdFrame = e.lcd.Framebuffer()
			e.lcdGen = g
			e.lcdDuty = duty
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
	if v := e.chip.OTPVersion(); v != e.otpGen || e.otpRows == nil {
		e.otpGen = v
		e.otpRows = e.chip.OTPRowsCopy()
	}
	otp := &OTPState{
		Rows:       e.otpRows,
		Writes:     e.chip.OTPWrites(),
		LastWrite:  e.chip.OTPLastWriteRow(),
		SeedSource: e.otpSeedSource,
		SeedRows:   e.otpSeeded,
		SeedPage:   e.otpSeedPage,
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
	s.Hang = e.hangText
	if infos := e.chip.USBHIDInterfaces(); len(infos) > 0 {
		s.HID = make([]HIDDeviceState, 0, len(infos))
		for _, info := range infos {
			s.HID = append(s.HID, HIDDeviceState{
				Interface:        info.Interface,
				Kinds:            append([]string(nil), info.Kinds...),
				Protocol:         info.Protocol,
				Subclass:         info.Subclass,
				InEndpoint:       info.InEndpoint,
				OutEndpoint:      info.OutEndpoint,
				ReportID:         info.ReportID,
				KeyboardReportID: info.KeyboardReportID,
				OutputReportID:   info.OutputReportID,
			})
		}
	}
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
	if e.audio != nil {
		cfg := e.audio.Config()
		s.AudioRate = cfg.Rate
		s.AudioChannels = cfg.Channels
		s.AudioSamples = e.audio.Len()
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
	s.DeviceLogs = append([]string(nil), e.deviceLog...)
	if e.ble != nil {
		st := e.ble.Status()
		s.BLE = &BLEState{
			Address:       formatBLEAddr(st.Address),
			Connected:     st.Connected,
			Peer:          formatBLEAddr(st.Peer),
			MTU:           st.MTU,
			Notifications: st.Notifications,
			Pending:       st.Pending,
			Events:        append([]string(nil), e.bleEvents...),
		}
	}
	if e.cyw != nil && e.board != nil && e.board.Wifi != nil {
		w := e.board.Wifi
		s.CYW = &CYWState{
			FirmwareUp: e.cyw.FirmwareUp(),
			LinkUp:     e.cyw.LinkUp(),
			SSID:       e.cyw.SSID(),
			LED:        e.cyw.LED(),
			MAC:        w.MAC,
			Pins: fmt.Sprintf("WL_REG_ON GP%d · WL_D GP%d · WL_CS GP%d · WL_CLK GP%d",
				w.RegOn, w.Data, w.CS, w.CLK),
		}
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
	s.Buses = append(s.Buses, e.presetErr...)
	if b := e.board; b != nil {
		for _, d := range b.I2CPresets {
			s.Buses = append(s.Buses, fmt.Sprintf("I2C%d preset %s @0x%02x", d.Bus, d.Preset, d.Addr))
		}
		for _, d := range b.SPIPresets {
			s.Buses = append(s.Buses, fmt.Sprintf("SPI%d CS%d preset %s", d.Bus, d.CS, d.Preset))
		}
	}
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
	if len(e.inputs) > 0 {
		s.Inputs = make(map[string]InputValue, len(e.inputs))
		maps.Copy(s.Inputs, e.inputs)
	}
	s.BootselHeld = e.bootselHeld
	if e.chip != nil {
		s.BootselAtBoot = e.chip.BootselAtBoot()
	}
	return s
}

// SendInput forwards host bytes to UART0 immediately and queues them for USB
// CDC until the emulated host has enumerated and armed the OUT endpoint.
func (e *Emu) SendInput(data []byte) {
	if len(data) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	const maxQueued = 64 * 1024
	if len(data) > maxQueued {
		data = data[len(data)-maxQueued:]
	}
	e.usbInput = append(e.usbInput, data...)
	if len(e.usbInput) > maxQueued {
		e.usbInput = append([]byte(nil), e.usbInput[len(e.usbInput)-maxQueued:]...)
	}
	e.chip.SendUART(0, data)
	for _, b := range data {
		e.addUART(0, e.chip.Cycles, true, b)
	}
	e.flushUSBInputLocked()
}

// flushUSBInputLocked publishes queued console bytes in endpoint-sized packets.
// The caller holds e.mu.
func (e *Emu) flushUSBInputLocked() {
	sent := 0
	for sent < len(e.usbInput) && e.chip != nil && e.chip.USBCDCReady() {
		n := min(64, len(e.usbInput)-sent)
		packet := e.usbInput[sent : sent+n]
		e.chip.SendUSBCDC(packet)
		e.addUSBEvent(e.chip.Cycles, false, packet)
		sent += n
	}
	if sent == 0 {
		return
	}
	if sent == len(e.usbInput) {
		e.usbInput = nil
		return
	}
	e.usbInput = append([]byte(nil), e.usbInput[sent:]...)
}
