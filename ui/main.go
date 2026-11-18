package main

import (
	"flag"
	"fmt"
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
	snap := emu.Snapshot()
	updateConsole(snap)

	Container(Attrs(Viewport, Background(220, 8, 12, 1)), func() {
		toolbar(snap)
		Container(Attrs(Row, Grow(1), Gap(10), Pad4(10, 10, 10, 10)), func() {
			pinsPanel(snap)
			Container(Attrs(Grow(1), Expand, Gap(10)), func() {
				lcdPanel(snap)
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
		if Button(NoIcon, "Load") {
			emu.Load(strings.TrimSpace(fwPath))
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
	Container(Attrs(FixWidth(600), Expand, Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("GPIO", FontSize(16), FontWeight(700))
			Label("function · direction · level", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("%d pins", countActive(snap)), FontSize(12))
		})
		Container(Attrs(Row, Gap(6)), func() {
			for col := 0; col < 3; col++ {
				Container(Attrs(Gap(2)), func() {
					for row := 0; row < 16; row++ {
						p := col*16 + row
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
	return in.OE || in.Func != 0x1f && in.Func != 0
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
	ContainerWithKey(p, Attrs(Row, CrossMid, Gap(6), Pad2(3, 6), Corners(4), Background(h, s, l, 1)), func() {
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

func consolePanel() {
	Container(Attrs(Grow(1), Expand, Gap(6)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Console", FontSize(16), FontWeight(700))
			Label("USB CDC + UART", FontSize(12))
			Filler(1)
		})
		Container(Attrs(Grow(1), Expand, Background(220, 12, 8, 1), Corners(6), Pad(8), Clip), func() {
			LogView(console, TextStyle(Fonts(Monospace...), FontSize(12), TextColor(140, 12, 85, 1)))
		})
	})
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
