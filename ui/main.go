package main

import (
	"flag"
	"fmt"
	"image/color"
	"math"
	"path/filepath"
	"strings"

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

func levelName(high bool) string {
	if high {
		return "HIGH"
	}
	return "LOW"
}

func RootView() {
	// The panels paint dark surfaces, so use the dark scheme's light text.
	SetDarkMode(true)

	snap := emu.Snapshot()
	updateConsole(snap)

	// The panels below paint explicit dark surfaces, so the text color must be
	// set explicitly too: relying on the color scheme leaves dark-on-dark text
	// when the host is in light mode.
	lightText := TextColor(220, 8, 92, 1)
	Container(Attrs(Viewport, Background(220, 8, 12, 1), AmendTextStyle(lightText)), func() {
		toolbar(snap)
		Container(Attrs(Row, Grow(1), Gap(10), Pad4(10, 10, 10, 10)), func() {
			Container(Attrs(FixWidth(680), Row, Expand, Gap(10)), func() {
				boardPanel(snap)
				pinsPanel(snap)
			})
			Container(Attrs(Grow(1), Expand, Gap(10)), func() {
				lcdPanel(snap)
				ws2812Panel(snap)
				ethPanel(snap)
				consolePanel()
			})
		})
	})
}

func toolbar(snap MachineState) {
	Container(Attrs(Row, CrossMid, Gap(8), Pad2(10, 10), Background(220, 10, 16, 1)), func() {
		Label("rp2350sim", FontSize(18), FontWeight(700))
		Label("Cortex-M33 · RP2350", FontSize(12))
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
			for col := 0; col < 2; col++ {
				Container(Attrs(Gap(2), Grow(1), Expand), func() {
					for row := 0; row < 15; row++ {
						p := col*15 + row
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
	ContainerWithKey(p, Attrs(Row, CrossMid, Grow(1), Gap(6), Pad2(3, 6), Corners(4), Background(h, s, l, 1)), func() {
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
	Container(Attrs(Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("LCD", FontSize(16), FontWeight(700))
			Label("ST7789 240×135", FontSize(12))
			Filler(1)
			if snap.LCD == nil {
				Label("no frame yet", FontSize(12))
			}
		})
		Container(Attrs(Corners(6), Clip, Background(0, 0, 0, 1), BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
			if snap.LCD != nil {
				id := UseImage("lcd", snap.LCD)
				ImageViewAt(id, Vec2{480, 270})
			} else {
				Container(Attrs(FixSize(480, 270), Center), func() {
					Label("LCD idle", FontSize(14), TextColor(220, 5, 55, 1))
				})
			}
		})
	})
}

// ws2812Panel shows the last WS2812B frame decoded from the monitored pin.
func ws2812Panel(snap MachineState) {
	if !snap.WSEnabled {
		return
	}
	Container(Attrs(Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("WS2812B", FontSize(16), FontWeight(700))
			if len(snap.WSColors) == 0 {
				Label("waiting for frames", FontSize(12))
			} else {
				Label(fmt.Sprintf("%d LEDs · frame %d", len(snap.WSColors), snap.WSFrames), FontSize(12))
			}
		})
		Container(Attrs(Row, CrossMid, Gap(8), Pad(10), Corners(6), Background(220, 12, 8, 1), BorderWidth(1), BorderColor(220, 8, 30, 1)), func() {
			if len(snap.WSColors) == 0 {
				Label("no data", FontSize(12), TextColor(220, 5, 55, 1))
				return
			}
			for i, c := range snap.WSColors {
				h, s, l, a := rgbToHSLA(c)
				ContainerWithKey(i, Attrs(FixSize(26, 26), Corners(13), Background(h, s, l, a), BorderWidth(1), BorderColor(220, 8, 35, 1)), func() {})
			}
		})
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

// ethPanel shows the socket activity of the attached Ethernet model.
func ethPanel(snap MachineState) {
	Container(Attrs(Gap(4)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Ethernet", FontSize(16), FontWeight(700))
			Label(snap.EthKind+" · SPI0 CS GP17", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("%d events", len(snap.EthLog)), FontSize(11))
		})
		Container(Attrs(FixHeight(92), Expand, Gap(1), Pad(8), Corners(6), Clip,
			Background(220, 12, 8, 1), BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
			if len(snap.EthLog) == 0 {
				Label("no socket activity", FontSize(11), TextColor(220, 5, 55, 1))
				return
			}
			start := len(snap.EthLog) - 6
			if start < 0 {
				start = 0
			}
			for _, line := range snap.EthLog[start:] {
				Label(line, Fonts(Monospace...), FontSize(10), TextColor(140, 12, 80, 1))
			}
		})
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
	flag.Parse()

	// Allow a firmware path on the command line as a convenience.
	if rest := flag.Args(); len(rest) > 0 {
		fwPath = rest[0]
		emu.Load(fwPath)
	}
	if *runTo > 0 {
		emu.SetRunning(false)
		emu.RunFor(uint64(*runTo * 150_000_000))
	}

	if *png != "" {
		// The system font scan runs in the background; wait so headless text
		// is not missing from the snapshot.
		WaitForSystemFontScan()
		if err := RenderToPNG(*png, 1280, 820, RootView); err != nil {
			fmt.Println("render failed:", err)
		}
		return
	}

	app.SetupWindow("rp2350sim", 1280, 820)
	app.Run(RootView)
}
