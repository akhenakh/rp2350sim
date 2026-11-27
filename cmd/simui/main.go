package main

import (
	"flag"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/otpfile"
	"github.com/akhenakh/rp2350sim/internal/rp2350"

	app "go.hasen.dev/shirei/app"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// version is set by GoReleaser with -X main.version.
var version = "dev"

var (
	emu     = NewEmu()
	fwPath  = ""
	pinView = 0 // 0: all pins, 1: configured only
	console = NewTextRing(256 * 1024)
	seenPos int64

	boards             []BoardDef
	selectedBoard      *BoardDef
	pendingBoardPicker bool
	inputArgs          []string
	activeTab          = tabBoard
	frameTab           = -1
	scopePins          []int
	scopeZoom          = 1.0
	scopeRangeIdx      = 4
	scopeFFT           bool
	scopeFFTChannel    int
	scopeFFTLog        = true
	scopeAnalog        = map[int]bool{}
	scopeAnalyzer      scopeSpectrum
	triggerKind        string
	triggerPause       bool
	logicSaveMsg       string
	logicSaveErr       bool
	busTerms           = map[string]*BusTerminal{}
	busTermErrs        = map[string]string{}
	busInput           string
	busSel             int
	busPluginSel       int
	busAutoConnect     bool
	busAnswers         = map[string]string{}
	touchDemo          bool
	touchDemoStart     time.Time
	touchDemoAt        time.Time
	pendingTouch       bool
	pendingTouchX      int
	pendingTouchY      int

	watchEnabled  bool
	flashMode     bool
	firmwareWatch *firmwareWatcher
)

// Scope voltage ranges in volts (full scale, from 0).
var scopeRanges = []float32{0.5, 1, 1.65, 2, 3.3, 5}

// Tabs. Board and Console always exist; the others appear only when the
// selected board defines the device.
const (
	tabBoard = iota
	tabLCD
	tabDVI
	tabEth
	tabWS
	tabSensors
	tabScope
	tabLogic
	tabAudio
	tabMIDI
	tabOTP
	tabBus
	tabPlugins
	tabUART
	tabDebug
	tabProfile
	tabHID
	tabBootsel
	tabGPS
	tabCAN
	tabBLE
	tabTrace
	tabConsole
)

// otpPage is the OTP page shown in the OTP tab; -1 follows the last
// programmed row, or page 0 when nothing was written.
var otpPage = -1

var funcNames = []string{"JTAG", "SPI", "UART", "I2C", "PWM", "SIO", "PIO0", "PIO1", "PIO2"}

func funcName(f uint8) string {
	if int(f) < len(funcNames) {
		return funcNames[f]
	}
	if f == 0x1f {
		return "—"
	}
	return fmt.Sprintf("F%d", f)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func levelName(high bool) string {
	if high {
		return "HIGH"
	}
	return "LOW"
}

// quitRequested reports the platform quit chord: Cmd+Q on macOS and Ctrl+Q
// elsewhere, mirroring app.PrimaryMod.
func quitRequested() bool {
	return ActiveCombo() == Combo(KeyQ, PrimaryMod())
}

func RootView() {
	if quitRequested() {
		stopAudio()
		app.Quit()
	}

	// The panels paint dark surfaces, so use the dark scheme's light text.
	SetDarkMode(true)

	// Apply a board change requested by the toolbar last frame, so the frame
	// that handled the click renders a consistent tab set.
	if pendingBoardPicker {
		pendingBoardPicker = false
		returnToBoardPicker()
	}

	// The logic sampler runs at 2 MHz; only pay for it while its tab is on
	// screen. A trigger armed from the UI or the command line overrides this.
	emu.SetLogicTabActive(activeTab == tabLogic)
	if frameTab != activeTab {
		frameTab = activeTab
		every := webFrameInterval(activeTab)
		emu.SetFrameInterval(every)
		webSetFrameInterval(every)
	}
	if emu.Booted() {
		updateTouchInput()
	}
	snap := emu.Snapshot()
	updateConsole(snap)

	lightText := TextColor(220, 8, 92, 1)
	Container(Attrs(Viewport, Background(220, 8, 12, 1), AmendTextStyle(lightText)), func() {
		if selectedBoard == nil {
			boardPicker()
			return
		}
		toolbar(snap)
		if snap.Hang != "" {
			Container(Attrs(Row, CrossMid, Gap(10), Pad4(4, 10, 4, 10), Background(140, 40, 18, 1)), func() {
				Label("HANG", FontSize(12), FontWeight(700), TextColor(50, 20, 90, 1))
				Label(snap.Hang, FontSize(12), TextColor(50, 20, 95, 1))
			})
		}
		// The toolbar's Board… button clears the selection mid-frame; show
		// the picker instead of the tabs for this frame.
		if selectedBoard == nil {
			boardPicker()
			return
		}
		tabsBar(snap)
		content := func() {
			switch activeTab {
			case tabLCD:
				lcdPanel(snap)
			case tabDVI:
				dviPanel(snap)
			case tabEth:
				ethPanel(snap)
			case tabWS:
				ws2812Panel(snap)
			case tabSensors:
				sensorsPanel(snap)
			case tabScope:
				scopePanel()
			case tabLogic:
				logicPanel()
			case tabAudio:
				audioPanel(snap)
			case tabMIDI:
				midiPanel(snap)
			case tabOTP:
				otpPanel(snap)
			case tabBus:
				busPanel(snap)
			case tabPlugins:
				pluginsPanel(snap)
			case tabUART:
				uartPanel(snap)
			case tabDebug:
				debugPanel(snap)
			case tabProfile:
				profilePanel(snap)
			case tabGPS:
				gpsPanel()
			case tabCAN:
				canPanel()
			case tabBLE:
				blePanel(snap)
			case tabTrace:
				tracePanel()
			case tabHID:
				hidPanel(snap)
			case tabBootsel:
				bootselPanel(snap)
			case tabConsole:
				consolePanel(snap)
			default:
				boardAndPins(snap)
			}
		}
		Container(Attrs(Grow(1), Expand, Pad4(10, 10, 10, 10)), content)
	})
	firmwareBrowserModal()
	bootselBrowserModal()
}

// boardPicker is the startup screen: one card per YAML board definition.
func boardPicker() {
	Container(Attrs(Grow(1), Expand, Gap(10), Pad(28)), func() {
		Container(Attrs(CrossMid, Expand, Gap(4)), func() {
			Label("rp2350sim", FontSize(32), FontWeight(700))
			Label("RP2350 emulator · choose the board to emulate", FontSize(15))
		})
		// The board list scrolls: it grows as boards.yaml grows.
		Container(Attrs(Grow(1), Expand, Clip, Viewport, CrossMid), func() {
			ScrollOnInput()
			ScrollBars()
			cardW := float32(viewW) * 0.52
			if cardW < 780 {
				cardW = 780
			}
			if cardW > 1200 {
				cardW = 1200
			}
			Container(Attrs(Gap(10), FixWidth(cardW)), func() {
				for i := range boards {
					b := &boards[i]
					ContainerWithKey(b.ID, Attrs(Row, CrossMid, Expand, Gap(14), Pad(14), Corners(10),
						Background(220, 12, 10, 1), BorderWidth(1), BorderColor(220, 8, 28, 1)), func() {
						st := ProcessButtonEvents(false)
						if st.Hovered {
							ModAttrs(Background(220, 12, 15, 1), BorderColor(220, 20, 45, 1))
						}
						if st.Clicked {
							selectBoard(b)
						}
						Container(Attrs(Grow(1), Gap(3)), func() {
							Label(b.Name, FontSize(17), FontWeight(700))
							Label(b.Description, FontSize(12), TextColor(220, 6, 70, 1))
						})
						for _, badge := range boardBadges(b) {
							Label(badge, FontSize(11), TextColor(140, 30, 70, 1), FontWeight(600))
						}
						Label("→", FontSize(16))
					})
				}
			})
		})
		Container(Attrs(CrossMid, Expand), func() {
			Label("Board list: RP2350SIM_BOARDS=/path/to/boards.yaml", FontSize(11), TextColor(220, 5, 50, 1))
		})
	})
}

func boardBadges(b *BoardDef) []string {
	var out []string
	if b.LCD != nil {
		out = append(out, fmt.Sprintf("LCD %d×%d", b.LCD.Width, b.LCD.Height))
	}
	if b.Ethernet != nil {
		out = append(out, strings.ToUpper(b.Ethernet.Kind))
	}
	if b.WS2812 != nil {
		out = append(out, "WS2812B")
	}
	if b.PSRAM != nil {
		out = append(out, fmt.Sprintf("PSRAM %d MB", b.PSRAM.SizeMB))
	}
	if b.OTP != nil {
		out = append(out, "OTP")
	}
	if len(b.Analog) > 0 {
		out = append(out, "ANALOG")
	}
	if len(b.Serial) > 0 {
		out = append(out, "TCP SERIAL")
	}
	if len(b.I2CTCP) > 0 || len(b.SPITCP) > 0 {
		out = append(out, "TCP BUS")
	}
	if b.Wifi != nil {
		if b.Wifi.Kind == "cyw43439" {
			out = append(out, "CYW43439")
		} else {
			out = append(out, "WIFI (VIRTUAL)")
		}
	}
	if len(out) == 0 {
		out = append(out, "GPIO")
	}
	return out
}

func releaseBoardResources() {
	// Queue Note-Off/All-Notes-Off before SetBoard flushes them to the old
	// transport, then close UI terminals owned by the old board.
	panicMIDI()
	hidLED = 0
	hidSendHex = ""
	for _, term := range busTerms {
		term.Close()
	}
	busTerms = map[string]*BusTerminal{}
	busTermErrs = map[string]string{}
	busInput = ""
	busSel = 0
	busPluginSel = 0
}

func returnToBoardPicker() {
	releaseBoardResources()
	if firmwareWatch != nil {
		firmwareWatch.Stop()
		firmwareWatch = nil
	}
	clearInputViews()
	emu.SetBoard(nil)
	selectedBoard = nil
	activeTab = tabBoard
	scopePins = nil
	RequestNextFrame()
}

func selectBoard(b *BoardDef) {
	releaseBoardResources()
	clearInputViews()
	selectedBoard = b
	activeTab = tabBoard
	scopePins = b.ScopePins()
	emu.SetBoard(b)
	applyInputSpecs(b)
	if watchEnabled && fwPath != "" {
		mode := "firmware"
		if flashMode {
			mode = "flash"
		}
		watchFile(fwPath, mode)
	}
	if busAutoConnect {
		autoConnectBusTerminals()
	}
	RequestNextFrame()
}

// applyInputSpecs applies --input ID=VALUE specs to the selected board. It is
// used by headless snapshots and scripted demos; normal sessions use the
// Board tab widgets.
func applyInputSpecs(b *BoardDef) {
	if b == nil {
		return
	}
	for _, spec := range inputArgs {
		id, val, ok := strings.Cut(spec, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "--input %q: expected id=value\n", spec)
			continue
		}
		var in *InputDef
		for i := range b.Inputs {
			if b.Inputs[i].ID == id {
				in = &b.Inputs[i]
				break
			}
		}
		if in == nil {
			fmt.Fprintf(os.Stderr, "--input %q: board %s has no input %q\n", spec, b.ID, id)
			continue
		}
		switch in.Kind {
		case "button", "switch", "bootsel":
			on, err := parseOnOff(val)
			if err != nil {
				fmt.Fprintf(os.Stderr, "--input %q: %v\n", spec, err)
				continue
			}
			emu.SetInput(id, InputValue{On: on})
		case "adc":
			v, err := strconv.ParseFloat(strings.TrimSpace(val), 32)
			if err != nil {
				fmt.Fprintf(os.Stderr, "--input %q: %v\n", spec, err)
				continue
			}
			emu.SetInput(id, InputValue{Volts: float32(v)})
		case "encoder":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				fmt.Fprintf(os.Stderr, "--input %q: %v\n", spec, err)
				continue
			}
			emu.QueueEncoder(id, n)
		}
	}
}

func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "press", "pressed", "down":
		return true, nil
	case "off", "0", "false", "release", "released", "up":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off")
}

// autoConnectBusTerminals attaches the Bus tab's device side to every bridge
// listener. It exists for headless snapshots and scripted demos, where there is
// no pointer to press "Connect terminal"; normal sessions stay manual.
func autoConnectBusTerminals() {
	for _, info := range emu.BusBridges() {
		if info.Listen == "" {
			continue
		}
		if _, ok := busTerms[info.Listen]; ok {
			continue
		}
		t, err := NewBusTerminal(info.Listen)
		if err != nil {
			busTermErrs[info.Listen] = err.Error()
			continue
		}
		busTerms[info.Listen] = t
		delete(busTermErrs, info.Listen)
		if answer := busAnswers[strings.ToLower(info.Kind)]; answer != "" {
			t.Send(answer)
		}
	}
}

type busAnswerFlags map[string]string

func (f busAnswerFlags) String() string {
	var parts []string
	for kind, answer := range f {
		parts = append(parts, kind+"="+answer)
	}
	return strings.Join(parts, ",")
}

// Set accepts kind=response, where kind is i2c or spi and response is a list of
// Bus Pirate tokens. Answers are queued as soon as the terminal connects, so a
// headless run can show a complete request/response session.
func (f busAnswerFlags) Set(v string) error {
	kind, answer, ok := strings.Cut(v, "=")
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !ok || (kind != "i2c" && kind != "spi") || strings.TrimSpace(answer) == "" {
		return fmt.Errorf("expected i2c=TOKENS or spi=TOKENS, got %q", v)
	}
	f[kind] = strings.TrimSpace(answer)
	return nil
}

func findBoard(id string) *BoardDef {
	for i := range boards {
		if boards[i].ID == id {
			return &boards[i]
		}
	}
	return nil
}

// touchBootCycles is how much virtual time passes before a host-injected touch
// is delivered. The touch controller and the panel share a reset line, so a
// touch sent while the firmware is still in its driver init would be cleared;
// 100 ms clears the 60 ms init sequence in the bundled examples.
const touchBootCycles = 100 * 150_000 // 100 ms at 150 MHz

// updateTouchInput delivers host touches only once the firmware has booted and
// advanced virtual time. A pending --touch is held from that point; the demo
// contact starts moving then too.
func updateTouchInput() {
	if pendingTouch {
		pendingTouch = false
		emu.SetTouch(pendingTouchX, pendingTouchY, true)
	}
	if touchDemo {
		updateTouchDemo()
	}
}

// updateTouchDemo walks a contact around the panel so a recording shows a
// moving touch instead of a still card. It is a UI-only demo; the emulator
// itself stays deterministic.
func updateTouchDemo() {
	now := time.Now()
	if now.Sub(touchDemoAt) < 120*time.Millisecond {
		return
	}
	touchDemoAt = now
	if touchDemoStart.IsZero() {
		touchDemoStart = now
	}
	t := now.Sub(touchDemoStart).Seconds()
	x := int(120 + 70*math.Cos(t*1.1))
	y := int(160 + 110*math.Sin(t*0.85))
	emu.SetTouch(x, y, true)
}

func setTab(name string) {
	switch strings.ToLower(name) {
	case "", "board":
		activeTab = tabBoard
	case "lcd":
		if selectedBoard != nil && selectedBoard.LCD != nil {
			activeTab = tabLCD
		}
	case "dvi", "hstx":
		if selectedBoard != nil && selectedBoard.HSTX {
			activeTab = tabDVI
		}
	case "eth", "ethernet", "wifi", "wi-fi":
		if selectedBoard != nil && (selectedBoard.Ethernet != nil || selectedBoard.Wifi != nil) {
			activeTab = tabEth
		}
	case "ws", "leds", "ws2812":
		if selectedBoard != nil && selectedBoard.WS2812 != nil {
			activeTab = tabWS
		}
	case "sensors", "imu", "touch", "battery":
		if boardHasSensors(selectedBoard) {
			activeTab = tabSensors
		}
	case "gps", "nmea":
		if selectedBoard != nil && selectedBoard.GPS != nil {
			activeTab = tabGPS
		}
	case "scope", "osc":
		activeTab = tabScope
	case "logic", "la":
		activeTab = tabLogic
	case "audio":
		if selectedBoard != nil && selectedBoard.Audio != nil {
			activeTab = tabAudio
		}
	case "midi":
		if selectedBoard != nil && selectedBoard.MIDI != nil {
			activeTab = tabMIDI
		}
	case "otp":
		activeTab = tabOTP
	case "bus", "i2c", "spi", "bridges":
		activeTab = tabBus
	case "plugins", "plugin", "wasm", "script":
		activeTab = tabPlugins
	case "uart", "serial", "seriallog":
		activeTab = tabUART
	case "can", "canbus":
		if selectedBoard != nil && selectedBoard.CAN != nil {
			activeTab = tabCAN
		}
	case "ble", "bluetooth":
		if selectedBoard != nil && selectedBoard.BLE != nil {
			activeTab = tabBLE
		}
	case "debug", "dbg":
		activeTab = tabDebug
	case "profile", "prof", "coverage":
		activeTab = tabProfile
	case "trace", "itm", "swo":
		activeTab = tabTrace
	case "hid", "usb", "keyboard", "mouse", "gamepad":
		activeTab = tabHID
	case "bootsel", "rpi-rp2", "uf2":
		activeTab = tabBootsel
	case "console":
		activeTab = tabConsole
	}
}

