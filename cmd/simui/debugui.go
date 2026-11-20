package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/debug"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var (
	debugBreakSpec  string
	debugWatchSpec  string
	debugCyclesSpec = "1000000"
	debugMemSpec    string
	debugMsg        string
	debugCore       int
)

func debugPanel(snap MachineState) {
	s := emu.DebugSnapshot()
	Container(Attrs(Row, Grow(1), Expand, Gap(10)), func() {
		Container(Attrs(FixWidth(350), Gap(8), Expand), func() {
			debugControls(s)
			debugBreakpoints(s)
			debugWatches(s)
		})
		Container(Attrs(Grow(1), Expand, Gap(8)), func() {
			debugSource(s)
			Container(Attrs(Row, Grow(1), Expand, Gap(8)), func() {
				Container(Attrs(FixWidth(280), Expand, Gap(4)), func() {
					debugRegisters(s)
				})
				Container(Attrs(Grow(1), Expand, Gap(4)), func() {
					debugMemory(s)
				})
				Container(Attrs(FixWidth(330), Expand, Gap(4)), func() {
					debugVariables(s)
				})
			})
		})
	})
	_ = snap
}

func debugControls(s DebugSnapshot) {
	Container(Attrs(Gap(6), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Label("Execution", FontSize(16), FontWeight(700))
		Label(fmt.Sprintf("cycles %d · %.3fs · %s", s.Cycles, float64(s.Cycles)/150e6,
			debugStopText(s)), FontSize(12))
		Container(Attrs(Row, Gap(6)), func() {
			if Button(NoIcon, "Run") {
				emu.SetRunning(true)
				RequestNextFrame()
			}
			if Button(NoIcon, "Pause") {
				emu.SetRunning(false)
				RequestNextFrame()
			}
			if Button(NoIcon, "Step") {
				emu.StepDebug(debugCore)
				RequestNextFrame()
			}
		})
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Label("Core", FontSize(12))
			if Button(NoIcon, fmt.Sprintf("%d", debugCore)) {
				debugCore = 1 - debugCore
				RequestNextFrame()
			}
			Label("cycles", FontSize(12))
			textField(&debugCyclesSpec, 110)
			if Button(NoIcon, "Run cycles") {
				if n, err := parseDebugNumber(debugCyclesSpec); err == nil {
					emu.RunCycles(n)
					RequestNextFrame()
				} else {
					debugMsg = err.Error()
				}
			}
		})
		Container(Attrs(Row, CrossMid, Gap(6)), func() {
			Label("run to cycle", FontSize(12))
			textField(&debugCyclesSpec, 110)
			if Button(NoIcon, "Go") {
				if n, err := parseDebugNumber(debugCyclesSpec); err == nil {
					emu.RunToCycle(n)
					RequestNextFrame()
				} else {
					debugMsg = err.Error()
				}
			}
		})
		if debugMsg != "" {
			Label(debugMsg, FontSize(11), TextColor(0, 65, 55, 1))
		}
	})
}

func debugStopText(s DebugSnapshot) string {
	switch {
	case s.Stopped && s.StopReason != "":
		return s.StopReason
	case s.Stopped:
		return "stopped"
	case s.PC == 0:
		return "no firmware"
	default:
		return "running"
	}
}

