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

	"github.com/akhenakh/rp2350sim/internal/devices"

	app "go.hasen.dev/shirei/app"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var (
	emu     = NewEmu()
	fwPath  = ""
	pinView = 0 // 0: all pins, 1: configured only
	console = NewTextRing(256 * 1024)
	seenPos int64

	boards             []BoardDef
	selectedBoard      *BoardDef
	pendingBoardPicker bool
	activeTab          = tabBoard
	scopePins          []int
	scopeZoom          = 1.0
	scopeRangeIdx      = 4
)

// Scope voltage ranges in volts (full scale, from 0).
var scopeRanges = []float32{0.5, 1, 1.65, 2, 3.3, 5}

// Tabs. Board and Console always exist; the others appear only when the
// selected board defines the device.
const (
	tabBoard = iota
	tabLCD
	tabEth
	tabWS
	tabScope
	tabLogic
	tabConsole
)

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

func RootView() {
	// The panels paint dark surfaces, so use the dark scheme's light text.
	SetDarkMode(true)

	// Apply a board change requested by the toolbar last frame, so the frame
	// that handled the click renders a consistent tab set.
	if pendingBoardPicker {
		pendingBoardPicker = false
		selectedBoard = nil
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
		// The toolbar's Board… button clears the selection mid-frame; show
		// the picker instead of the tabs for this frame.
		if selectedBoard == nil {
			boardPicker()
			return
		}
		tabsBar()
		Container(Attrs(Grow(1), Expand, Pad4(10, 10, 10, 10)), func() {
			switch activeTab {
			case tabLCD:
				lcdPanel(snap)
			case tabEth:
				ethPanel(snap)
			case tabWS:
				ws2812Panel(snap)
			case tabScope:
				scopePanel()
			case tabLogic:
				logicPanel()
			case tabConsole:
				consolePanel()
			default:
				boardAndPins(snap)
			}
		})
	})
}

// boardPicker is the startup screen: one card per YAML board definition.
func boardPicker() {
	Container(Attrs(Grow(1), Expand, Center, Gap(14), Pad(28)), func() {
		Label("rp2350sim", FontSize(32), FontWeight(700))
		Label("RP2350 emulator · choose the board to emulate", FontSize(15))
		Container(Attrs(Gap(10), FixWidth(780)), func() {
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
		Label("Board list: RP2350SIM_BOARDS=/path/to/boards.yaml", FontSize(11), TextColor(220, 5, 50, 1))
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
	if len(b.Analog) > 0 {
		out = append(out, "ANALOG")
	}
	if len(out) == 0 {
		out = append(out, "GPIO")
	}
	return out
}

func selectBoard(b *BoardDef) {
	selectedBoard = b
	activeTab = tabBoard
	scopePins = b.ScopePins()
	emu.SetBoard(b)
	RequestNextFrame()
}

func findBoard(id string) *BoardDef {
	for i := range boards {
		if boards[i].ID == id {
			return &boards[i]
		}
	}
	return nil
}

func setTab(name string) {
	switch strings.ToLower(name) {
	case "", "board":
		activeTab = tabBoard
	case "lcd":
		if selectedBoard != nil && selectedBoard.LCD != nil {
			activeTab = tabLCD
		}
	case "eth", "ethernet":
		if selectedBoard != nil && selectedBoard.Ethernet != nil {
			activeTab = tabEth
		}
	case "ws", "leds", "ws2812":
		if selectedBoard != nil && selectedBoard.WS2812 != nil {
			activeTab = tabWS
		}
	case "scope", "osc":
		activeTab = tabScope
	case "logic", "la":
		activeTab = tabLogic
	case "console":
		activeTab = tabConsole
	}
}

// tabsBar lists the main view plus one tab per device on the selected board.
func tabsBar() {
	if selectedBoard == nil {
		return
	}
	Container(Attrs(Row, CrossMid, Gap(4), Pad4(2, 10, 2, 10), Background(220, 10, 15, 1)), func() {
		tabButton("Board", tabBoard)
		if selectedBoard.LCD != nil {
			tabButton("LCD", tabLCD)
		}
		if selectedBoard.Ethernet != nil {
			tabButton("Ethernet", tabEth)
		}
		if selectedBoard.WS2812 != nil {
			tabButton("LEDs", tabWS)
		}
		tabButton("Scope", tabScope)
		tabButton("Logic", tabLogic)
		tabButton("Console", tabConsole)
		Filler(1)
		if selectedBoard.Description != "" {
			Label(selectedBoard.Description, FontSize(11), TextColor(220, 6, 68, 1))
		}
	})
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
		boardPanel(snap)
		pinsPanel(snap)
	})
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
			if p := strings.TrimSpace(fwPath); p != "" {
				emu.Load(p)
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
		if Button(NoIcon, "Board…") {
			pendingBoardPicker = true
			RequestNextFrame()
		}
		if Button(NoIcon, "All / Active") {
			pinView = 1 - pinView
		}
	})

	Container(Attrs(Row, CrossMid, Gap(14), Pad4(2, 10, 6, 10)), func() {
		name := "no firmware"
		if snap.Firmware != "" {
			name = filepath.Base(snap.Firmware)
		}
		Label(name, FontSize(12), FontWeight(600))
		Label(fmt.Sprintf("virtual %.3fs", float64(snap.Cycles)/150_000_000), FontSize(12))
		Label(fmt.Sprintf("PC %08x  SP %08x", snap.PC, snap.SP), FontSize(12), Fonts(Monospace...))
		if snap.Halted {
			Label("HALTED", FontSize(12), TextColor(0, 70, 50, 1), FontWeight(700))
		}
		if snap.Err != "" {
			Label(snap.Err, FontSize(12), TextColor(0, 70, 50, 1))
		}
	})
}

func pinsPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("GPIO", FontSize(16), FontWeight(700))
			Label("function · direction · level", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("%d active", countActive(snap)), FontSize(12))
		})
		Container(Attrs(Row, Grow(1), Expand, Gap(6)), func() {
			for col := 0; col < 3; col++ {
				Container(Attrs(Gap(2), Grow(1), Expand), func() {
					for row := 0; row < 10; row++ {
						p := col*10 + row
						in := snap.Pins[p]
						if pinView == 1 && !pinActive(in) {
							continue
						}
						pinCard(p, in)
					}
				})
			}
		})
	})
}