// tabsBar lists the main view plus one tab per device on the selected board.
func tabsBar(snap MachineState) {
	if selectedBoard == nil {
		return
	}
	Container(Attrs(Row, CrossMid, Gap(4), Pad4(2, 10, 2, 10), Background(220, 10, 15, 1)), func() {
		tabButton("Board", tabBoard)
		if selectedBoard.LCD != nil {
			tabButton("LCD", tabLCD)
		}
		if selectedBoard.HSTX {
			tabButton("DVI", tabDVI)
		}
		if selectedBoard.Ethernet != nil {
			tabButton("Ethernet", tabEth)
		}
		if selectedBoard.Wifi != nil {
			tabButton("Wi-Fi", tabEth)
		}
		if selectedBoard.WS2812 != nil {
			tabButton("LEDs", tabWS)
		}
		if boardHasSensors(selectedBoard) {
			tabButton("Sensors", tabSensors)
		}
		tabButton("Scope", tabScope)
		tabButton("Logic", tabLogic)
		if selectedBoard.Audio != nil {
			tabButton("Audio", tabAudio)
		}
		if selectedBoard.MIDI != nil {
			tabButton("MIDI", tabMIDI)
		}
		tabButton("OTP", tabOTP)
		tabButton("Debug", tabDebug)
		tabButton("Profile", tabProfile)
		tabButton("Trace", tabTrace)
		tabButton("Bus", tabBus)
		if boardHasPlugins(selectedBoard) {
			tabButton("Plugins", tabPlugins)
		}
		if boardHasUART(selectedBoard, snap) {
			tabButton("UART", tabUART)
		}
		if selectedBoard.GPS != nil {
			tabButton("GPS", tabGPS)
		}
		if selectedBoard.CAN != nil {
			tabButton("CAN", tabCAN)
		}
		if selectedBoard.BLE != nil {
			tabButton("BLE", tabBLE)
		}
		if len(snap.HID) > 0 || activeTab == tabHID {
			tabButton("HID", tabHID)
		}
		if snap.BootselHeld || snap.Firmware == "" || activeTab == tabBootsel {
			tabButton("BOOTSEL", tabBootsel)
		}
		tabButton("Console", tabConsole)
	})
}

// boardHasSensors reports whether the board exposes on-board sensors worth
// their own tab.
func boardHasSensors(b *BoardDef) bool {
	return b != nil && (b.Touch != nil || b.IMU != nil || b.Battery != nil || b.SD != nil)
}

// boardHasPlugins reports whether the board loads any scripted device model.
func boardHasPlugins(b *BoardDef) bool {
	return b != nil && (len(b.I2CPlugins) > 0 || len(b.SPIPlugins) > 0 || len(b.UARTPlugins) > 0)
}

// boardHasUART reports whether the board has a UART worth a raw byte log.
func boardHasUART(b *BoardDef, snap MachineState) bool {
	if snap.UARTCount > 0 {
		return true
	}
	return b != nil && (len(b.UARTPlugins) > 0 || (b.Logic != nil && b.Logic.UART != nil))
}

func tabButton(label string, tab int) {
	Container(Attrs(Row, CrossMid, Pad2(6, 12), Corners(4)), func() {
		st := ProcessButtonEvents(false)
		if st.Clicked {
			activeTab = tab
			RequestNextFrame()
		}
		switch {
		case activeTab == tab:
			ModAttrs(Background(220, 14, 24, 1))
		case st.Hovered:
			ModAttrs(Background(220, 12, 18, 1))
		}
		Label(label, FontSize(13), FontWeight(600))
	})
}

func boardAndPins(snap MachineState) {
	Container(Attrs(Row, Grow(1), Expand, Gap(14)), func() {
		Container(Attrs(FixWidth(255), Grow(1), Expand, Clip, Viewport, Gap(10)), func() {
			ScrollOnInput()
			ScrollBars()
			boardPanel(snap)
			inputsPanel(snap)
		})
		pinsPanel(snap)
	})
}

// inputView keeps the UI-local state a widget needs across frames. The
// authoritative button/switch/ADC state lives in Emu; these fields are the
// drag accumulation and the value buffer ProcessSlider mutates.
type inputView struct {
	volts float32
}

var inputViews = map[string]*inputView{}

type encoderDrag struct {
	active bool
	y      float32
}

func clearInputViews() { inputViews = map[string]*inputView{} }

// snapInput returns a board input state, zero when the board has none.
func snapInput(snap MachineState, id string) InputValue {
	if snap.Inputs == nil {
		return InputValue{}
	}
	return snap.Inputs[id]
}

func inputViewFor(id string, v InputValue) *inputView {
	iv := inputViews[id]
	if iv == nil {
		iv = &inputView{volts: v.Volts}
		inputViews[id] = iv
	}
	return iv
}

// inputsPanel renders the board's interactive controls: momentary buttons,
// slide switches, rotary encoders, ADC pots/sliders and BOOTSEL.
func inputsPanel(snap MachineState) {
	if selectedBoard == nil || len(selectedBoard.Inputs) == 0 {
		return
	}
	Container(Attrs(Gap(5), Pad(8), Corners(8), Background(150, 18, 12, 1),
		BorderWidth(1), BorderColor(150, 25, 22, 1)), func() {
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Label("Inputs", FontSize(15), FontWeight(700))
			Filler(1)
			Label("click · drag · wheel", FontSize(10), TextColor(220, 6, 62, 1))
		})
		for _, in := range selectedBoard.Inputs {
			control := &in
			switch control.Kind {
			case "button":
				inputMomentary(*control, snap)
			case "bootsel":
				inputBootsel(*control, snap)
			case "switch":
				inputSwitch(*control, snap)
			case "encoder":
				inputEncoder(*control, snap)
			case "adc":
				inputADC(*control, snap)
			}
		}
	})
}

func inputMomentary(in InputDef, snap MachineState) {
	st := snapInput(snap, in.ID)
	pin := fmt.Sprintf("GP%d", in.Pin)
	Container(Attrs(Row, CrossMid, Gap(8), FixHeight(32), Pad2(3, 8), Corners(4),
		Background(220, 10, 20, 1), BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
		state := ProcessButtonEvents(false)
		// Only pointer transitions write the input; a value already held by
		// --input or the diagram must not be cancelled on the first frame.
		pressed := Use[bool]("input-press:" + in.ID)
		if state.Active && !*pressed {
			*pressed = true
			emu.SetInput(in.ID, InputValue{On: true})
			RequestNextFrame()
		} else if !state.Active && *pressed {
			*pressed = false
			emu.SetInput(in.ID, InputValue{On: false})
			RequestNextFrame()
		}
		on := st.On || state.Active
		if state.Hovered {
			ModAttrs(Background(220, 12, 26, 1))
		}
		if on {
			ModAttrs(Background(140, 45, 34, 1))
		}
		Container(Attrs(FixSize(34, 16), Corners(3),
			Background(boolHSLA(on))), func() {
			Label(pressLabel(on), FontSize(9), FontWeight(700),
				TextColor(220, 6, 86, 1))
		})
		Container(Attrs(Gap(0), Grow(1)), func() {
			Label(in.Label, FontSize(12), FontWeight(600))
			Label(pin, FontSize(10), Fonts(Monospace...), TextColor(220, 6, 62, 1))
		})
	})
}

// inputBootsel is a latching control: the real button is momentary, but a
// single mouse cannot hold it across a click on Reset. Latching it lets the
// boot sample see the button held; the board diagram remains momentary.
func inputBootsel(in InputDef, snap MachineState) {
	held := snap.BootselHeld
	Container(Attrs(Gap(1)), func() {
		Container(Attrs(Row, CrossMid, Gap(8), FixHeight(32), Pad2(3, 8), Corners(4),
			Background(220, 10, 20, 1), BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
			st := ProcessToggleEvents(&held, false)
			if st.Clicked {
				emu.SetBootsel(held)
				RequestNextFrame()
			}
			if st.Hovered {
				ModAttrs(Background(220, 12, 26, 1))
			}
			if held {
				ModAttrs(Background(140, 45, 34, 1))
			}
			Container(Attrs(FixSize(34, 16), Corners(3), Background(boolHSLA(held))), func() {
				Label(bootselLabel(held), FontSize(9), FontWeight(700),
					TextColor(220, 6, 86, 1))
			})
			Container(Attrs(Gap(0), Grow(1)), func() {
				Label(in.Label, FontSize(12), FontWeight(600))
				Label("QSPI CS", FontSize(10), Fonts(Monospace...),
					TextColor(220, 6, 62, 1))
			})
			Label("latch", FontSize(9), TextColor(220, 6, 58, 1))
		})
		if snap.BootselAtBoot {
			Label("held at reset · USB boot not modelled", FontSize(9),
				TextColor(140, 40, 68, 1))
		} else {
			Label("latch to hold through Reset", FontSize(9),
				TextColor(220, 6, 58, 1))
		}
	})
}

func bootselLabel(held bool) string {
	if held {
		return "HELD"
	}
	return "OFF"
}

func pressLabel(active bool) string {
	if active {
		return "PRESS"
	}
	return "READY"
}

func boolHSLA(active bool) (h, s, l float32, a float32) {
	if active {
		return 140, 55, 42, 1
	}
	return 220, 12, 32, 1
}

func inputSwitch(in InputDef, snap MachineState) {
	on := snapInput(snap, in.ID).On
	Container(Attrs(Row, CrossMid, Gap(8), FixHeight(32), Pad2(3, 8), Corners(4),
		Background(220, 10, 20, 1), BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
		st := ProcessToggleEvents(&on, false)
		if st.Clicked {
			emu.SetInput(in.ID, InputValue{On: on})
			RequestNextFrame()
		}
		if st.Hovered {
			ModAttrs(Background(220, 12, 26, 1))
		}
		// Track and knob.
		Container(Attrs(FixSize(34, 16), Corners(8),
			Background(switchTrackHSLA(on))), func() {
			x := float32(2)
			if on {
				x = 34 - 14
			}
			Container(Attrs(Float(x, 2), FixSize(12, 12), Corners(6),
				Background(220, 8, 82, 1)), func() {})
		})
		Container(Attrs(Gap(0), Grow(1)), func() {
			Label(in.Label, FontSize(12), FontWeight(600))
			Label(fmt.Sprintf("GP%d", in.Pin), FontSize(10), Fonts(Monospace...),
				TextColor(220, 6, 62, 1))
		})
		Label(switchLabel(on), FontSize(10), FontWeight(700),
			TextColor(switchTextHSLA(on)))
	})
}

func switchLabel(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

func switchTrackHSLA(on bool) (h, s, l float32, a float32) {
	if on {
		return 140, 45, 34, 1
	}
	return 220, 10, 30, 1
}

func switchTextHSLA(on bool) (h, s, l, a float32) {
	if on {
		return 140, 60, 70, 1
	}
	return 220, 5, 58, 1
}

func inputADC(in InputDef, snap MachineState) {
	st := snapInput(snap, in.ID)
	iv := inputViewFor(in.ID, st)
	v := iv.volts
	Container(Attrs(Gap(2), Pad(3), Corners(4), Background(220, 10, 20, 1),
		BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Label(in.Label, FontSize(12), FontWeight(600))
			Label(fmt.Sprintf("GP%d", in.Pin), FontSize(10), Fonts(Monospace...),
				TextColor(220, 6, 62, 1))
			Filler(1)
			Label(fmt.Sprintf("%.2f V", v), FontSize(11), Fonts(Monospace...),
				TextColor(140, 40, 72, 1))
		})
		Container(Attrs(FixWidth(220), FixHeight(20)), func() {
			sl := ProcessSlider(&v, SliderConfig{
				Min: float32(in.Min), Max: float32(in.Max),
				Step: float32(in.Step), Width: 220, HandleInset: 7,
			})
			Container(Attrs(Float(0, 6), FixSize(sl.Width, 6), Corners(3),
				Background(220, 8, 28, 1)), func() {})
			Container(Attrs(Float(0, 6), FixSize(max(sl.T*sl.Width, 2), 6), Corners(3),
				Background(140, 45, 42, 1)), func() {})
			Container(Attrs(Float(sl.HandleX, 3), FixSize(14, 14), Corners(7),
				Background(boolHSLA(sl.Active || sl.Hovered))), func() {})
		})
	})
	iv.volts = v
	if math.Abs(float64(v-st.Volts)) > 1e-4 {
		emu.SetInput(in.ID, InputValue{Volts: v})
		RequestNextFrame()
	}
}

func inputEncoder(in InputDef, snap MachineState) {
	st := snapInput(snap, in.ID)
	delta := 0
	Container(Attrs(Row, CrossMid, Gap(8), Pad(3), Corners(4), Background(220, 10, 20, 1),
		BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
		Container(Attrs(FixSize(42, 42), Corners(21), Background(220, 10, 24, 1),
			BorderWidth(1), BorderColor(220, 10, 36, 1)), func() {
			btn := ProcessButtonEvents(false)
			if btn.Hovered {
				ModAttrs(Background(220, 12, 30, 1))
				if sc := GetFrameInput().Scroll[1]; sc > 0 {
					delta = 1
				} else if sc < 0 {
					delta = -1
				}
			}
			drag := Use[encoderDrag]("enc-drag:" + in.ID)
			if btn.Active {
				if !drag.active {
					drag.active = true
					drag.y = btn.Local[1]
				}
				if dy := btn.Local[1] - drag.y; dy >= 10 {
					delta = 1
					drag.y = btn.Local[1]
				} else if dy <= -10 {
					delta = -1
					drag.y = btn.Local[1]
				}
				ModAttrs(Background(220, 12, 32, 1))
			} else {
				drag.active = false
			}
			// Indicator dot: one quarter turn per quadrature phase.
			ang := float64(st.Phase) * math.Pi / 8
			dx := 21 + 12*math.Sin(ang) - 3
			dy := 21 - 12*math.Cos(ang) - 3
			Container(Attrs(Float(float32(dx), float32(dy)), FixSize(6, 6), Corners(3),
				Background(140, 60, 62, 1)), func() {})
		})
		Container(Attrs(Gap(1), Grow(1)), func() {
			Label(in.Label, FontSize(12), FontWeight(600))
			Label(fmt.Sprintf("GP%d / GP%d", in.PinA, in.PinB), FontSize(10),
				Fonts(Monospace...), TextColor(220, 6, 62, 1))
			Container(Attrs(Row, Gap(4)), func() {
				if inputStepButton("‹") {
					delta = -1
				}
				if inputStepButton("›") {
					delta = 1
				}
			})
		})
	})
	if delta != 0 {
		emu.StepEncoder(in.ID, delta)
		RequestNextFrame()
	}
}

// inputStepButton is a tiny momentary arrow; it reports a completed click.
func inputStepButton(label string) bool {
	clicked := false
	Container(Attrs(FixSize(22, 18), Corners(3), Background(220, 12, 28, 1),
		Center), func() {
		st := ProcessButtonEvents(false)
		if st.Hovered {
			ModAttrs(Background(220, 14, 34, 1))
		}
		if st.Clicked {
			clicked = true
		}
		Label(label, FontSize(14), FontWeight(700))
	})
	return clicked
}