func debugBreakpoints(s DebugSnapshot) {
	Container(Attrs(Gap(5), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Label(fmt.Sprintf("Breakpoints · %d", len(s.Breakpoints)), FontSize(14), FontWeight(700))
		Container(Attrs(Row, CrossMid, Gap(5)), func() {
			textField(&debugBreakSpec, 190)
			if Button(NoIcon, "Add") {
				if addr, err := emu.AddBreakpoint(debugBreakSpec); err != nil {
					debugMsg = err.Error()
				} else {
					debugMsg = fmt.Sprintf("breakpoint at %#x", addr)
					debugBreakSpec = ""
				}
				RequestNextFrame()
			}
		})
		if len(s.Breakpoints) == 0 {
			Label("address or symbol, e.g. main or 0x10001a34", FontSize(11), TextColor(220, 6, 62, 1))
		}
		for _, bp := range s.Breakpoints {
			Container(Attrs(Row, CrossMid, Gap(6)), func() {
				Label(fmt.Sprintf("%08x", bp.Addr), Fonts(Monospace...), FontSize(11))
				label := bp.Symbol
				if label == "" {
					label = "address"
				}
				Label(label, FontSize(11), FontWeight(600))
				if bp.File != "" {
					Label(fmt.Sprintf("%s:%d", baseName(bp.File), bp.Line), FontSize(10),
						TextColor(220, 6, 65, 1))
				}
				Filler(1)
				if Button(NoIcon, "×") {
					emu.RemoveBreakpoint(bp.Addr)
					RequestNextFrame()
				}
			})
		}
	})
}

func debugWatches(s DebugSnapshot) {
	Container(Attrs(Gap(5), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Label(fmt.Sprintf("Write watchpoints · %d", len(s.Watches)), FontSize(14), FontWeight(700))
		Container(Attrs(Row, CrossMid, Gap(5)), func() {
			textField(&debugWatchSpec, 190)
			if Button(NoIcon, "Add") {
				if _, err := emu.AddWatch(debugWatchSpec, 4); err != nil {
					debugMsg = err.Error()
				} else {
					debugMsg = "watchpoint added"
					debugWatchSpec = ""
				}
				RequestNextFrame()
			}
		})
		for _, w := range s.Watches {
			Container(Attrs(Row, CrossMid, Gap(6)), func() {
				Label(fmt.Sprintf("%08x · %d bytes", w.Addr, w.Size), Fonts(Monospace...), FontSize(11))
				Filler(1)
				if Button(NoIcon, "×") {
					emu.RemoveWatch(w.Addr)
					RequestNextFrame()
				}
			})
		}
	})
}

func debugSource(s DebugSnapshot) {
	Container(Attrs(Grow(1), Expand, Gap(3), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Source", FontSize(14), FontWeight(700))
			if s.Function != "" {
				Label(s.Function, FontSize(12), FontWeight(600), TextColor(140, 40, 70, 1))
			}
			if s.SourceFile != "" {
				Label(fmt.Sprintf("%s:%d", baseName(s.SourceFile), s.SourceLine), FontSize(11),
					TextColor(220, 6, 68, 1))
			}
		})
		if len(s.SourceLines) == 0 {
			if s.SourceFile != "" {
				Label(s.SourceFile, Fonts(Monospace...), FontSize(11))
			}
			Label("source is not available on this machine", FontSize(11), TextColor(220, 6, 62, 1))
			return
		}
		for n, line := range s.SourceLines {
			ln := s.SourceStart + n
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				if ln == s.SourceLine {
					ModAttrs(Background(140, 25, 15, 1))
				}
				Label(fmt.Sprintf("%4d", ln), Fonts(Monospace...), FontSize(11),
					TextColor(220, 6, 60, 1))
				Label(strings.TrimRight(line, "\t"), Fonts(Monospace...), FontSize(11),
					TextColor(220, 8, 88, 1))
			})
		}
	})
}

func debugRegisters(s DebugSnapshot) {
	Container(Attrs(Gap(2), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Label("Registers", FontSize(14), FontWeight(700))
		for r := 0; r < 16; r += 2 {
			Container(Attrs(Row, Gap(10)), func() {
				Label(fmt.Sprintf("R%-2d %08x", r, s.Registers[r]), Fonts(Monospace...), FontSize(11))
				Label(fmt.Sprintf("R%-2d %08x", r+1, s.Registers[r+1]), Fonts(Monospace...), FontSize(11))
			})
		}
		Label(fmt.Sprintf("PC %08x  SP %08x", s.PC, s.SP), Fonts(Monospace...), FontSize(11), FontWeight(600))
		Label(fmt.Sprintf("APSR %08x  IPSR %d", s.APSR, s.IPSR), Fonts(Monospace...), FontSize(10))
		Label(fmt.Sprintf("CONTROL %x  MSP %08x  PSP %08x", s.CONTROL, s.MSP, s.PSP),
			Fonts(Monospace...), FontSize(10))
	})
}

