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
	e.loadLocked()
}

// resetChip builds a fresh machine and attaches the selected board's devices.
func (e *Emu) resetChip() {
	c := rp2350.New()
	e.lcd = nil
	e.ws = nil
	e.ethKind = ""
	e.ethLog = e.ethLog[:0]
	logEth := func(format string, args ...any) {
		e.ethLog = append(e.ethLog, fmt.Sprintf(format, args...))
		if len(e.ethLog) > 64 {
			e.ethLog = e.ethLog[len(e.ethLog)-32:]
		}
	}

	if b := e.board; b != nil {
		if d := b.LCD; d != nil {
			e.lcd = devices.NewST7789(c, d.CS, d.DC, d.RST)
			if d.SPI == 1 {
				c.OnSPI1 = e.lcd.Transfer
			} else {
				c.OnSPI0 = e.lcd.Transfer
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
				c.OnSPI1 = transfer
			} else {
				c.OnSPI0 = transfer
			}
			c.CycleHooks = append(c.CycleHooks, tick)
		}
		if d := b.WS2812; d != nil {
			e.ws = devices.NewWS2812B(c, d.Pin)
			c.CycleHooks = append(c.CycleHooks, e.ws.Tick)
		}
	}

	appendBytes := func(b []byte) {
		e.console = append(e.console, b...)
		e.consolePos += int64(len(b))
		// Keep the tail; the UI resets its view when the length drops.
		if len(e.console) > 64*1024 {
			e.console = append([]byte(nil), e.console[len(e.console)-32*1024:]...)
		}
	}
	c.OnUART0 = func(b byte) { appendBytes([]byte{b}) }
	c.OnUART1 = func(b byte) { appendBytes([]byte{b}) }
	c.SetUSBCDC(appendBytes)

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
	e.mu.Unlock()
}