func pinActive(in PinInfo) bool {
	return in.OE || in.Func != 0x1f
}

func countActive(snap MachineState) int {
	n := 0
	for i := 0; i < 48; i++ {
		if pinActive(snap.Pins[i]) {
			n++
		}
	}
	return n
}

func pinCard(p int, in PinInfo) {
	h, s, l := float32(220), float32(8), float32(22)
	switch {
	case in.OE && in.Level:
		h, s, l = 140, 70, 50
	case in.OE:
		h, s, l = 210, 15, 62
	case in.Level:
		h, s, l = 205, 45, 55
	}
	ContainerWithKey(p, Attrs(Row, CrossMid, FixHeight(52), Gap(6), Pad2(3, 6), Corners(4), Background(h, s, l, 1)), func() {
		dotH, dotS, dotL := float32(210), float32(10), float32(45)
		if in.Level {
			dotH, dotS, dotL = 140, 70, 55
		}
		Container(Attrs(FixSize(9, 9), Corners(5), Background(dotH, dotS, dotL, 1)), func() {})
		Label(fmt.Sprintf("GP%-2d", p), FontSize(12), FontWeight(700))
		Label(funcName(in.Func), FontSize(12))
		dir := "IN "
		if in.OE {
			dir = "OUT"
		}
		Label(fmt.Sprintf("%s %s", dir, levelName(in.Level)), FontSize(12))
	})
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
			Container(Attrs(Corners(6), Clip, Background(0, 0, 0, 1), BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
				if snap.LCD != nil {
					id := UseImage("lcd", snap.LCD)
					ImageViewAt(id, Vec2{640, 360})
				} else {
					Container(Attrs(FixSize(640, 360), Center), func() {
						Label("LCD idle", FontSize(14), TextColor(220, 5, 55, 1))
					})
				}
			})
		})
	})
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
		Container(Attrs(Row, CrossMid, Gap(10), Pad(16), Corners(8), Background(220, 12, 8, 1),
			BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
			if len(snap.WSColors) == 0 {
				Label("no data", FontSize(13), TextColor(220, 5, 55, 1))
				return
			}
			for i, c := range snap.WSColors {
				h, s, l, a := rgbToHSLA(c)
				ContainerWithKey(i, Attrs(FixSize(38, 38), Corners(19), Background(h, s, l, a),
					BorderWidth(1), BorderColor(220, 8, 35, 1)), func() {})
			}
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
			Label("Pico 2", FontSize(12))
			Filler(1)
		})
		Container(Attrs(Gap(1), Pad(10), Corners(12),
			Background(150, 22, 22, 1), BorderWidth(1), BorderColor(150, 35, 38, 1)), func() {
			Container(Attrs(Row, Center, Gap(6)), func() {
				Container(Attrs(FixSize(46, 12), Corners(2), Background(210, 6, 36, 1)), func() {})
				Label("USB", FontSize(9), TextColor(220, 5, 62, 1))
			})
			for i := 0; i < 20; i++ {
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
			Container(Attrs(FixSize(20, 9), Corners(2), Background(210, 8, 42, 1)), func() {})
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
			Label("Ethernet", FontSize(16), FontWeight(700))
			if d := selectedBoard.Ethernet; d != nil {
				Label(fmt.Sprintf("%s · SPI%d · CS GP%d", strings.ToUpper(d.Kind), d.SPI, d.CS), FontSize(12))
			}
			Filler(1)
			Label(fmt.Sprintf("%d events", len(snap.EthLog)), FontSize(11))
		})
		Container(Attrs(Grow(1), Expand, Gap(2), Pad(10), Corners(6), Clip,
			Background(220, 12, 8, 1), BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
			if len(snap.EthLog) == 0 {
				Label("no socket activity", FontSize(12), TextColor(220, 5, 55, 1))
				return
			}
			start := len(snap.EthLog) - 48
			if start < 0 {
				start = 0
			}
			for _, line := range snap.EthLog[start:] {
				Label(line, Fonts(Monospace...), FontSize(12), TextColor(140, 12, 80, 1))
			}
		})
	})
}