func toolbar(snap MachineState) {
	Container(Attrs(Row, CrossMid, Gap(8), Pad2(10, 10), Background(220, 10, 16, 1)), func() {
		Label("rp2350sim", FontSize(18), FontWeight(700))
		Label("Cortex-M33 · RP2350", FontSize(12))
		Label(selectedBoard.Name, FontSize(12), FontWeight(600), TextColor(140, 40, 70, 1))
		Filler(1)

		attrs := DefaultTextInputAttrs()
		attrs.MinWidth = 360
		attrs.FixedWidth = true
		attrs.NoAutoFocus = true
		TextInputExt(&fwPath, attrs)
		if Button(NoIcon, "Open…") {
			openFirmwareDialog()
		}
		if Button(NoIcon, "Load") {
			if !webReloadFirmware() {
				if p := strings.TrimSpace(fwPath); p != "" {
					_ = emu.Load(p)
					flashMode = false
					watchFile(p, "firmware")
				}
			}
		}
		runLabel := "Run"
		if snap.Running {
			runLabel = "Pause"
		}
		if Button(NoIcon, runLabel) {
			emu.SetRunning(!snap.Running)
		}
		if Button(NoIcon, "Reset") {
			emu.Reset()
		}
		// GDB needs a listening socket; the browser has none.
		if webHostTools() {
			if gdbRunning() {
				if Button(NoIcon, "Stop GDB") {
					stopGDB()
				}
			} else if Button(NoIcon, "GDB") {
				if err := startGDB(gdbDefaultAddr); err != nil {
					fmt.Fprintln(os.Stderr, "gdb:", err)
				}
			}
		}
		if Button(NoIcon, "Board…") {
			pendingBoardPicker = true
			RequestNextFrame()
		}
	})

	Container(Attrs(Row, CrossMid, Expand, Gap(14), Pad4(2, 10, 6, 10)), func() {
		name := "no firmware"
		if snap.Firmware != "" {
			name = filepath.Base(snap.Firmware)
		}
		if snap.FlashWrites > 0 && snap.FlashPath != "" {
			name = filepath.Base(snap.FlashPath) + " (flash)"
		}
		Label(name, FontSize(12), FontWeight(600))
		virtual := fmt.Sprintf("virtual %.3fs", float64(snap.Cycles)/150_000_000)
		timeAttrs := []TextStyleFn{FontSize(12)}
		if snap.Running && snap.Speed > 0 {
			virtual += fmt.Sprintf(" · %.2f×", snap.Speed)
		}
		if snap.Slow {
			// The interpreter cannot keep up with real time: make the clock
			// the warning.
			timeAttrs = append(timeAttrs, TextColor(210, 60, 60, 1), FontWeight(700))
		}
		Label(virtual, timeAttrs...)
		Label(fmt.Sprintf("PC %08x  SP %08x", snap.PC, snap.SP), FontSize(12), Fonts(Monospace...))
		if addr := gdbListenAddr(); addr != "" {
			Label("GDB "+addr, FontSize(12), FontWeight(600), TextColor(20, 90, 70, 1))
		}
		if snap.PSRAMBytes > 0 {
			Label(fmt.Sprintf("PSRAM %d MB · %d cyc", snap.PSRAMBytes>>20, snap.PSRAMLatency),
				FontSize(12), FontWeight(600), TextColor(140, 40, 70, 1))
		}
		if snap.Halted {
			Label("HALTED", FontSize(12), TextColor(0, 70, 50, 1), FontWeight(700))
		}
		if snap.Err != "" {
			Label(snap.Err, FontSize(12), TextColor(0, 70, 50, 1))
		}
		if firmwareWatch != nil {
			st := firmwareWatch.Status()
			if st.Watching {
				text := "watch " + filepath.Base(st.Path)
				if st.Reloads > 0 {
					text += fmt.Sprintf(" · reload %d", st.Reloads)
				}
				if st.Err != "" {
					text = "watch error: " + st.Err
				}
				Label(text, FontSize(12), FontWeight(600), TextColor(35, 70, 65, 1))
			}
		}
		if selectedBoard != nil && selectedBoard.Description != "" {
			Filler(1)
			Label(selectedBoard.Description, FontSize(11), TextColor(220, 6, 68, 1))
		}
	})
	if snap.FlashWrites > 0 {
		Container(Attrs(Row, CrossMid, Gap(12), Pad4(3, 10, 4, 10), Background(140, 30, 10, 1)), func() {
			location := snap.FlashPath
			if location == "" {
				location = "in-memory flash (use --flash PATH to persist)"
			}
			if snap.Boots > 1 {
				Label(fmt.Sprintf("now running firmware from %s", location), FontSize(12),
					FontWeight(600), TextColor(140, 45, 72, 1))
			} else {
				Label(fmt.Sprintf("flash updated at %s; reset to run", location), FontSize(12),
					FontWeight(600), TextColor(35, 65, 65, 1))
			}
			Filler(1)
			Label(fmt.Sprintf("boot #%d · entry %08x · %d flash writes",
				snap.Boots, snap.BootEntry, snap.FlashWrites), FontSize(11),
				TextColor(140, 25, 75, 1))
		})
	}
}

func pinsPanel(snap MachineState) {
	n := snap.GPIOs
	if n <= 0 {
		n = 30
	}
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("GPIO", FontSize(16), FontWeight(700))
			Label("function · direction · level", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("%d pins · %d active", n, countActive(snap)), FontSize(12))
			if Button(NoIcon, "All / Active") {
				pinView = 1 - pinView
			}
		})
		pinsCardsBody(snap, n)
	})
}

// pinCardHSLA is the card background for a pin's electrical state.
func pinCardHSLA(in PinInfo) (h, s, l float32) {
	switch {
	case in.OE && in.Level:
		return 140, 70, 45
	case in.OE:
		return 210, 15, 52
	case in.Level:
		return 205, 45, 42
	}
	return 220, 8, 18
}

// pinLevelHSLA is the text/dot colour for the level.
func pinLevelHSLA(in PinInfo) (h, s, l float32) {
	if in.Level {
		return 140, 65, 68
	}
	return 215, 6, 58
}

func pinDirLabel(in PinInfo) string {
	if in.OE {
		return "OUT"
	}
	return "IN"
}

// pinsCardsBody lays out uniform fixed-width cards. The column count is
// derived from the available width, and every card has the same width, so the
// function/level text can never change the alignment.
func pinsCardsBody(snap MachineState, n int) {
	const cardW, gap = 232, 6
	w := GetContentWidth()
	if w < 100 {
		w = float32(viewW) - 289
	}
	cols := int((w + gap) / (cardW + gap))
	cols = min(max(cols, 2), 6)
	rows := (n + cols - 1) / cols
	Container(Attrs(Gap(6), Expand), func() {
		for row := range rows {
			Container(Attrs(Row, Gap(gap)), func() {
				for col := range cols {
					p := row*cols + col
					if p >= n {
						break
					}
					in := snap.Pins[p]
					if pinView == 1 && !pinActive(in) {
						continue
					}
					pinCardUniform(p, in)
				}
			})
		}
	})
}

func pinCardUniform(p int, in PinInfo) {
	h, s, l := pinCardHSLA(in)
	dh, ds, dl := pinLevelHSLA(in)
	ContainerWithKey(p, Attrs(Row, CrossMid, FixWidth(232), FixHeight(34), Gap(5),
		Pad2(2, 7), Corners(4), Background(h, s, l, 1)), func() {
		Container(Attrs(FixSize(8, 8), Corners(4), Background(dh, ds, dl, 1)), func() {})
		Container(Attrs(FixWidth(36)), func() {
			Label(fmt.Sprintf("GP%-2d", p), FontSize(12), FontWeight(700))
		})
		Container(Attrs(FixWidth(54)), func() {
			Label(funcName(in.Func), FontSize(12))
		})
		Container(Attrs(FixWidth(26)), func() {
			Label(pinDirLabel(in), FontSize(11), TextColor(220, 6, 68, 1))
		})
		Filler(1)
		Label(levelName(in.Level), FontSize(11), FontWeight(600), TextColor(dh, ds, dl, 1))
	})
}

func pinActive(in PinInfo) bool {
	return in.OE || in.Func != 0x1f
}

func countActive(snap MachineState) int {
	n := 0
	for i := range 48 {
		if pinActive(snap.Pins[i]) {
			n++
		}
	}
	return n
}

// lcdAspect returns the displayed frame size: the last frame when there is
// one, otherwise the board's logical panel size. YAML width/height are already
// post-rotation, so no swap is applied here.
func lcdAspect(snap MachineState) (int, int) {
	if snap.LCD != nil {
		b := snap.LCD.Bounds()
		if b.Dx() > 1 && b.Dy() > 1 {
			return b.Dx(), b.Dy()
		}
	}
	if selectedBoard != nil && selectedBoard.LCD != nil {
		return selectedBoard.LCD.Width, selectedBoard.LCD.Height
	}
	return 240, 135
}

// lcdViewSize fits the panel into the window while keeping its aspect.
func lcdViewSize(snap MachineState) Vec2 {
	iw, ih := lcdAspect(snap)
	if iw <= 0 || ih <= 0 {
		iw, ih = 240, 135
	}
	aspect := float32(iw) / float32(ih)
	w := float32(viewW) * 0.62
	h := w / aspect
	if maxH := float32(viewH) * 0.64; h > maxH {
		h = maxH
		w = h * aspect
	}
	return Vec2{w, h}
}

func lcdPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("LCD", FontSize(16), FontWeight(700))
			if d := selectedBoard.LCD; d != nil {
				Label(fmt.Sprintf("ST7789 %d×%d · SPI%d · CS GP%d · DC GP%d · RST GP%d",
					d.Width, d.Height, d.SPI, d.CS, d.DC, d.RST), FontSize(12))
			}
			Filler(1)
			if snap.LCD == nil {
				Label("no frame yet", FontSize(12))
			}
		})
		Container(Attrs(Grow(1), Expand, Center), func() {
			size := lcdViewSize(snap)
			Container(Attrs(FixSizeVec(size), Corners(6), Clip, Background(0, 0, 0, 1),
				BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
				st := ProcessButtonEvents(false)
				if snap.LCD != nil {
					id := UseImage("lcd", snap.LCD)
					ImageViewAt(id, size)
				} else {
					Container(Attrs(FixSizeVec(size), Center), func() {
						Label("LCD idle", FontSize(14), TextColor(220, 5, 55, 1))
					})
				}
				// A press or drag on the panel is a finger on the capacitive
				// controller. The image is in driver order; convert back to
				// the panel's native coordinates.
				if snap.LCD != nil && snap.Touch != nil {
					down := Use[bool]("lcd-touch-down")
					if st.Active {
						b := snap.LCD.Bounds()
						lx := clampFloat(st.Local[0]/size[0], 0, 1)
						ly := clampFloat(st.Local[1]/size[1], 0, 1)
						nx, ny := touchPoint(selectedBoard, lx, ly, b.Dx(), b.Dy())
						emu.SetTouch(nx, ny, true)
						*down = true
						RequestNextFrame()
					} else if *down {
						emu.SetTouch(0, 0, false)
						*down = false
					}
				}
			})
			if snap.Touch != nil {
				Label("Click or drag the panel to inject touch", FontSize(12),
					TextColor(220, 6, 70, 1))
			}
		})
	})
}

func clampFloat(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// touchPoint maps a normalised position in the displayed (rotated) frame to
// the touch controller's native panel coordinates.
func touchPoint(b *BoardDef, fx, fy float32, dispW, dispH int) (int, int) {
	if b == nil || b.Touch == nil {
		return int(fx * float32(dispW)), int(fy * float32(dispH))
	}
	t := b.Touch
	rot := 0
	if b.LCD != nil {
		rot = b.LCD.Rotation
	}
	var nx, ny int
	switch rot {
	case 90:
		nx = t.Width - 1 - int(fy*float32(t.Width))
		ny = int(fx * float32(t.Height))
	case 180:
		nx = t.Width - 1 - int(fx*float32(t.Width))
		ny = t.Height - 1 - int(fy*float32(t.Height))
	case 270:
		nx = int(fy * float32(t.Width))
		ny = t.Height - 1 - int(fx*float32(t.Height))
	default:
		nx = int(fx * float32(t.Width))
		ny = int(fy * float32(t.Height))
	}
	if nx < 0 {
		nx = 0
	}
	if ny < 0 {
		ny = 0
	}
	if nx >= t.Width {
		nx = t.Width - 1
	}
	if ny >= t.Height {
		ny = t.Height - 1
	}
	return nx, ny
}

// sensorsPanel shows the touch, IMU, battery, backlight and card state.
func sensorsPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(10)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Sensors", FontSize(16), FontWeight(700))
			Filler(1)
			if snap.Touch != nil {
				Label("drag the LCD to inject touch", FontSize(12))
			}
		})
		Container(Attrs(Grow(1), Expand, Gap(10)), func() {
			ScrollOnInput()
			ScrollBars()
			Container(Attrs(Row, Gap(10)), func() {
				if snap.Touch != nil {
					touchCard(snap.Touch)
				}
				if snap.IMU != nil {
					imuCard(snap.IMU)
				}
			})
			Container(Attrs(Row, Gap(10)), func() {
				if snap.Battery != nil {
					batteryCard(snap.Battery)
				}
				if snap.Backlight != nil {
					infoCard("Backlight", fmt.Sprintf("%d%%", int(snap.Backlight.Duty*100+0.5)), "GP15 PWM")
				}
				if snap.SD != nil {
					mb := snap.SD.Blocks / 2048
					src := snap.SD.Source
					if len(src) > 48 {
						src = "…" + src[len(src)-47:]
					}
					infoCard("microSD", fmt.Sprintf("%d MB", mb), src)
				}
			})
		})
	})
}

func sensorCard(title string, body func()) {
	Container(Attrs(Grow(1), Gap(6), Pad(14), Corners(8), Background(220, 12, 12, 1),
		BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
		Label(title, FontSize(14), FontWeight(700))
		body()
	})
}

func touchCard(t *TouchState) {
	sensorCard("CST816D touch", func() {
		if t.Pressed {
			Label(fmt.Sprintf("X %d   Y %d", t.X, t.Y), Fonts(Monospace...), FontSize(20))
			Label("contact", FontSize(12), TextColor(140, 65, 55, 1))
		} else {
			Label("no contact", Fonts(Monospace...), FontSize(20))
			Label(fmt.Sprintf("last %d,%d", t.X, t.Y), FontSize(12))
		}
	})
}

func imuCard(m *IMUState) {
	sensorCard("QMI8658 IMU", func() {
		Label(fmt.Sprintf("acc  %+.2f %+.2f %+.2f g", m.AX, m.AY, m.AZ), Fonts(Monospace...), FontSize(13))
		Label(fmt.Sprintf("gyro %+.1f %+.1f %+.1f °/s", m.GX, m.GY, m.GZ), Fonts(Monospace...), FontSize(13))
		Label(fmt.Sprintf("die  %.1f °C", m.Temp), FontSize(12), TextColor(220, 6, 68, 1))
	})
}

func batteryCard(b *BatteryState) {
	pct := min(max(int((b.Volts-3.2)*100), 0), 100)
	sensorCard("Battery", func() {
		Label(fmt.Sprintf("%.2f V", b.Volts), Fonts(Monospace...), FontSize(20))
		Label(fmt.Sprintf("%d%% · ADC %d", pct, b.Raw), FontSize(12), TextColor(220, 6, 68, 1))
		Label("GP28 · ÷3 divider", FontSize(11))
	})
}

func infoCard(title, value, sub string) {
	sensorCard(title, func() {
		Label(value, Fonts(Monospace...), FontSize(20))
		Label(sub, FontSize(11), TextColor(220, 6, 68, 1))
	})
}

// ---- OTP tab ----

func lockShort(level uint8) string {
	switch level {
	case 0:
		return "RW"
	case 1:
		return "RO"
	}
	return "locked"
}

func lockColor(level uint8) (h, s, l float32) {
	switch level {
	case 0:
		return 140, 30, 62
	case 1:
		return 35, 60, 62
	}
	return 0, 60, 66
}

// otpVisiblePage picks the page the panel shows: the user's choice, else the
// page holding the last programmed row, else page 0.
func otpVisiblePage(o *OTPState) int {
	if otpPage >= 0 {
		return otpPage
	}
	if o.LastWrite >= 0 {
		return o.LastWrite / rp2350.OTPPageRows
	}
	if o.SeedPage >= 0 {
		return o.SeedPage
	}
	return 0
}

// otpChipID assembles the 64-bit manufacturing ID from rows 0-3.
func otpChipID(o *OTPState) (uint64, bool) {
	var id uint64
	for i := range 4 {
		if i >= len(o.Rows) {
			return 0, false
		}
		data, ok := rp2350.OTPDecodeRow(o.Rows[i])
		if !ok {
			return 0, false
		}
		id |= uint64(data) << (16 * i)
	}
	return id, true
}