func debugMemory(s DebugSnapshot) {
	if debugMemSpec == "" {
		debugMemSpec = fmt.Sprintf("%#x", s.PC)
	}
	Container(Attrs(Gap(5), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Container(Attrs(Row, CrossMid, Gap(5)), func() {
			Label("Memory", FontSize(14), FontWeight(700))
			textField(&debugMemSpec, 150)
			if Button(NoIcon, "PC") {
				debugMemSpec = fmt.Sprintf("%#x", s.PC)
				RequestNextFrame()
			}
			if Button(NoIcon, "SP") {
				debugMemSpec = fmt.Sprintf("%#x", s.SP)
				RequestNextFrame()
			}
		})
		base, ok := debugAddress(debugMemSpec)
		if !ok {
			Label("enter a hex address", FontSize(11), TextColor(220, 6, 62, 1))
			return
		}
		data, ok := emu.ReadMemory(base, 128)
		if !ok {
			Label(fmt.Sprintf("%08x is not readable memory", base), FontSize(11), TextColor(0, 65, 55, 1))
			return
		}
		for row := 0; row < len(data); row += 16 {
			Container(Attrs(Row, Gap(8)), func() {
				Label(fmt.Sprintf("%08x", base+uint32(row)), Fonts(Monospace...), FontSize(10),
					TextColor(220, 6, 60, 1))
				var hex strings.Builder
				for _, b := range data[row : row+16] {
					fmt.Fprintf(&hex, "%02x ", b)
				}
				Label(strings.TrimSpace(hex.String()), Fonts(Monospace...), FontSize(10))
				var ascii strings.Builder
				for _, b := range data[row : row+16] {
					if b >= 32 && b < 127 {
						ascii.WriteByte(b)
					} else {
						ascii.WriteByte('.')
					}
				}
				Label(ascii.String(), Fonts(Monospace...), FontSize(10), TextColor(220, 6, 70, 1))
			})
		}
	})
}

func debugVariables(s DebugSnapshot) {
	Container(Attrs(Grow(1), Expand, Gap(3), Pad(10), Corners(6), Background(220, 12, 10, 1),
		BorderWidth(1), BorderColor(220, 8, 25, 1)), func() {
		Label("Variables", FontSize(14), FontWeight(700))
		ScrollOnInput()
		ScrollBars()
		if len(s.Locals) == 0 {
			Label("no locals at this PC", FontSize(11), TextColor(220, 6, 62, 1))
		}
		for _, v := range s.Locals {
			debugVariableRow(v)
		}
		Label("Global objects", FontSize(12), FontWeight(600))
		for n, v := range s.Globals {
			if strings.HasPrefix(v.Name, "_") {
				continue
			}
			if n >= 40 {
				Label(fmt.Sprintf("… %d more globals", len(s.Globals)-n), FontSize(10),
					TextColor(220, 6, 62, 1))
				break
			}
			debugVariableRow(v)
		}
	})
}

func debugVariableRow(v debug.Variable) {
	Container(Attrs(Row, CrossMid, Gap(8)), func() {
		Label(v.Name, FontSize(11), FontWeight(600))
		if v.Type != "" && v.Scope != "global" {
			Label(v.Type, FontSize(10), TextColor(220, 6, 65, 1))
		}
		Filler(1)
		value := v.Value
		if !v.Available {
			value = v.Note
			if value == "" {
				value = "unavailable"
			}
		}
		Label(value, Fonts(Monospace...), FontSize(11))
	})
}

func textField(s *string, width float32) {
	attrs := DefaultTextInputAttrs()
	attrs.MinWidth = width
	attrs.FixedWidth = true
	attrs.NoAutoFocus = true
	TextInputExt(s, attrs)
}

func parseDebugNumber(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	n, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return n, nil
}

func debugAddress(s string) (uint32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		return path[i+1:]
	}
	return path
}