// ---- Oscilloscope tab ----

// scopeCandidates are the pins offered as scope channels: the GPIO header
// pins most demos use plus the four ADC inputs.
var scopeCandidates = []int{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9,
	10, 11, 12, 13, 14, 15, 26, 27, 28, 29,
}

func scopePanel() {
	pins, samples, interval := emu.ScopeData()
	vmax := scopeRanges[scopeRangeIdx]
	visible := samples
	if scopeZoom > 1 && len(samples) > 0 {
		n := len(samples) / int(scopeZoom)
		if n < 4 {
			n = 4
		}
		visible = samples[len(samples)-n:]
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
		})
		Container(Attrs(Gap(3)), func() {
			for row := 0; row < 2; row++ {
				Container(Attrs(Row, Gap(4)), func() {
					for col := 0; col < 10; col++ {
						i := row*10 + col
						if i < len(scopeCandidates) {
							scopePinChip(scopeCandidates[i])
						}
					}
				})
			}
		})
		Container(Attrs(Row, Gap(6), Expand), func() {
			Container(Attrs(FixWidth(38), FixHeight(420)), func() {
				Label(fmt.Sprintf("%s V", strconv.FormatFloat(float64(vmax), 'g', 3, 32)), FontSize(10))
				Filler(1)
				Label("0 V", FontSize(10))
			})
			Container(Attrs(Grow(1), Expand, Clip), func() {
				w := GetContentWidth()
				if w < 200 {
					w = 1100 // first frame, before layout has resolved
				}
				img := scopeImage(pins, visible, int(w), 420, vmax)
				id := UseImage("scope", img)
				ImageViewAt(id, Vec2{w, 420})
			})
		})
		Container(Attrs(Row, CrossMid, Gap(16), Pad2(2, 0)), func() {
			if len(pins) == 0 {
				Label("no channels selected", FontSize(11), TextColor(220, 5, 55, 1))
				return
			}
			for i, p := range pins {
				if i >= 8 {
					break
				}
				c := paletteAt(i)
				h, s, l, a := rgbToHSLA(c)
				Container(Attrs(FixSize(10, 10), Corners(5), Background(h, s, l, a)), func() {})
				Label(fmt.Sprintf("GP%d", p), FontSize(12), FontWeight(600))
				v := float32(0)
				if len(visible) > 0 {
					v = visible[len(visible)-1].V[i]
				}
				Label(fmt.Sprintf("%.2f V", v), FontSize(11))
			}
		})
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
	ContainerWithKey(fmt.Sprintf("scope-%d", pin), Attrs(Row, CrossMid, Gap(5), Pad2(3, 7), Corners(4),
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
			Label("hardware taps + pin traces", FontSize(11), TextColor(220, 6, 60, 1))
		})
		Container(Attrs(Grow(1), Expand, Clip, Viewport), func() {
			ScrollOnInput()
			ScrollBars()
			Container(Attrs(Gap(10), Expand), func() {
				l := selectedBoard.Logic
				if l == nil {
					Label("No protocols configured for this board (add a logic: block to ui/boards.yaml).",
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

func logicUART(pins []int, samples []devices.TraceSample, interval uint64, d *UARTDevice) {
	lines := []logicLine{{Pin: d.TX, Label: "TX", Color: paletteAt(0)}}
	if d.RX != d.TX {
		lines = append(lines, logicLine{Pin: d.RX, Label: "RX", Color: paletteAt(1)})
	}

	var wavePins []int
	var waveSamples []devices.TraceSample
	decoded := ""
	if events := uartTXEvents(d.Bus); len(events) > 0 {
		decoded = uartEventText(events)
		wavePins, waveSamples = uartWave(events, d.Baud)
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

func uartTXEvents(bus int) []UARTEvent {
	all := emu.UARTEvents()
	var out []UARTEvent
	for _, ev := range all {
		if ev.Bus == bus && !ev.In {
			out = append(out, ev)
		}
	}
	if len(out) > 16 {
		out = out[len(out)-16:]
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
func uartWave(events []UARTEvent, baud int) ([]int, []devices.TraceSample) {
	const interval = 75 // 2 MHz, matching the logic trace
	bit := int(math.Round(150e6 / float64(interval) / float64(baud)))
	if bit < 1 {
		bit = 1
	}
	var samples []devices.TraceSample
	add := func(volts float32, n int) {
		for i := 0; i < n; i++ {
			var s devices.TraceSample
			s.T = uint64(len(samples)) * interval
			s.V[0] = volts
			samples = append(samples, s)
		}
	}
	add(3.3, bit) // idle
	for _, ev := range events {
		add(0, bit) // start
		for b := 0; b < 8; b++ {
			if ev.Data&(1<<uint(b)) != 0 {
				add(3.3, bit)
			} else {
				add(0, bit)
			}
		}
		add(3.3, bit)     // stop
		add(3.3, bit/2+1) // idle between bytes
	}
	return []int{0}, samples
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
		for i := 0; i < n; i++ {
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
	events := spiBusEvents(d.Bus)
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

func spiBusEvents(bus int) []SPIEvent {
	all := emu.SPIEvents()
	var out []SPIEvent
	for _, ev := range all {
		if ev.Bus == bus {
			out = append(out, ev)
		}
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
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
		for i := 0; i < n; i++ {
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
	start := len(frames) - 16
	if start < 0 {
		start = 0
	}
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
	start := len(frames) - 12
	if start < 0 {
		start = 0
	}
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
		start := len(events) - 10
		if start < 0 {
			start = 0
		}
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

func consolePanel() {
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
		n := int(snap.ConsolePos - seenPos)
		if n > len(snap.Console) {
			n = len(snap.Console)
		}
		console.Append([]byte(snap.Console[len(snap.Console)-n:]))
		seenPos = snap.ConsolePos
	}
}

func main() {
	png := flag.String("png", "", "render one settled frame to PATH and exit")
	runTo := flag.Float64("run", 0, "advance this many seconds of virtual time before rendering")
	winW := flag.Int("width", 1280, "window width in points")
	winH := flag.Int("height", 820, "window height in points")
	zFlag := flag.Float64("scope-zoom", 1, "initial scope time zoom (1, 2, 4, ...)")
	rFlag := flag.Float64("scope-range", 3.3, "initial scope voltage range in volts")
	boardID := flag.String("board", "", "board id to select at startup (see --list-boards)")
	tabName := flag.String("tab", "board", "tab to show: board, lcd, eth, ws, scope, logic, console")
	listBoards := flag.Bool("list-boards", false, "list board definitions and exit")
	flag.Parse()

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
	if *boardID != "" {
		b := findBoard(*boardID)
		if b == nil {
			fmt.Fprintf(os.Stderr, "unknown board %q (use --list-boards)\n", *boardID)
			os.Exit(2)
		}
		selectBoard(b)
	}

	// Allow a firmware path on the command line as a convenience.
	if rest := flag.Args(); len(rest) > 0 {
		fwPath = rest[0]
		emu.Load(fwPath)
	}
	setTab(*tabName)
	if *zFlag >= 1 {
		scopeZoom = *zFlag
	}
	best := 0
	for i, v := range scopeRanges {
		if abs(float64(v)-*rFlag) < abs(float64(scopeRanges[best])-*rFlag) {
			best = i
		}
	}
	scopeRangeIdx = best

	if *runTo > 0 {
		emu.SetRunning(false)
		emu.RunFor(uint64(*runTo * 150_000_000))
	}

	if *png != "" {
		// The system font scan runs in the background; wait so headless text
		// is not missing from the snapshot.
		WaitForSystemFontScan()
		if err := RenderToPNG(*png, *winW, *winH, RootView); err != nil {
			fmt.Println("render failed:", err)
		}
		return
	}

	app.SetupWindow("rp2350sim", *winW, *winH)
	app.Run(RootView)
}