// otpPanel shows the OTP array: 64 pages of 64 rows, their effective secure
// and non-secure locks, and the raw/ECC contents of the selected page.
func otpPanel(snap MachineState) {
	o := snap.OTP
	if o == nil {
		return
	}
	page := otpVisiblePage(o)
	rowBase := page * rp2350.OTPPageRows
	Container(Attrs(Grow(1), Expand, Gap(10)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("OTP", FontSize(16), FontWeight(700))
			Label("8 KB antifuse · 4096 rows × 16 bit + Hamming ECC", FontSize(12))
			if id, ok := otpChipID(o); ok {
				Label(fmt.Sprintf("chip id %016x", id), Fonts(Monospace...), FontSize(11),
					TextColor(220, 6, 68, 1))
			}
			Filler(1)
			Label(fmt.Sprintf("%d rows programmed · %d writes", o.Programmed, o.Writes), FontSize(12))
		})
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			if o.SeedSource != "" {
				rows := "rows"
				if o.SeedRows == 1 {
					rows = "row"
				}
				Label(fmt.Sprintf("seeded from %s (%d %s)", o.SeedSource, o.SeedRows, rows), FontSize(12))
			} else {
				Label("blank OTP at boot", FontSize(12), TextColor(220, 6, 62, 1))
			}
			Filler(1)
		})
		Container(Attrs(Row, Grow(1), Expand, Gap(12)), func() {
			Container(Attrs(FixWidth(636), Gap(6)), func() {
				Label("Pages · 64 rows each", FontSize(12), FontWeight(600))
				for row := range 8 {
					Container(Attrs(Row, Gap(6)), func() {
						for col := range 8 {
							n := row*8 + col
							otpPageTile(n, o.Pages[n], n == page)
						}
					})
				}
				Container(Attrs(Row, CrossMid, Gap(12), Pad2(4, 2)), func() {
					Label("secure / non-secure:", FontSize(10), TextColor(220, 6, 62, 1))
					for _, level := range []uint8{0, 1, 2} {
						h, s, l := lockColor(level)
						Label(lockShort(level), FontSize(10), TextColor(h, s, l, 1))
					}
					Filler(1)
					Label("locked → firmware reads return all-ones", FontSize(10),
						TextColor(220, 6, 62, 1))
				})
			})
			Container(Attrs(Grow(1), Expand, Pad(12), Corners(8), Background(220, 12, 8, 1),
				BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
				sec := o.Pages[page].Secure
				nsec := o.Pages[page].NonSecure
				Container(Attrs(Row, CrossMid, Gap(8)), func() {
					Label(fmt.Sprintf("Page %d · rows %#03x–%#03x", page, rowBase, rowBase+rp2350.OTPPageRows-1),
						FontSize(13), FontWeight(700))
					Filler(1)
					sh, ss, sl := lockColor(sec)
					nh, ns, nl := lockColor(nsec)
					Label("secure "+lockShort(sec), FontSize(10), TextColor(sh, ss, sl, 1))
					Label("non-secure "+lockShort(nsec), FontSize(10), TextColor(nh, ns, nl, 1))
					if o.Pages[page].SoftwareLocked {
						Label("software locked", FontSize(10), TextColor(0, 60, 66, 1))
					}
				})
				Container(Attrs(Row, Grow(1), Expand, Gap(24), Pad2(6, 0)), func() {
					for half := range 2 {
						Container(Attrs(Grow(1), Gap(1)), func() {
							for i := range rp2350.OTPPageRows / 2 {
								n := rowBase + half*(rp2350.OTPPageRows/2) + i
								otpRowLine(n, o.Rows[n])
							}
						})
					}
				})
			})
		})
	})
}

func otpPageTile(page int, p OTPPageState, selected bool) {
	Container(Attrs(FixWidth(72), FixHeight(58), Pad(6), Corners(6),
		Background(220, 12, 14, 1), BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
		st := ProcessButtonEvents(false)
		switch {
		case selected:
			ModAttrs(Background(140, 35, 22, 1), BorderColor(140, 45, 45, 1))
		case st.Hovered:
			ModAttrs(Background(220, 14, 20, 1))
		}
		if st.Clicked {
			otpPage = page
			RequestNextFrame()
		}
		Label(fmt.Sprintf("P%02d", page), FontSize(12), FontWeight(700))
		if p.Programmed == 0 {
			Label("blank", FontSize(10), TextColor(220, 5, 55, 1))
		} else {
			Label(fmt.Sprintf("%d rows", p.Programmed), FontSize(10))
		}
		sh, ss, sl := lockColor(p.Secure)
		nh, ns, nl := lockColor(p.NonSecure)
		Container(Attrs(Row, Gap(4)), func() {
			Label(lockShort(p.Secure), FontSize(9), TextColor(sh, ss, sl, 1))
			Label(lockShort(p.NonSecure), FontSize(9), TextColor(nh, ns, nl, 1))
		})
	})
}

func otpRowLine(row int, raw uint32) {
	if raw == 0 {
		Label(fmt.Sprintf("%03x  ----  ------  blank", row), Fonts(Monospace...), FontSize(10),
			TextColor(220, 5, 44, 1))
		return
	}
	data, status := rp2350.OTPRowStatus(raw)
	line := fmt.Sprintf("%03x  %04x  %06x  %s", row, data, raw&0xffffff, status)
	h, s, l := float32(140), float32(18), float32(78)
	switch status {
	case "corrected":
		h, s, l = 35, 65, 68
	case "ecc-error":
		h, s, l = 195, 35, 66
	case "uncorrectable":
		h, s, l = 0, 65, 68
	}
	Label(line, Fonts(Monospace...), FontSize(10), TextColor(h, s, l, 1))
}

// ws2812Panel shows the last WS2812B frame decoded from the board's LED pin.
func ws2812Panel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("WS2812B", FontSize(16), FontWeight(700))
			if d := selectedBoard.WS2812; d != nil {
				Label(fmt.Sprintf("GP%d · GRB 24-bit", d.Pin), FontSize(12))
			}
			Filler(1)
			if len(snap.WSColors) == 0 {
				Label("waiting for frames", FontSize(12))
			} else {
				Label(fmt.Sprintf("%d LEDs · frame %d", len(snap.WSColors), snap.WSFrames), FontSize(12))
			}
		})
		Container(Attrs(Grow(1), Expand, Center), func() {
			Container(Attrs(Row, CrossMid, Gap(28), Pad4(30, 38, 24, 38), Corners(18),
				Background(220, 14, 9, 1), BorderWidth(1), BorderColor(220, 12, 24, 1),
				BoxShadow(26)), func() {
				if len(snap.WSColors) == 0 {
					Label("no data", FontSize(14), TextColor(220, 5, 55, 1))
					return
				}
				for i, c := range snap.WSColors {
					h, s, l, a := rgbToHSLA(c)
					Container(Attrs(Gap(10), Center), func() {
						ContainerWithKey(i, Attrs(FixSize(112, 112), Corners(56), Background(h, s, l, a),
							BorderWidth(2), BorderColor(220, 10, 20, 1), Glow(16)), func() {})
						Label(fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B), Fonts(Monospace...), FontSize(12))
					})
				}
			})
		})
	})
}

// The Pico 2 exposes 20 pins per header. Left is pin 1..20 top to bottom;
// right is pin 40..21, which is how it reads when the USB connector is up.
type headerPin struct {
	label string
	gpio  int // -1 for power, ground and control pins
}

var leftHeader = []headerPin{
	{"GP0", 0}, {"GP1", 1}, {"GND", -1}, {"GP2", 2}, {"GP3", 3},
	{"GP4", 4}, {"GP5", 5}, {"GND", -1}, {"GP6", 6}, {"GP7", 7},
	{"GP8", 8}, {"GP9", 9}, {"GND", -1}, {"GP10", 10}, {"GP11", 11},
	{"GP12", 12}, {"GP13", 13}, {"GND", -1}, {"GP14", 14}, {"GP15", 15},
}

var rightHeader = []headerPin{
	{"VBUS", -1}, {"VSYS", -1}, {"GND", -1}, {"3V3_EN", -1}, {"3V3", -1},
	{"ADC_VREF", -1}, {"GP28", 28}, {"GND", -1}, {"GP27", 27}, {"GP26", 26},
	{"RUN", -1}, {"GP22", 22}, {"GND", -1}, {"GP21", 21}, {"GP20", 20},
	{"GP19", 19}, {"GP18", 18}, {"GND", -1}, {"GP17", 17}, {"GP16", 16},
}

// boardPanel draws a stylised Pico 2: both headers, the USB connector, the
// BOOTSEL button and the on-board LED on GP25.
func boardPanel(snap MachineState) {
	Container(Attrs(FixWidth(255), Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Board", FontSize(16), FontWeight(700))
			Label(snap.Package, FontSize(12))
			Filler(1)
		})
		Container(Attrs(Gap(1), Pad(10), Corners(12),
			Background(150, 22, 22, 1), BorderWidth(1), BorderColor(150, 35, 38, 1)), func() {
			Container(Attrs(Row, Center, Gap(6)), func() {
				Container(Attrs(FixSize(46, 12), Corners(2), Background(210, 6, 36, 1)), func() {})
				Label("USB", FontSize(9), TextColor(220, 5, 62, 1))
			})
			for i := range 20 {
				Container(Attrs(Row, CrossMid), func() {
					headerPad(leftHeader[i], snap, false)
					Filler(1)
					boardCenter(i, snap)
					Filler(1)
					headerPad(rightHeader[i], snap, true)
				})
			}
		})
	})
}

func headerPad(hp headerPin, snap MachineState, right bool) {
	h, s, l := padHSLA(hp, snap)
	Container(Attrs(Row, CrossMid, Gap(4), FixWidth(82)), func() {
		label := func() {
			Label(hp.label, FontSize(9), TextColor(220, 6, 72, 1))
		}
		pad := func() {
			Container(Attrs(FixSize(9, 9), Corners(2), Background(h, s, l, 1)), func() {})
		}
		if right {
			label()
			Filler(1)
			pad()
		} else {
			pad()
			Filler(1)
			label()
		}
	})
}

func padHSLA(hp headerPin, snap MachineState) (h, s, l float32) {
	if hp.gpio < 0 {
		return 210, 5, 34
	}
	in := snap.Pins[hp.gpio]
	switch {
	case in.OE && in.Level:
		return 140, 70, 50
	case in.OE:
		return 210, 15, 62
	case in.Level:
		return 205, 45, 55
	default:
		return 150, 18, 42
	}
}

func boardCenter(row int, snap MachineState) {
	Container(Attrs(FixWidth(58), Center), func() {
		switch row {
		case 5:
			Label("Pico 2", FontSize(11), FontWeight(700), TextColor(220, 8, 92, 1))
		case 12:
			h, s, l := float32(140), float32(18), float32(28)
			if snap.Pins[25].Level {
				h, s, l = 140, 70, 55
			}
			Container(Attrs(FixSize(14, 14), Corners(7), Background(h, s, l, 1),
				BorderWidth(1), BorderColor(210, 10, 55, 1)), func() {})
			Label("LED", FontSize(8), TextColor(220, 5, 65, 1))
		case 16:
			Container(Attrs(FixSize(34, 12), Corners(2), Background(210, 8, 42, 1)),
				func() {
					st := ProcessButtonEvents(false)
					pressed := Use[bool]("diagram-bootsel")
					if st.Active && !*pressed {
						*pressed = true
						emu.SetBootsel(true)
						RequestNextFrame()
					} else if !st.Active && *pressed {
						*pressed = false
						emu.SetBootsel(false)
						RequestNextFrame()
					}
					held := snap.BootselHeld || st.Active
					if st.Hovered {
						ModAttrs(BorderWidth(1), BorderColor(140, 50, 62, 1))
					}
					if held {
						ModAttrs(Background(140, 55, 40, 1))
					}
					Label("BOOT", FontSize(7), FontWeight(700),
						TextColor(220, 6, 86, 1))
				})
			Label("BOOTSEL", FontSize(7), TextColor(220, 5, 65, 1))
		}
	})
}

// rgbToHSLA converts an 8-bit colour into Shirei's HSLA range.
func rgbToHSLA(c color.RGBA) (float32, float32, float32, float32) {
	r := float64(c.R) / 255
	g := float64(c.G) / 255
	b := float64(c.B) / 255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l := (max + min) / 2
	var h, s float64
	if max != min {
		d := max - min
		if l > 0.5 {
			s = d / (2 - max - min)
		} else {
			s = d / (max + min)
		}
		switch max {
		case r:
			h = (g - b) / d
			if g < b {
				h += 6
			}
		case g:
			h = (b-r)/d + 2
		default:
			h = (r-g)/d + 4
		}
		h *= 60
	}
	return float32(h), float32(s * 100), float32(l * 100), float32(c.A) / 255
}

func ethPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			title := "Ethernet"
			if selectedBoard.Wifi != nil {
				title = "Wi-Fi"
			}
			Label(title, FontSize(16), FontWeight(700))
			if w := selectedBoard.Wifi; w != nil {
				if w.Kind == "cyw43439" {
					Label(fmt.Sprintf("CYW43439 · gSPI · REG_ON GP%d · DATA GP%d · CS GP%d · CLK GP%d",
						w.RegOn, w.Data, w.CS, w.CLK), FontSize(12))
				} else {
					Label(fmt.Sprintf("Wi-Fi (virtual) · UART%d · host-backed serial netdev", w.UART), FontSize(12))
				}
			} else if d := selectedBoard.Ethernet; d != nil {
				if d.Kind == "ch9120" {
					Label(fmt.Sprintf("CH9120 · UART%d · CFG0 GP%d · RST GP%d · TCPCS GP%d",
						d.UART, d.CFG0, d.RST, d.TCPCS), FontSize(12))
				} else {
					Label(fmt.Sprintf("%s · SPI%d · CS GP%d", strings.ToUpper(d.Kind), d.SPI, d.CS), FontSize(12))
				}
			}
			Filler(1)
			Label(fmt.Sprintf("%d events", len(snap.EthLog)), FontSize(11))
		})
		if c := snap.CYW; c != nil {
			Container(Attrs(Gap(4), Pad(12), Corners(6), Background(220, 12, 8, 1),
				BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
				Container(Attrs(Row, CrossMid, Gap(12)), func() {
					Label("CYW43439", FontSize(13), FontWeight(700))
					state := "waiting for WL_REG_ON"
					if c.FirmwareUp {
						state = "firmware running · control channel ready"
					}
					if c.LinkUp {
						state = "joined " + c.SSID
					}
					Label(state, FontSize(12), TextColor(140, 35, 70, 1))
					Filler(1)
					led := "LED off"
					if c.LED {
						led = "LED on"
					}
					Label(led, FontSize(12), FontWeight(600))
					if c.MAC != "" {
						Label(c.MAC, Fonts(Monospace...), FontSize(11), TextColor(220, 6, 68, 1))
					}
				})
				Label(c.Pins, FontSize(11), TextColor(220, 6, 62, 1))
			})
		}
		if n := snap.Network; n != nil {
			networkCard(n)
		}
		Container(Attrs(Grow(1), Expand, Gap(2), Pad(10), Corners(6), Clip,
			Background(220, 12, 8, 1), BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
			if len(snap.EthLog) == 0 {
				Label("no socket activity", FontSize(12), TextColor(220, 5, 55, 1))
				return
			}
			start := max(len(snap.EthLog)-48, 0)
			for _, line := range snap.EthLog[start:] {
				Label(line, Fonts(Monospace...), FontSize(12), TextColor(140, 12, 80, 1))
			}
		})
	})
}

// networkCard shows the guest identity and the host addresses that reach it.
func networkCard(n *devices.NetworkInfo) {
	Container(Attrs(Gap(4), Pad(12), Corners(6), Background(220, 12, 8, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Container(Attrs(Row, CrossMid, Gap(12)), func() {
			Label(n.Kind, FontSize(13), FontWeight(700))
			if n.Mode != "" {
				Label(n.Mode, FontSize(12), TextColor(140, 35, 70, 1))
			}
			Filler(1)
			if n.IP != "" {
				Label("ip "+n.IP, Fonts(Monospace...), FontSize(13))
			}
			if n.Mask != "" {
				Label("mask "+n.Mask, Fonts(Monospace...), FontSize(12), TextColor(220, 6, 68, 1))
			}
			if n.Gateway != "" {
				Label("gw "+n.Gateway, Fonts(Monospace...), FontSize(12), TextColor(220, 6, 68, 1))
			}
			if n.MAC != "" {
				Label(n.MAC, Fonts(Monospace...), FontSize(11), TextColor(220, 6, 68, 1))
			}
		})
		if !n.Configured {
			Label("waiting for the firmware to configure the interface", FontSize(12), TextColor(220, 6, 62, 1))
			return
		}
		if len(n.Sockets) == 0 {
			Label("no sockets open yet", FontSize(12), TextColor(220, 6, 62, 1))
			return
		}
		for _, s := range n.Sockets {
			Container(Attrs(Row, CrossMid, Gap(12)), func() {
				Label(s.Label(), Fonts(Monospace...), FontSize(13))
				Label(s.State, FontSize(11), TextColor(220, 8, 72, 1))
				if s.GuestAddr != "" {
					Label("device "+s.GuestAddr, Fonts(Monospace...), FontSize(12), TextColor(220, 6, 68, 1))
				}
				if s.Peer != "" {
					Label("peer "+s.Peer, Fonts(Monospace...), FontSize(12), TextColor(220, 6, 68, 1))
				}
				Filler(1)
				if s.HostAddr != "" {
					Label("connect to "+s.HostAddr, Fonts(Monospace...), FontSize(14),
						FontWeight(600), TextColor(140, 45, 72, 1))
				}
			})
		}
	})
}

// ---- Oscilloscope tab ----

// scopeCandidates are all GPIOs the emulator models. RP2350A packages expose
// 0..29; the RP2350B QFN-80 exposes 0..47. Showing the full range lets a B
// board be scoped without a package switch.
var scopeCandidates = func() []int {
	out := make([]int, 48)
	for i := range out {
		out[i] = i
	}
	return out
}()

var (
	scopeTrigMode = ""
	scopeTrigPin  = 0
)

func scopePanel() {
	pins, samples, interval := emu.ScopeData()
	triggered, trigInfo := emu.ScopeTriggered()
	trigIdx := -1
	if triggered {
		trigIdx = emu.ScopeTriggerIndex()
	}
	vmax := scopeRanges[scopeRangeIdx]
	visible := samples
	if scopeZoom > 1 && len(samples) > 0 {
		n := max(len(samples)/int(scopeZoom), 4)
		visible = samples[len(samples)-n:]
	}
	marker := -1
	if trigIdx >= 0 {
		start := len(samples) - len(visible)
		if trigIdx >= start {
			marker = trigIdx - start
		}
	}
	if len(pins) > 0 {
		scopeFFTChannel = min(max(scopeFFTChannel, 0), len(pins)-1)
	}
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Oscilloscope", FontSize(16), FontWeight(700))
			if interval > 0 {
				rate := 150e6 / float64(interval)
				window := float64(len(visible)) / rate * 1000
				Label(fmt.Sprintf("%.1f MS/s · %.2f ms window", rate/1e6, window), FontSize(12))
			}
			Filler(1)
			fftLabel := "Show FFT"
			if scopeFFT {
				fftLabel = "Hide FFT"
			}
			if Button(NoIcon, fftLabel) {
				scopeFFT = !scopeFFT
				RequestNextFrame()
			}
			Label("click pins to add or remove traces", FontSize(11), TextColor(220, 6, 60, 1))
		})
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Label("Time", FontSize(11), FontWeight(600))
			if Button(NoIcon, "Zoom −") && scopeZoom > 1 {
				scopeZoom /= 2
			}
			if Button(NoIcon, "Zoom +") && scopeZoom < 64 {
				scopeZoom *= 2
			}
			Label(fmt.Sprintf("%.0f×", scopeZoom), FontSize(11))
			Container(Attrs(FixWidth(14)), func() {})
			if Button(NoIcon, "Range −") && scopeRangeIdx > 0 {
				scopeRangeIdx--
			}
			Label(fmt.Sprintf("0–%s V", strconv.FormatFloat(float64(vmax), 'g', 3, 32)), FontSize(11))
			if Button(NoIcon, "Range +") && scopeRangeIdx < len(scopeRanges)-1 {
				scopeRangeIdx++
			}
			if scopeZoom != 1 || scopeRangeIdx != 4 {
				if Button(NoIcon, "Reset") {
					scopeZoom = 1
					scopeRangeIdx = 4
				}
			}
			Container(Attrs(FixWidth(10)), func() {})
			if scopeFFT && len(pins) > 1 {
				Label("GP", FontSize(11))
				if Button(NoIcon, "−") {
					scopeFFTChannel = (scopeFFTChannel + len(pins) - 1) % len(pins)
				}
				Label(fmt.Sprintf("%d", pins[scopeFFTChannel]), FontSize(11), FontWeight(600))
				if Button(NoIcon, "+") {
					scopeFFTChannel = (scopeFFTChannel + 1) % len(pins)
				}
			}
			if scopeFFT {
				label := "Log"
				if !scopeFFTLog {
					label = "Lin"
				}
				if Button(NoIcon, label) {
					scopeFFTLog = !scopeFFTLog
					RequestNextFrame()
				}
			}
		})
		Container(Attrs(Row, CrossMid, Gap(4)), func() {
			Label("Trigger", FontSize(11), FontWeight(600))
			scopeTriggerButton("Off", "")
			scopeTriggerButton("Rise", "rise")
			scopeTriggerButton("Fall", "fall")
			scopeTriggerButton("High", "high")
			scopeTriggerButton("Low", "low")
			Container(Attrs(FixWidth(10)), func() {})
			Label("GP", FontSize(11))
			if Button(NoIcon, "−") {
				scopeTrigPin = (scopeTrigPin + len(scopeCandidates) - 1) % len(scopeCandidates)
				applyScopeTrigger()
			}
			Label(fmt.Sprintf("%d", scopeTrigPin), FontSize(12), FontWeight(600))
			if Button(NoIcon, "+") {
				scopeTrigPin = (scopeTrigPin + 1) % len(scopeCandidates)
				applyScopeTrigger()
			}
			if triggered {
				Label("TRIGGERED · "+trigInfo, FontSize(11), FontWeight(700), TextColor(0, 70, 55, 1))
				if Button(NoIcon, "Rearm") {
					emu.RearmScope()
					RequestNextFrame()
				}
			} else if scopeTrigMode != "" {
				Label("armed · "+scopeTrigMode, FontSize(11), TextColor(45, 60, 60, 1))
			}
		})
		scopePinPicker()
		Container(Attrs(Row, Gap(6), Expand, Grow(1), Extrinsic), func() {
			h := int(GetContentHeight())
			if h < 40 {
				h = 200 // first frame, before layout has resolved
			}
			Container(Attrs(FixWidth(38), FixHeight(float32(h))), func() {
				Label(fmt.Sprintf("%s V", strconv.FormatFloat(float64(vmax), 'g', 3, 32)), FontSize(10))
				Filler(1)
				Label("0 V", FontSize(10))
			})
			Container(Attrs(Grow(1), Expand, Extrinsic, Clip), func() {
				w := GetContentWidth()
				if w < 200 {
					w = 800 // first frame, before layout has resolved
				}
				img := scopeImage(pins, visible, int(w), h, vmax, marker)
				id := UseImage("scope", img)
				ImageViewAt(id, Vec2{w, float32(h)})
			})
		})
		if scopeFFT {
			scopeSpectrumPanel(pins, visible, interval, vmax)
		}
		Container(Attrs(Row, Wrap, Gap(14), Pad2(2, 0)), func() {
			if len(pins) == 0 {
				Label("no channels selected", FontSize(11), TextColor(220, 5, 55, 1))
				return
			}
			for i, p := range pins {
				if i >= 8 {
					break
				}
				Container(Attrs(Row, CrossMid, Gap(5)), func() {
					c := paletteAt(i)
					h, s, l, a := rgbToHSLA(c)
					Container(Attrs(FixSize(10, 10), Corners(5), Background(h, s, l, a)), func() {})
					Label(fmt.Sprintf("GP%d", p), FontSize(12), FontWeight(600))
					v := float32(0)
					if len(visible) > 0 {
						v = visible[len(visible)-1].V[i]
					}
					Label(fmt.Sprintf("%.2f V", v), FontSize(11))
					m := devices.MeasureDigital(visible, i, interval)
					if m.Valid {
						Label(fmt.Sprintf("%s · %.1f%% · %s", formatFreq(m.Frequency), m.Duty, formatPeriod(m.Period)),
							FontSize(11), TextColor(200, 10, 62, 1))
					} else {
						level := "low"
						if m.High > 0.5 {
							level = "high"
						}
						Label("static "+level, FontSize(11), TextColor(200, 10, 55, 1))
					}
					analogLabel := "10–90"
					if scopeAnalog[p] {
						analogLabel = "● 10–90"
					}
					if Button(NoIcon, analogLabel) {
						scopeAnalog[p] = !scopeAnalog[p]
						RequestNextFrame()
					}
					if scopeAnalog[p] {
						if m.RiseValid && m.FallValid {
							Label(fmt.Sprintf("rise %s · fall %s", formatPeriod(m.Rise), formatPeriod(m.Fall)),
								FontSize(11), TextColor(20, 60, 80, 1))
						} else {
							Label("rise/fall n/a", FontSize(11), TextColor(220, 6, 62, 1))
						}
					}
				})
			}
		})
		if sigs := emu.Signals(); len(sigs) > 0 {
			Container(Attrs(Row, Wrap, Gap(12), Pad2(2, 0)), func() {
				Label("Signals", FontSize(11), FontWeight(600))
				waves := []string{"sine", "square", "triangle", "ramp", "noise"}
				for _, s := range sigs {
					Label(fmt.Sprintf("GP%d %s", s.Pin, s.Wave), FontSize(11))
					label := "mute"
					if !s.Enabled {
						label = "enable"
					}
					if Button(NoIcon, label) {
						emu.SetSignalEnabled(s.Pin, !s.Enabled)
						RequestNextFrame()
					}
					if Button(NoIcon, "wave") {
						next := "sine"
						for i, w := range waves {
							if w == s.Wave {
								next = waves[(i+1)%len(waves)]
								break
							}
						}
						emu.SetSignalWave(s.Pin, next)
						RequestNextFrame()
					}
				}
			})
		}
	})
}

func scopeTriggerButton(name, mode string) {
	label := name
	if scopeTrigMode == mode {
		label = "● " + name
	}
	if Button(NoIcon, label) && scopeTrigMode != mode {
		scopeTrigMode = mode
		applyScopeTrigger()
	}
}

func applyScopeTrigger() {
	emu.SetScopeTrigger(ScopeTriggerConfig{Mode: scopeTrigMode, Pin: scopeTrigPin})
	RequestNextFrame()
}

func formatFreq(hz float64) string {
	switch {
	case hz >= 1e6:
		return fmt.Sprintf("%.3f MHz", hz/1e6)
	case hz >= 1e3:
		return fmt.Sprintf("%.3f kHz", hz/1e3)
	default:
		return fmt.Sprintf("%.1f Hz", hz)
	}
}

func formatPeriod(sec float64) string {
	switch {
	case sec >= 1:
		return fmt.Sprintf("%.3f s", sec)
	case sec >= 1e-3:
		return fmt.Sprintf("%.3f ms", sec*1e3)
	case sec >= 1e-6:
		return fmt.Sprintf("%.3f us", sec*1e6)
	default:
		return fmt.Sprintf("%.1f ns", sec*1e9)
	}
}

const (
	scopePinW   float32 = 64
	scopePinGap float32 = 4
)

// scopePinColumns returns how many fixed-width pin chips fit in width. The
// grid is explicit because Row+Wrap can overflow when its parent has no
// resolved width (notably during the first layout pass).
func scopePinColumns(width float32) int {
	if width < scopePinW {
		width = scopePinW
	}
	return max(1, int((width+scopePinGap)/(scopePinW+scopePinGap)))
}

func scopePinAvailableWidth() float32 {
	maxW := float32(viewW) - 40
	if maxW < scopePinW {
		maxW = scopePinW
	}
	width := GetContentWidth()
	if width < 100 || width > maxW {
		width = maxW
	}
	return width
}

func scopePinPicker() {
	cols := scopePinColumns(scopePinAvailableWidth())
	rows := (len(scopeCandidates) + cols - 1) / cols
	Container(Attrs(Gap(scopePinGap), Expand), func() {
		for row := range rows {
			Container(Attrs(Row, Gap(scopePinGap)), func() {
				for col := range cols {
					i := row*cols + col
					if i >= len(scopeCandidates) {
						break
					}
					scopePinChip(scopeCandidates[i])
				}
			})
		}
	})
}

func scopePinChip(pin int) {
	idx := -1
	for i, p := range scopePins {
		if p == pin {
			idx = i
			break
		}
	}
	ContainerWithKey(fmt.Sprintf("scope-%d", pin), Attrs(Row, CrossMid, FixWidth(scopePinW), Gap(5), Pad2(3, 7), Corners(4),
		Background(220, 12, 12, 1), BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		st := ProcessButtonEvents(false)
		if st.Clicked {
			toggleScopePin(pin)
		}
		if st.Hovered {
			ModAttrs(Background(220, 12, 17, 1))
		}
		if idx >= 0 {
			c := paletteAt(idx)
			h, s, l, a := rgbToHSLA(c)
			ModAttrs(Background(220, 10, 19, 1), BorderColor(h, s*0.5, 35, 1))
			Container(Attrs(FixSize(8, 8), Corners(4), Background(h, s, l, a)), func() {})
		}
		Label(fmt.Sprintf("GP%d", pin), FontSize(11))
	})
}

func toggleScopePin(pin int) {
	for i, p := range scopePins {
		if p == pin {
			scopePins = append(scopePins[:i], scopePins[i+1:]...)
			emu.SetScopePins(scopePins)
			return
		}
	}
	if len(scopePins) >= 8 {
		return
	}
	scopePins = append(scopePins, pin)
	emu.SetScopePins(scopePins)
}

// ---- Logic analyzer tab ----

// saveLogicTrace writes the current capture in the same text format as the
// headless runner.
func saveLogicTrace(pins []int, samples []devices.TraceSample, interval uint64) (string, error) {
	when := time.Now()
	trace := devices.LogicTrace{
		BoardID:   selectedBoard.ID,
		BoardName: selectedBoard.Name,
		Captured:  when,
		Interval:  interval,
		Pins:      pins,
		Samples:   samples,
	}
	if l := selectedBoard.Logic; l != nil {
		if d := l.UART; d != nil {
			text := "no frames"
			if ev := uartTXEvents(d.Bus, 0); len(ev) > 0 {
				text = uartEventText(ev)
			} else if ch := channelOf(pins, d.TX); ch >= 0 && interval > 0 {
				text = uartFrameText(devices.DecodeUART(samples, ch, float64(d.Baud), 150e6/float64(interval)), samples)
			}
			trace.Sections = append(trace.Sections, devices.LogicSection{
				Name:   "UART",
				Detail: fmt.Sprintf("%d baud · TX GP%d", d.Baud, d.TX),
				Text:   text,
			})
		}
		if d := l.I2C; d != nil {
			text := "no transactions"
			if ev := emu.I2CEvents(); len(ev) > 0 {
				text = i2cEventText(ev, d.Bus)
			} else {
				sda, scl := channelOf(pins, d.SDA), channelOf(pins, d.SCL)
				if sda >= 0 && scl >= 0 {
					text = i2cText(devices.DecodeI2C(samples, sda, scl))
				}
			}
			trace.Sections = append(trace.Sections, devices.LogicSection{
				Name:   "I2C",
				Detail: fmt.Sprintf("SDA GP%d · SCL GP%d", d.SDA, d.SCL),
				Text:   text,
			})
		}
		if d := l.SPI; d != nil {
			text := "no frames"
			if ev := spiBusEvents(d.Bus, 0); len(ev) > 0 {
				text = spiEventText(ev)
			} else {
				sck, mosi := channelOf(pins, d.SCK), channelOf(pins, d.MOSI)
				if sck >= 0 && mosi >= 0 {
					text = spiText(devices.DecodeSPI(samples, sck, mosi, channelOf(pins, d.MISOPin()), channelOf(pins, d.CSPin())))
				}
			}
			trace.Sections = append(trace.Sections, devices.LogicSection{
				Name:   "SPI",
				Detail: fmt.Sprintf("SCK GP%d · MOSI GP%d", d.SCK, d.MOSI),
				Text:   text,
			})
		}
	}
	if text := usbTraceText(emu.USBEvents()); text != "" {
		trace.Sections = append(trace.Sections, devices.LogicSection{Name: "USB CDC", Text: text})
	}
	trace.Sections = append(trace.Sections, fullEventSections()...)
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path := filepath.Join(wd, devices.LogicTraceFilename(selectedBoard.ID, when))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if err := devices.WriteLogicTrace(f, trace); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func usbTraceText(events []USBEvent) string {
	if len(events) == 0 {
		return ""
	}
	base := events[0].Cycle
	var b strings.Builder
	for _, ev := range events {
		dir := "IN "
		if !ev.In {
			dir = "OUT"
		}
		fmt.Fprintf(&b, "%7.2fms %s %q\n", float64(ev.Cycle-base)/150e3, dir, string(ev.Data))
	}
	return b.String()
}

// fullEventSections dumps every captured controller event, across both UARTs
// and directions, not just the board's configured protocol.
func fullEventSections() []devices.LogicSection {
	var out []devices.LogicSection
	if ev := emu.UARTEvents(); len(ev) > 0 {
		base := ev[0].Cycle
		var b strings.Builder
		for _, e := range ev {
			dir := "TX"
			if e.In {
				dir = "RX"
			}
			fmt.Fprintf(&b, "%8.3fms UART%d %s 0x%02x %q\n",
				float64(e.Cycle-base)/150e3, e.Bus, dir, e.Data, string(rune(e.Data)))
		}
		out = append(out, devices.LogicSection{Name: "UART events", Text: b.String()})
	}
	if ev := emu.SPIEvents(); len(ev) > 0 {
		base := ev[0].Cycle
		var b strings.Builder
		for _, e := range ev {
			fmt.Fprintf(&b, "%8.3fms SPI%d MOSI 0x%02x MISO 0x%02x\n",
				float64(e.Cycle-base)/150e3, e.Bus, e.TX, e.RX)
		}
		out = append(out, devices.LogicSection{Name: "SPI events", Text: b.String()})
	}
	if ev := emu.I2CEvents(); len(ev) > 0 {
		base := ev[0].Cycle
		var b strings.Builder
		for _, e := range ev {
			fmt.Fprintf(&b, "%8.3fms I2C%d %s", float64(e.Cycle-base)/150e3, e.Bus, e.Kind)
			switch e.Kind {
			case "addr":
				fmt.Fprintf(&b, " 0x%02x", e.Addr)
			case "data", "read":
				fmt.Fprintf(&b, " 0x%02x", e.Data)
			}
			if e.Kind == "data" || e.Kind == "read" {
				if e.Ack {
					b.WriteString(" ack")
				} else {
					b.WriteString(" nack")
				}
			}
			b.WriteByte('\n')
		}
		out = append(out, devices.LogicSection{Name: "I2C events", Text: b.String()})
	}
	return out
}

// saveLogicVCD exports the current pin capture for GTKWave or PulseView.
func saveLogicVCD(pins []int, samples []devices.TraceSample, interval uint64) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path := filepath.Join(wd, devices.VCDFilename(selectedBoard.ID, time.Now()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if err := devices.WriteVCD(f, pins, samples, interval); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func logicPanel() {
	pins, samples, interval := emu.LogicData()
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Logic analyzer", FontSize(16), FontWeight(700))
			if interval > 0 {
				rate := 150e6 / float64(interval)
				window := float64(len(samples)) / rate * 1000
				Label(fmt.Sprintf("%.1f MS/s · %.1f ms window", rate/1e6, window), FontSize(12))
			}
			Filler(1)
			if webFileOutput() {
				if Button(NoIcon, "Save trace") {
					path, err := saveLogicTrace(pins, samples, interval)
					if err != nil {
						logicSaveMsg = "save failed: " + err.Error()
						logicSaveErr = true
					} else {
						logicSaveMsg = "saved " + path
						logicSaveErr = false
					}
					RequestNextFrame()
				}
				if Button(NoIcon, "Save VCD") {
					path, err := saveLogicVCD(pins, samples, interval)
					if err != nil {
						logicSaveMsg = "VCD failed: " + err.Error()
						logicSaveErr = true
					} else {
						logicSaveMsg = "saved " + path
						logicSaveErr = false
					}
					RequestNextFrame()
				}
			}
			Label("hardware taps + pin traces", FontSize(11), TextColor(220, 6, 60, 1))
		})
		if logicSaveMsg != "" {
			color := TextColor(140, 45, 72, 1)
			if logicSaveErr {
				color = TextColor(0, 65, 60, 1)
			}
			Label(logicSaveMsg, FontSize(11), FontWeight(600), color)
		}
		Container(Attrs(Row, CrossMid, Gap(4)), func() {
			Label("Trigger", FontSize(11), FontWeight(600))
			triggerButton("Off", "")
			triggerButton("Any", "any")
			triggerButton("UART", "uart")
			triggerButton("I2C", "i2c")
			triggerButton("SPI", "spi")
			pause := "Pause: off"
			if triggerPause {
				pause = "Pause: on"
			}
			if Button(NoIcon, pause) {
				triggerPause = !triggerPause
				emu.SetLogicTrigger(LogicTrigger{Kind: triggerKind, Pause: triggerPause})
			}
			if Button(NoIcon, "Rearm") {
				emu.RearmLogic()
			}
			triggered, info := emu.LogicTriggered()
			if triggered {
				Label("TRIGGERED · "+info, FontSize(11), FontWeight(700), TextColor(0, 70, 55, 1))
			} else if triggerKind != "" {
				Label("armed: "+triggerKind, FontSize(11), TextColor(45, 60, 60, 1))
			}
		})
		Container(Attrs(Grow(1), Expand, Clip, Viewport), func() {
			ScrollOnInput()
			ScrollBars()
			Container(Attrs(Gap(10), Expand), func() {
				l := selectedBoard.Logic
				if l == nil {
					Label("No protocols configured for this board (add a logic: block to cmd/simui/boards.yaml).",
						FontSize(12), TextColor(220, 6, 60, 1))
				} else {
					if l.UART != nil {
						logicUART(pins, samples, interval, l.UART)
					}
					if l.I2C != nil {
						logicI2C(pins, samples, l.I2C)
					}
					if l.SPI != nil {
						logicSPI(pins, samples, l.SPI)
					}
				}
				logicUSB()
			})
		})
	})
}

func triggerButton(name, kind string) {
	label := name
	if triggerKind == kind {
		label = "● " + name
	}
	if Button(NoIcon, label) && triggerKind != kind {
		triggerKind = kind
		emu.SetLogicTrigger(LogicTrigger{Kind: kind, Pause: triggerPause})
	}
}

func logicUART(pins []int, samples []devices.TraceSample, interval uint64, d *UARTDevice) {
	lines := []logicLine{{Pin: d.TX, Label: "TX", Color: paletteAt(0)}}
	if d.RX != d.TX {
		lines = append(lines, logicLine{Pin: d.RX, Label: "RX", Color: paletteAt(1)})
	}

	var wavePins []int
	var waveSamples []devices.TraceSample
	decoded := ""
	if events := uartTXEvents(d.Bus, 16); len(events) > 0 {
		decoded = uartEventText(events)
		wavePins, waveSamples = uartWave(events, d.Baud, d.TX)
		lines = []logicLine{{Pin: d.TX, Label: "TX", Color: paletteAt(0)}}
	} else {
		// Fallback for PIO/bit-banged UART: decode the pin trace itself.
		if ch := channelOf(pins, d.TX); ch >= 0 && interval > 0 {
			frames := devices.DecodeUART(samples, ch, float64(d.Baud), 150e6/float64(interval))
			decoded = uartFrameText(frames, samples)
		}
		wavePins, waveSamples = pins, samples
	}
	logicSection("UART", fmt.Sprintf("%d baud · TX GP%d", d.Baud, d.TX), wavePins, waveSamples, lines, decoded)
}

func uartTXEvents(bus, limit int) []UARTEvent {
	all := emu.UARTEvents()
	var out []UARTEvent
	for _, ev := range all {
		if ev.Bus == bus && !ev.In {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func uartEventText(events []UARTEvent) string {
	if len(events) == 0 {
		return "no frames"
	}
	raw := make([]byte, len(events))
	for i, ev := range events {
		raw[i] = ev.Data
	}
	return strconv.QuoteToASCII(string(raw))
}

// uartWave synthesises a TX waveform from captured bytes so the analyzer can
// show the wire even though the UART model drives the console directly.
func uartWave(events []UARTEvent, baud, txPin int) ([]int, []devices.TraceSample) {
	const interval = 75 // 2 MHz, matching the logic trace
	bit := max(int(math.Round(150e6/float64(interval)/float64(baud))), 1)
	var samples []devices.TraceSample
	add := func(volts float32, n int) {
		for range n {
			var s devices.TraceSample
			s.T = uint64(len(samples)) * interval
			s.V[0] = volts
			samples = append(samples, s)
		}
	}
	add(3.3, bit) // idle
	for _, ev := range events {
		add(0, bit) // start
		for b := range 8 {
			if ev.Data&(1<<uint(b)) != 0 {
				add(3.3, bit)
			} else {
				add(0, bit)
			}
		}
		add(3.3, bit)     // stop
		add(3.3, bit/2+1) // idle between bytes
	}
	return []int{txPin}, samples
}

func logicI2C(pins []int, samples []devices.TraceSample, d *I2CDevice) {
	lines := []logicLine{
		{Pin: d.SDA, Label: "SDA", Color: paletteAt(0)},
		{Pin: d.SCL, Label: "SCL", Color: paletteAt(1)},
	}
	var decoded string
	wavePins, waveSamples := pins, samples
	if ev := emu.I2CEvents(); len(ev) > 0 {
		decoded = i2cEventText(ev, d.Bus)
		wavePins, waveSamples = i2cWave(ev, d.Bus, lines)
	}
	if decoded == "" {
		sda, scl := channelOf(pins, d.SDA), channelOf(pins, d.SCL)
		if sda >= 0 && scl >= 0 {
			decoded = i2cText(devices.DecodeI2C(samples, sda, scl))
		}
	}
	logicSection("I2C", fmt.Sprintf("SDA GP%d · SCL GP%d", d.SDA, d.SCL), wavePins, waveSamples, lines, decoded)
}

// i2cWave synthesises the SDA/SCL waveform from tapped I2C phases.
func i2cWave(events []I2CEvent, bus int, lines []logicLine) ([]int, []devices.TraceSample) {
	const half = 25
	var samples []devices.TraceSample
	pins := []int{lines[0].Pin, lines[1].Pin}
	add := func(n int, sda, scl bool) {
		for range n {
			var s devices.TraceSample
			s.T = uint64(len(samples)) * 75
			if sda {
				s.V[0] = 3.3
			}
			if scl {
				s.V[1] = 3.3
			}
			samples = append(samples, s)
		}
	}
	phase := func(b byte, nack bool) {
		for bit := 7; bit >= 0; bit-- {
			sda := b&(1<<uint(bit)) != 0
			add(half, sda, false)
			add(half, sda, true)
		}
		add(half, nack, false)
		add(half, nack, true)
	}
	add(half*2, true, true) // idle
	for _, e := range events {
		if e.Bus != bus {
			continue
		}
		switch e.Kind {
		case "addr":
			add(half, false, true) // START: SDA falls while SCL high
			phase(e.Addr<<1, false)
		case "data":
			phase(e.Data, !e.Ack)
		case "read":
			phase(e.Data, false)
		case "stop":
			add(half, false, false)
			add(half, false, true)
			add(half, true, true) // STOP
		}
	}
	add(half*2, true, true)
	return pins, samples
}

func i2cEventText(events []I2CEvent, bus int) string {
	var tx []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tx = append(tx, cur.String())
			cur.Reset()
		}
	}
	for _, e := range events {
		if e.Bus != bus {
			continue
		}
		switch e.Kind {
		case "addr":
			flush()
			fmt.Fprintf(&cur, "S %02x", e.Addr)
		case "data":
			if e.Ack {
				fmt.Fprintf(&cur, " %02x A", e.Data)
			} else {
				fmt.Fprintf(&cur, " %02x N", e.Data)
			}
		case "read":
			fmt.Fprintf(&cur, " %02xR N", e.Data)
		case "stop":
			cur.WriteString(" P")
			flush()
		}
	}
	flush()
	if len(tx) > 6 {
		tx = tx[len(tx)-6:]
	}
	if len(tx) == 0 {
		return "no transactions"
	}
	return strings.Join(tx, "   ")
}

func logicSPI(pins []int, samples []devices.TraceSample, d *SPIDevice) {
	lines := []logicLine{
		{Pin: d.SCK, Label: "SCK", Color: paletteAt(0)},
		{Pin: d.MOSI, Label: "MOSI", Color: paletteAt(1)},
	}
	if miso := d.MISOPin(); miso >= 0 {
		lines = append(lines, logicLine{Pin: miso, Label: "MISO", Color: paletteAt(2)})
	}
	if cs := d.CSPin(); cs >= 0 {
		lines = append(lines, logicLine{Pin: cs, Label: "CS", Color: paletteAt(3)})
	}
	var decoded string
	var waveSamples []devices.TraceSample
	var wavePins []int
	events := spiBusEvents(d.Bus, 8)
	if len(events) > 0 {
		decoded = spiEventText(events)
		wavePins, waveSamples = spiWave(events, lines)
	} else {
		sck, mosi := channelOf(pins, d.SCK), channelOf(pins, d.MOSI)
		if sck >= 0 && mosi >= 0 {
			frames := devices.DecodeSPI(samples, sck, mosi, channelOf(pins, d.MISOPin()), channelOf(pins, d.CSPin()))
			decoded = spiText(frames)
		}
		wavePins, waveSamples = pins, samples
	}
	logicSection("SPI", fmt.Sprintf("SCK GP%d · MOSI GP%d", d.SCK, d.MOSI), wavePins, waveSamples, lines, decoded)
}

func spiBusEvents(bus, limit int) []SPIEvent {
	all := emu.SPIEvents()
	var out []SPIEvent
	for _, ev := range all {
		if ev.Bus == bus {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func spiEventText(events []SPIEvent) string {
	if len(events) == 0 {
		return "no frames"
	}
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "MOSI %02x MISO %02x  ", ev.TX, ev.RX)
	}
	return b.String()
}

// spiWave synthesises a mode-0 SPI transfer for the captured bytes.
func spiWave(events []SPIEvent, lines []logicLine) ([]int, []devices.TraceSample) {
	const interval = 75
	var samples []devices.TraceSample
	ch := map[string]int{}
	pins := make([]int, len(lines))
	for i, line := range lines {
		ch[line.Label] = i
		pins[i] = line.Pin
	}
	add := func(n int, f func(s *devices.TraceSample)) {
		for range n {
			var s devices.TraceSample
			s.T = uint64(len(samples)) * interval
			f(&s)
			samples = append(samples, s)
		}
	}
	if c, ok := ch["CS"]; ok {
		add(30, func(s *devices.TraceSample) { s.V[c] = 3.3 })
	}
	for _, ev := range events {
		if c, ok := ch["CS"]; ok {
			add(30, func(s *devices.TraceSample) { s.V[c] = 0 })
		}
		for bit := 7; bit >= 0; bit-- {
			mo := ev.TX&(1<<uint(bit)) != 0
			mi := ev.RX&(1<<uint(bit)) != 0
			add(30, func(s *devices.TraceSample) {
				if v, ok := ch["MOSI"]; ok && mo {
					s.V[v] = 3.3
				}
				if v, ok := ch["MISO"]; ok && mi {
					s.V[v] = 3.3
				}
			})
			add(30, func(s *devices.TraceSample) {
				if v, ok := ch["SCK"]; ok {
					s.V[v] = 3.3
				}
				if v, ok := ch["MOSI"]; ok && mo {
					s.V[v] = 3.3
				}
				if v, ok := ch["MISO"]; ok && mi {
					s.V[v] = 3.3
				}
			})
		}
		if c, ok := ch["CS"]; ok {
			add(30, func(s *devices.TraceSample) { s.V[c] = 3.3 })
		}
	}
	return pins, samples
}

func logicSection(name, subtitle string, pins []int, samples []devices.TraceSample, lines []logicLine, decoded string) {
	Container(Attrs(Gap(3), Expand), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label(name, FontSize(13), FontWeight(700))
			Label(subtitle, FontSize(11))
			Filler(1)
			for _, line := range lines {
				h, s, l, a := rgbToHSLA(line.Color)
				Container(Attrs(FixSize(8, 8), Corners(4), Background(h, s, l, a)), func() {})
				Label(fmt.Sprintf("%s GP%d", line.Label, line.Pin), FontSize(10))
			}
		})
		h := 18*len(lines) + 8
		Container(Attrs(Clip, Expand), func() {
			w := GetContentWidth()
			if w < 200 {
				w = 1100 // first frame, before layout has resolved
			}
			img := logicImage(pins, samples, lines, int(w), h)
			id := UseImage("logic-"+name, img)
			ImageViewAt(id, Vec2{w, float32(h)})
		})
		Container(Attrs(Clip), func() {
			Label(decoded, Fonts(Monospace...), FontSize(11), TextColor(140, 12, 80, 1))
		})
	})
}

func uartFrameText(frames []devices.UARTFrame, samples []devices.TraceSample) string {
	if len(frames) == 0 {
		return "no frames"
	}
	base := samples[0].T
	start := max(len(frames)-16, 0)
	var b strings.Builder
	for _, f := range frames[start:] {
		if f.Data >= 0x20 && f.Data < 0x7f {
			fmt.Fprintf(&b, "%.2fms %q  ", float64(f.T-base)/150e3, string(rune(f.Data)))
		} else {
			fmt.Fprintf(&b, "%.2fms %02x  ", float64(f.T-base)/150e3, f.Data)
		}
	}
	return b.String()
}

func i2cText(events []devices.I2CEvent) string {
	if len(events) == 0 {
		return "no transactions"
	}
	var tx []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tx = append(tx, cur.String())
			cur.Reset()
		}
	}
	for _, e := range events {
		switch e.Kind {
		case devices.I2CStart:
			flush()
			cur.WriteString("S")
		case devices.I2CAddr:
			rw := "W"
			if e.Read {
				rw = "R"
			}
			fmt.Fprintf(&cur, " %02x%s", e.Byte>>1, rw)
		case devices.I2CData:
			fmt.Fprintf(&cur, " %02x", e.Byte)
		case devices.I2CAck:
			if e.Ack {
				cur.WriteString(" A")
			} else {
				cur.WriteString(" N")
			}
		case devices.I2CStop:
			cur.WriteString(" P")
			flush()
		}
	}
	flush()
	if len(tx) > 6 {
		tx = tx[len(tx)-6:]
	}
	return strings.Join(tx, "   ")
}

func spiText(frames []devices.SPIFrame) string {
	if len(frames) == 0 {
		return "no frames"
	}
	start := max(len(frames)-12, 0)
	var b strings.Builder
	for _, f := range frames[start:] {
		fmt.Fprintf(&b, "MOSI %02x MISO %02x  ", f.MOSI, f.MISO)
	}
	return b.String()
}

func logicUSB() {
	events := emu.USBEvents()
	Container(Attrs(Gap(3)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("USB CDC", FontSize(13), FontWeight(700))
			Filler(1)
			Label(fmt.Sprintf("%d transfers", len(events)), FontSize(11))
		})
		if len(events) == 0 {
			Label("no CDC traffic", FontSize(11), TextColor(220, 5, 55, 1))
			return
		}
		base := events[0].Cycle
		start := max(len(events)-10, 0)
		for _, ev := range events[start:] {
			dir := "IN "
			if !ev.In {
				dir = "OUT"
			}
			Label(fmt.Sprintf("%7.2fms %s %q", float64(ev.Cycle-base)/150e3, dir, string(ev.Data)),
				Fonts(Monospace...), FontSize(11), TextColor(140, 12, 80, 1))
		}
	})
}

func busLabel(info BusBridgeInfo) string {
	if info.Kind == "I2C" {
		parts := make([]string, len(info.Addrs))
		for i, a := range info.Addrs {
			parts[i] = fmt.Sprintf("0x%02x", a)
		}
		return fmt.Sprintf("I2C%d %s", info.Bus, strings.Join(parts, ","))
	}
	return fmt.Sprintf("SPI%d CS%d", info.Bus, info.CS)
}

type boardBusDevice struct {
	name  string
	where string
}

// boardDevices lists the devices the board model attaches directly. The Bus
// tab shows them even when the board has no scripted or preset targets, which
// is the case for the sensor lab: its components are timed wire devices and
// an MCP2515, not I2C/SPI plugin slots.
func boardDevices(b *BoardDef) []boardBusDevice {
	if b == nil {
		return nil
	}
	var out []boardBusDevice
	add := func(name, where string) {
		out = append(out, boardBusDevice{name: name, where: where})
	}
	if d := b.LCD; d != nil {
		add("ST7789 LCD", fmt.Sprintf("SPI%d CS%d", d.SPI, d.CS))
	}
	if d := b.Touch; d != nil {
		add("CST816D touch", fmt.Sprintf("I2C%d 0x%02x", d.Bus, d.Addr))
	}
	if d := b.IMU; d != nil {
		add("QMI8658 IMU", fmt.Sprintf("I2C%d 0x%02x", d.Bus, d.Addr))
	}
	if d := b.SD; d != nil {
		add("microSD", fmt.Sprintf("SPI%d CS%d", d.SPI, d.CS))
	}
	if d := b.CAN; d != nil {
		add("MCP2515 CAN", fmt.Sprintf("SPI%d CS%d", d.Bus, d.CS))
	}
	for _, d := range b.OneWire {
		kind := d.Type
		if kind == "" {
			kind = "ds18b20"
		}
		add(strings.ToUpper(kind), fmt.Sprintf("1-Wire GP%d", d.Pin))
	}
	for _, d := range b.DHT {
		add(strings.ToUpper(d.Kind), fmt.Sprintf("GP%d", d.Pin))
	}
	for _, d := range b.Ultrasonic {
		add("HC-SR04", fmt.Sprintf("TRIG GP%d · ECHO GP%d", d.Trig, d.Echo))
	}
	if d := b.GPS; d != nil {
		add("NMEA GPS", fmt.Sprintf("UART%d", d.UART))
	}
	if d := b.Ethernet; d != nil {
		where := fmt.Sprintf("SPI%d CS%d", d.SPI, d.CS)
		if d.Kind == "ch9120" {
			where = fmt.Sprintf("UART%d", d.UART)
		}
		add(strings.ToUpper(d.Kind), where)
	}
	if d := b.Wifi; d != nil {
		if d.Kind == "cyw43439" {
			add("CYW43439", fmt.Sprintf("gSPI · CS GP%d · CLK GP%d", d.CS, d.CLK))
		} else {
			add("CH9120 Wi-Fi", fmt.Sprintf("UART%d", d.UART))
		}
	}
	if d := b.WS2812; d != nil {
		add("WS2812B", fmt.Sprintf("GP%d", d.Pin))
	}
	if d := b.PSRAM; d != nil {
		add("PSRAM", fmt.Sprintf("QMI CS%d", d.CS))
	}
	if d := b.BLE; d != nil {
		add("CYW43 BLE", "HCI · virtual controller")
	}
	return out
}

// deviceLogLines returns the transaction log of the timed wire devices that
// have no log tab of their own: 1-Wire, DHT and HC-SR04. CAN, GPS, WS2812 and
// BLE keep their history in their dedicated tabs.
func deviceLogLines(snap MachineState) []string {
	return append([]string(nil), snap.DeviceLogs...)
}

func busPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(10)), func() {
		infos := emu.BusBridges()
		boardDevs := boardDevices(selectedBoard)
		deviceLogs := deviceLogLines(snap)
		var parts []string
		if len(boardDevs) > 0 {
			parts = append(parts, "board devices")
		}
		if len(deviceLogs) > 0 {
			parts = append(parts, "device logs")
		}
		if selectedBoard != nil && (len(selectedBoard.I2CPresets) > 0 || len(selectedBoard.SPIPresets) > 0) {
			parts = append(parts, "built-in presets")
		}
		if len(infos) > 0 {
			parts = append(parts, "Bus Pirate terminals")
		}
		subtitle := strings.Join(parts, " · ")
		if subtitle == "" {
			subtitle = "no devices"
		}
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("Devices and buses", FontSize(16), FontWeight(700))
			Label(subtitle, FontSize(12))
			Filler(1)
			if len(snap.Buses) > 0 {
				Label(fmt.Sprintf("%d line(s)", len(snap.Buses)), FontSize(11), TextColor(140, 45, 65, 1))
			}
		})
		if len(boardDevs) > 0 {
			Container(Attrs(Gap(4), Pad(8), Corners(6), Background(220, 10, 18, 1),
				BorderWidth(1), BorderColor(220, 20, 26, 1)), func() {
				Label("Board devices", FontSize(12), FontWeight(700))
				Container(Attrs(Gap(2)), func() {
					for _, d := range boardDevs {
						Container(Attrs(Row, Expand, CrossMid, Gap(8)), func() {
							Label(d.name, FontSize(11), FontWeight(600))
							Filler(1)
							Label(d.where, FontSize(11), Fonts(Monospace...), TextColor(140, 45, 68, 1))
						})
					}
				})
				Label("Attached by the board model; the device tabs show their live state.",
					FontSize(11), TextColor(220, 6, 62, 1))
			})
		}
		if len(deviceLogs) > 0 {
			Container(Attrs(Grow(1), Expand, Gap(4), Pad(8), Corners(6), Background(220, 10, 18, 1),
				BorderWidth(1), BorderColor(220, 20, 26, 1)), func() {
				Container(Attrs(Row, CrossMid, Gap(8)), func() {
					Label("Device logs", FontSize(12), FontWeight(700))
					Filler(1)
					if Button(NoIcon, "Copy") {
						RequestTextCopy(strings.Join(deviceLogs, "\n"))
					}
					if Button(NoIcon, "Clear") {
						emu.ClearDeviceLogs()
						RequestNextFrame()
					}
				})
				Container(Attrs(Grow(1), Expand, Clip, Viewport, Gap(2), Pad2(4, 6),
					Background(220, 12, 8, 1), Corners(4)), func() {
					ScrollOnInput()
					ScrollBars()
					for _, line := range deviceLogs {
						Label(line, Fonts(Monospace...), FontSize(10), TextColor(220, 6, 68, 1))
					}
				})
			})
		}
		if selectedBoard != nil && (len(selectedBoard.I2CPresets) > 0 || len(selectedBoard.SPIPresets) > 0) {
			Container(Attrs(Gap(4), Pad(8), Corners(6), Background(220, 10, 18, 1),
				BorderWidth(1), BorderColor(220, 20, 26, 1)), func() {
				Label("Built-in presets", FontSize(12), FontWeight(700))
				Container(Attrs(Row, Gap(6), Wrap), func() {
					for _, d := range selectedBoard.I2CPresets {
						Label(fmt.Sprintf("I2C%d %s 0x%02x", d.Bus, strings.ToUpper(d.Preset), d.Addr),
							FontSize(11), Fonts(Monospace...), TextColor(140, 45, 68, 1))
					}
					for _, d := range selectedBoard.SPIPresets {
						Label(fmt.Sprintf("SPI%d CS%d %s", d.Bus, d.CS, strings.ToUpper(d.Preset)),
							FontSize(11), Fonts(Monospace...), TextColor(140, 45, 68, 1))
					}
				})
				Label("Attached directly to the emulated bus; no external script or TCP client needed.",
					FontSize(11), TextColor(220, 6, 62, 1))
			})
		}
		if len(infos) == 0 {
			if selectedBoard == nil || (len(selectedBoard.I2CPresets) == 0 &&
				len(selectedBoard.SPIPresets) == 0 && !boardHasPlugins(selectedBoard)) {
				if len(boardDevs) == 0 {
					Label("No I2C/SPI targets or board devices on this board.", FontSize(13))
				} else {
					Label("No scripted I2C/SPI targets; board devices are listed above.", FontSize(13))
				}
			}
			return
		}
		if busSel >= len(infos) {
			busSel = 0
		}
		Container(Attrs(Row, Gap(6), Wrap), func() {
			for i, info := range infos {
				label := busLabel(info)
				if Button(NoIcon, label) {
					busSel = i
					RequestNextFrame()
				}
			}
		})
		sel := infos[busSel]
		endpoint := sel.Listen
		if endpoint == "" {
			endpoint = sel.Connect
		}
		state := "listening"
		if sel.Connected {
			state = "client " + sel.Client
		}
		if sel.Connect != "" {
			state = "connecting"
			if sel.Connected {
				state = "connected " + sel.Client
			}
		}
		Label(fmt.Sprintf("%s · %s · %s", busLabel(sel), endpoint, state), FontSize(12), FontWeight(600))
		if !snap.Running {
			Label("Machine paused: connect the terminals, queue the answers, then press Run.", FontSize(12), FontWeight(600), TextColor(45, 60, 60, 1))
		}
		if sel.Listen == "" {
			Label("Connect mode: the external device is the server; use the CLI or a script there, not this terminal.", FontSize(12), TextColor(140, 45, 65, 1))
			return
		}
		term := busTerms[sel.Listen]
		if term == nil && busAutoConnect {
			autoConnectBusTerminals()
			term = busTerms[sel.Listen]
		}
		if term == nil {
			if Button(NoIcon, "Connect terminal") {
				t, err := NewBusTerminal(sel.Listen)
				if err != nil {
					busTermErrs[sel.Listen] = err.Error()
				} else {
					busTerms[sel.Listen] = t
					delete(busTermErrs, sel.Listen)
				}
				RequestNextFrame()
			}
			if msg := busTermErrs[sel.Listen]; msg != "" {
				Label(msg, FontSize(11), TextColor(0, 65, 60, 1))
			}
			Label("The terminal is the device side: it shows the controller's requests and sends the answers you type. Connect each bridge you want to answer.", FontSize(12), TextColor(140, 45, 65, 1))
			return
		}
		Container(Attrs(Row, Gap(8), CrossMid), func() {
			Label("terminal "+sel.Listen, FontSize(11), TextColor(140, 45, 65, 1))
			if q := term.Queued(); q > 0 {
				Label(fmt.Sprintf("%d queued", q), FontSize(11), FontWeight(600), TextColor(45, 60, 60, 1))
			}
			Filler(1)
			if Button(NoIcon, "Disconnect") {
				term.Close()
				delete(busTerms, sel.Listen)
				RequestNextFrame()
			}
		})
		Container(Attrs(Grow(1), Expand, Background(220, 12, 8, 1), Corners(6), Pad(8), Clip,
			BorderWidth(1), BorderColor(220, 25, 30, 1)), func() {
			term.Render()
		})
		Container(Attrs(Row, Gap(8), CrossMid), func() {
			attrs := DefaultTextInputAttrs()
			attrs.MinWidth = 360
			attrs.FixedWidth = true
			TextInputExt(&busInput, attrs)
			if Button(NoIcon, "Send") && strings.TrimSpace(busInput) != "" {
				term.Send(busInput)
				busInput = ""
				RequestNextFrame()
			}
			if Button(NoIcon, "Reset firmware") {
				emu.Reset()
				RequestNextFrame()
			}
		})
		Label("Type ACK, NAK or hex bytes; spaces queue one response per request (e.g. 0xEF 0x40 0x18).", FontSize(11), TextColor(140, 45, 65, 1))
		Label("If only HELLO appears, the firmware is not asking yet: queue the answers, then press Reset in the toolbar.", FontSize(11), TextColor(140, 45, 65, 1))
	})
}

// pluginsPanel is the dedicated log view for scripted I2C/SPI/UART device
// models. Keeping it out of the Bus tab gives a UART plugin's own output and
// the wire log separate homes.
func pluginsPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(10)), func() {
		plugins := emu.Plugins()
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("Scripted plugins", FontSize(16), FontWeight(700))
			Label("Lua and WASM device models", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("%d loaded · %d lines", len(plugins), pluginTotalLines(plugins)),
				FontSize(11), TextColor(140, 45, 65, 1))
		})
		if len(plugins) == 0 {
			Label("no scripted plugins on this board", FontSize(12), TextColor(220, 6, 62, 1))
			return
		}
		if busPluginSel >= len(plugins) || busPluginSel < 0 {
			busPluginSel = 0
		}
		Container(Attrs(Grow(1), Expand, Gap(6), Pad(8), Corners(6), Background(220, 10, 18, 1),
			BorderWidth(1), BorderColor(220, 20, 26, 1)), func() {
			Container(Attrs(Row, Gap(6), Wrap), func() {
				for i, p := range plugins {
					if Button(NoIcon, pluginLabel(p, i)) {
						busPluginSel = i
						RequestNextFrame()
					}
				}
			})
			p := plugins[busPluginSel]
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				Label(pluginTitle(p), FontSize(11), FontWeight(600), Fonts(Monospace...),
					TextColor(140, 45, 68, 1))
				Filler(1)
				if p.Index >= 0 && Button(NoIcon, "Clear") {
					emu.ClearPluginLogs(p.Index)
					RequestNextFrame()
				}
				if Button(NoIcon, "Copy") {
					RequestTextCopy(pluginLogText(p))
				}
			})
			if p.Error != "" {
				Label("error: "+p.Error, FontSize(10), TextColor(0, 65, 55, 1))
			}
			ContainerWithKey(fmt.Sprintf("plugin-log-%d", p.Index),
				Attrs(Grow(1), Expand, Clip, Viewport, Gap(2), Pad2(4, 6),
					Background(220, 12, 8, 1), Corners(4)), func() {
					ScrollOnInput()
					ScrollBars()
					if len(p.Logs) == 0 {
						Label("no output yet", FontSize(11), TextColor(220, 6, 62, 1))
						return
					}
					for _, line := range p.Logs {
						Label(line, Fonts(Monospace...), FontSize(10), TextColor(220, 6, 68, 1))
					}
				})
		})
	})
}

func pluginTotalLines(plugins []PluginInfo) int {
	var n int
	for _, p := range plugins {
		n += len(p.Logs)
	}
	return n
}

// uartPanel shows the raw hardware UART byte stream, both directions and both
// controllers. It is the wire log the device tabs and plugin models do not
// show.
func uartPanel(snap MachineState) {
	events := emu.UARTEvents()
	Container(Attrs(Grow(1), Expand, Gap(10)), func() {
		var tx, rx [2]int
		for _, e := range events {
			bus := e.Bus & 1
			if e.In {
				rx[bus]++
			} else {
				tx[bus]++
			}
		}
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("UART", FontSize(16), FontWeight(700))
			Label("hardware TX/RX bytes", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("UART0 TX %d · RX %d   UART1 TX %d · RX %d",
				tx[0], rx[0], tx[1], rx[1]), FontSize(11), TextColor(140, 45, 65, 1))
			if len(events) > 0 && Button(NoIcon, "Copy") {
				RequestTextCopy(uartBytesText(events))
			}
			if len(events) > 0 && Button(NoIcon, "Clear") {
				emu.ClearUARTEvents()
				RequestNextFrame()
			}
		})
		ContainerWithKey("uart-log",
			Attrs(Grow(1), Expand, Clip, Viewport, Gap(1), Pad2(4, 6),
				Background(220, 12, 8, 1), Corners(4), BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
				ScrollOnInput()
				ScrollBars()
				if len(events) == 0 {
					Label("no UART traffic yet", FontSize(11), TextColor(220, 6, 62, 1))
					return
				}
				base := events[0].Cycle
				show := events
				if len(show) > 512 {
					show = show[len(show)-512:]
				}
				for _, e := range show {
					Label(uartEventLine(base, e), Fonts(Monospace...), FontSize(10),
						TextColor(220, 6, 68, 1))
				}
			})
	})
}

func uartEventLine(base uint64, e UARTEvent) string {
	dir := "TX"
	if e.In {
		dir = "RX"
	}
	return fmt.Sprintf("%9.3fms UART%d %s 0x%02x %q",
		float64(e.Cycle-base)/150e3, e.Bus, dir, e.Data, string(rune(e.Data)))
}

func uartBytesText(events []UARTEvent) string {
	if len(events) == 0 {
		return ""
	}
	base := events[0].Cycle
	var b strings.Builder
	for _, e := range events {
		b.WriteString(uartEventLine(base, e))
		b.WriteByte('\n')
	}
	return b.String()
}

// pluginLabel is the compact selector label for one scripted device.
func pluginLabel(p PluginInfo, pos int) string {
	if p.Index < 0 {
		return fmt.Sprintf("ERROR %d", pos+1)
	}
	var parts []string
	if p.Lang != "" {
		parts = append(parts, p.Lang)
	}
	if p.Kind != "" {
		parts = append(parts, strings.ToUpper(p.Kind))
	}
	if p.Slot != "" {
		parts = append(parts, p.Slot)
	}
	if len(parts) == 0 {
		return "plugin"
	}
	return strings.Join(parts, " ")
}

// pluginTitle describes the selected device in the console header.
func pluginTitle(p PluginInfo) string {
	if p.Index < 0 {
		return "Plugin load error"
	}
	title := pluginLabel(p, 0)
	if p.Name != "" && !strings.EqualFold(p.Name, strings.ToLower(p.Kind)+" plugin") {
		title += " · " + p.Name
	}
	if p.Error != "" {
		title += " · error"
	} else {
		title += " · loaded"
	}
	return title
}

// pluginLogText is the plain-text export for the Copy button.
func pluginLogText(p PluginInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rp2350sim plugin %s\n", pluginTitle(p))
	if p.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", p.Error)
	}
	for _, line := range p.Logs {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(p.Logs) == 0 {
		b.WriteString("(no output)\n")
	}
	return b.String()
}

func consolePanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		FocusOnClick()
		focused := HasFocus()
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Console", FontSize(16), FontWeight(700))
			Label("USB CDC + UART", FontSize(12))
			Filler(1)
			if focused {
				Label("typing · Enter sends CR", FontSize(11), TextColor(140, 60, 60, 1))
			} else {
				Label("click to type", FontSize(11), TextColor(220, 5, 55, 1))
			}
		})
		if len(snap.Serial) > 0 {
			Container(Attrs(Row, Gap(16)), func() {
				for _, line := range snap.Serial {
					Label(line, FontSize(11), TextColor(140, 45, 65, 1))
				}
			})
		}
		if len(snap.Buses) > 0 {
			Container(Attrs(Row, Gap(16)), func() {
				for _, line := range snap.Buses {
					Label(line, FontSize(11), TextColor(140, 45, 65, 1))
				}
			})
		}
		ring := float32(25)
		if focused {
			ring = 140
		}
		Container(Attrs(Grow(1), Expand, Background(220, 12, 8, 1), Corners(6), Pad(8), Clip,
			BorderWidth(1), BorderColor(ring, 35, 40, 1)), func() {
			LogView(console, TextStyle(Fonts(Monospace...), FontSize(12), TextColor(140, 12, 85, 1)))
		})
		if focused {
			forwardConsoleInput()
		}
	})
}

// forwardConsoleInput sends this frame's typed text and editing keys to the
// firmware's USB CDC OUT endpoint and UART0.
func forwardConsoleInput() {
	fi := GetFrameInput()
	var out []byte
	if fi.Text != "" {
		out = append(out, fi.Text...)
	}
	switch fi.Key {
	case KeyEnter:
		out = append(out, '\r')
	case KeyTab:
		out = append(out, '\t')
	case KeyEscape:
		out = append(out, 0x1b)
	case KeyDeleteBackward:
		out = append(out, 0x7f)
	case KeyDeleteForward:
		out = append(out, 0x1b, '[', '3', '~')
	case KeyUp:
		out = append(out, 0x1b, '[', 'A')
	case KeyDown:
		out = append(out, 0x1b, '[', 'B')
	case KeyRight:
		out = append(out, 0x1b, '[', 'C')
	case KeyLeft:
		out = append(out, 0x1b, '[', 'D')
	case KeyHome:
		out = append(out, 0x1b, '[', 'H')
	case KeyEnd:
		out = append(out, 0x1b, '[', 'F')
	}
	if len(out) == 0 {
		return
	}
	emu.SendInput(out)
	RequestNextFrame()
}

func updateConsole(snap MachineState) {
	if snap.ConsolePos < seenPos {
		console = NewTextRing(256 * 1024)
		seenPos = 0
	}
	if snap.ConsolePos > seenPos {
		n := min(int(snap.ConsolePos-seenPos), len(snap.Console))
		console.Append([]byte(snap.Console[len(snap.Console)-n:]))
		seenPos = snap.ConsolePos
	}
}

// viewW and viewH mirror the window size so panels can fit their content.
var viewW, viewH = 1280, 820

func main() {
	png := flag.String("png", "", "render one settled frame to PATH and exit")
	runTo := flag.Float64("run", 0, "advance this many seconds of virtual time at startup (live windows keep running)")
	paused := flag.Bool("paused", false, "load the firmware but start paused (press Run to execute)")
	watchFlag := flag.Bool("watch", false, "reload and reset when the firmware/flash file changes on disk")
	winW := flag.Int("width", 1280, "window width in points")
	winH := flag.Int("height", 820, "window height in points")
	zFlag := flag.Float64("scope-zoom", 1, "initial scope time zoom (1, 2, 4, ...)")
	fftFlag := flag.Bool("fft", false, "show the scope spectrum (FFT) view")
	profileFlag := flag.Bool("profile", false, "start the Profile tab and collect samples + coverage")
	measureFlag := flag.Bool("measure", false, "show 10–90% rise/fall measurements for scope channels")
	rFlag := flag.Float64("scope-range", 3.3, "initial scope voltage range in volts")
	trigFlag := flag.String("logic-trigger", "", "arm a logic trigger: any, uart, i2c, spi")
	trigPause := flag.Bool("logic-pause", false, "pause the machine when the logic trigger fires")
	boardID := flag.String("board", "", "board id to select at startup (see --list-boards)")
	flag.Func("input", "set a board input at startup: ID=on|off|VOLTS|+N|-N (repeatable)", func(v string) error {
		inputArgs = append(inputArgs, v)
		return nil
	})
	flashPath := flag.String("flash", "", "raw flash backing image; loaded before firmware, and self-updates are written back here")
	otpPath := flag.String("otp", "", "seed OTP rows from this JSON file, overriding the board's otp.file")
	tabName := flag.String("tab", "board", "tab to show: board, lcd, dvi, eth/wifi, ws, sensors, scope, logic, audio, midi, otp, bus, debug, profile, hid, ble, bootsel, console")
	turboFlag := flag.Bool("turbo", false, "run as fast as the host allows instead of pacing virtual time to real time")
	debugBreak := flag.String("break", "", "add a debugger breakpoint (address or symbol) after loading")
	gdbFlag := flag.String("gdb", "", "start the GDB remote server on this address (e.g. 127.0.0.1:3333)")
	debugTab := flag.String("debug-tab", "registers", "debug inspector tab: registers, memory, variables")
	busConnect := flag.Bool("bus-connect", false, "auto-connect the Bus tab terminal to every bridge listener")
	flag.Var(busAnswerFlags(busAnswers), "bus-answer", "queue Bus Pirate answers for a bridge: i2c=TOKENS or spi=TOKENS (repeatable)")
	touchAt := flag.String("touch", "", "inject a held touch at native panel coordinates: x,y")
	touchDemoFlag := flag.Bool("touch-demo", false, "move a synthetic touch around the panel while the UI runs")
	listBoards := flag.Bool("list-boards", false, "list board definitions and exit")
	showVer := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	busAutoConnect = *busConnect
	emu.SetTurbo(*turboFlag)
	if webTurboDefault() {
		emu.SetTurbo(true)
	}
	// The browser build has no command line: the page URL names the board and
	// the firmware (see web_js.go). Desktop builds return ok=false.
	webFirmware := ""
	if id, fw, tab, ok := webBoot(); ok {
		if *boardID == "" {
			*boardID = id
		}
		if tab != "" {
			*tabName = tab
		}
		webFirmware = fw
	}
	if *showVer {
		fmt.Println("rp2350sim-ui", version)
		return
	}
	switch strings.ToLower(*debugTab) {
	case "memory", "mem":
		debugInspectorTab = 1
	case "variables", "vars":
		debugInspectorTab = 2
	}
	viewW, viewH = *winW, *winH

	var err error
	boards, err = LoadBoards()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *listBoards {
		for _, b := range boards {
			fmt.Printf("%-14s %-28s %s\n", b.ID, b.Name, strings.Join(boardBadges(&b), " · "))
		}
		return
	}
	if *otpPath != "" {
		rows, err := otpfile.Load(*otpPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "otp: %v\n", err)
			os.Exit(2)
		}
		emu.SetOTPOverride(rows, *otpPath)
	}
	if *boardID != "" {
		b := findBoard(*boardID)
		if b == nil {
			fmt.Fprintf(os.Stderr, "unknown board %q (use --list-boards)\n", *boardID)
			os.Exit(2)
		}
		webPrepareBoard(b)
		selectBoard(b)
	}

	if *flashPath != "" {
		emu.SetFlashPath(*flashPath)
	}
	// Allow a firmware path on the command line as a convenience. Without one,
	// a flash image can provide the boot vector directly.
	usedFlash := false
	if rest := flag.Args(); len(rest) > 0 {
		fwPath = rest[0]
		if err := emu.LoadPaused(fwPath); err != nil {
			fmt.Fprintln(os.Stderr, "firmware load:", err)
			os.Exit(1)
		}
	} else if webFirmware != "" {
		// The browser cannot read files: fetch the image and install it.
		data, err := webFetch(webFirmware)
		if err != nil {
			fmt.Fprintln(os.Stderr, "firmware load:", err)
			os.Exit(1)
		}
		fwPath = webFirmware
		if err := emu.LoadBytes(webFirmware, data, true); err != nil {
			fmt.Fprintln(os.Stderr, "firmware load:", err)
			os.Exit(1)
		}
	} else if *flashPath != "" {
		fwPath = *flashPath
		if err := emu.LoadFlashPaused(*flashPath); err != nil {
			fmt.Fprintln(os.Stderr, "flash load:", err)
			os.Exit(1)
		}
		usedFlash = true
	}
	flashMode = usedFlash
	// Headless snapshots are one-shot: a watcher would make the frame depend
	// on wall-clock state.
	watchEnabled = *watchFlag && *png == ""
	if watchEnabled {
		switch {
		case fwPath != "" && usedFlash:
			watchFile(fwPath, "flash")
		case fwPath != "":
			watchFile(fwPath, "firmware")
		default:
			fmt.Fprintln(os.Stderr, "--watch needs a firmware or flash file")
			os.Exit(2)
		}
	}
	if *debugBreak != "" {
		if _, err := emu.AddBreakpoint(*debugBreak); err != nil {
			fmt.Fprintln(os.Stderr, "breakpoint:", err)
			os.Exit(2)
		}
	}
	if *gdbFlag != "" {
		gdbDefaultAddr = *gdbFlag
		if err := startGDB(*gdbFlag); err != nil {
			fmt.Fprintln(os.Stderr, "gdb:", err)
			os.Exit(2)
		}
	}
	// Startup loads stay paused until debugger breakpoints and the GDB server
	// are installed, so even a reset-vector breakpoint sees the first fetch.
	if !*paused && *runTo <= 0 {
		emu.SetRunning(true)
	}
	if *touchAt != "" {
		xStr, yStr, ok := strings.Cut(*touchAt, ",")
		x, xErr := strconv.Atoi(strings.TrimSpace(xStr))
		y, yErr := strconv.Atoi(strings.TrimSpace(yStr))
		if !ok || xErr != nil || yErr != nil {
			fmt.Fprintln(os.Stderr, "--touch expects native panel coordinates as x,y")
			os.Exit(2)
		}
		pendingTouch, pendingTouchX, pendingTouchY = true, x, y
	}
	touchDemo = *touchDemoFlag
	setTab(*tabName)
	// A headless warm-up runs before the first frame, so apply the capture
	// demand now instead of waiting for RootView.
	emu.SetLogicTabActive(activeTab == tabLogic)
	if *profileFlag || activeTab == tabProfile {
		emu.SetProfiling(true)
	}
	if *zFlag >= 1 {
		scopeZoom = *zFlag
	}
	scopeFFT = *fftFlag
	if *measureFlag {
		for _, p := range scopePins {
			scopeAnalog[p] = true
		}
	}
	best := 0
	for i, v := range scopeRanges {
		if abs(float64(v)-*rFlag) < abs(float64(scopeRanges[best])-*rFlag) {
			best = i
		}
	}
	scopeRangeIdx = best
	if *trigFlag != "" {
		triggerKind = *trigFlag
		triggerPause = *trigPause
		emu.SetLogicTrigger(LogicTrigger{Kind: triggerKind, Pause: triggerPause})
	}

	if *runTo > 0 {
		emu.SetRunning(false)
		emu.RunFor(uint64(*runTo * 150_000_000))
		// A self-update during the warm-up has already produced a new flash
		// image; persist it before the live window starts.
		emu.SaveFlash()
		emu.SaveOTP()
		// A live window keeps running after the warm-up; only the headless
		// snapshot needs the machine frozen.
		if *png == "" {
			emu.SetRunning(true)
		}
	}

	if *paused && *png == "" {
		emu.SetRunning(false)
	}

	if *png != "" {
		// The system font scan runs in the background; wait so headless text
		// is not missing from the snapshot.
		WaitForSystemFontScan()
		if err := RenderToPNG(*png, *winW, *winH, RootView); err != nil {
			fmt.Println("render failed:", err)
		}
		emu.SyncSD()
		emu.SaveFlash()
		emu.SaveOTP()
		return
	}

	app.SetupWindow("rp2350sim", *winW, *winH)
	app.Run(RootView)
	stopAudio()
	emu.SyncSD()
	emu.SaveFlash()
	emu.SaveOTP()
}
