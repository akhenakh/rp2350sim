package main

import (
	"fmt"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/rp2350"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

const maxTraceLine = 4096

type traceLine struct {
	cycle uint64
	port  int
	text  string
	kind  string
}

// tracePanel shows ITM stimulus writes collected through Chip.OnTrace.
func tracePanel() {
	events := emu.TraceEvents()
	Container(Attrs(Grow(1), Expand, Gap(8), Pad4(4, 6, 4, 6)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("Trace", FontSize(16), FontWeight(700))
			Label("ITM stimulus · DWT counters", FontSize(12))
			Filler(1)
			if len(events) > 0 {
				if Button(NoIcon, "Copy") {
					RequestTextCopy(traceText(events))
				}
			}
			if Button(NoIcon, "Clear") {
				emu.ClearTrace()
				RequestNextFrame()
			}
			Label(fmt.Sprintf("%d events", len(events)), FontSize(12),
				TextColor(220, 6, 68, 1))
		})
		Container(Attrs(Grow(1), Expand, Gap(4), Pad(10), Corners(8),
			Background(220, 12, 12, 1), BorderWidth(1), BorderColor(220, 8, 26, 1),
			Clip, Viewport), func() {
			ScrollOnInput()
			if len(events) == 0 {
				Label("No trace events yet. Firmware writes ITM stimulus ports, or run with -itm from the CLI.",
					FontSize(12), TextColor(220, 6, 68, 1))
				Label("Try firmware/c/build/itmdemo.elf: it writes through ITM without UART or USB.",
					FontSize(11), TextColor(220, 6, 68, 1))
				return
			}
			lines := traceLines(events)
			for _, line := range lines {
				Container(Attrs(Row, Gap(10)), func() {
					colLabel(90, fmt.Sprintf("%8.2f ms", float64(line.cycle)/150_000),
						Fonts(Monospace...), FontSize(11))
					colLabel(55, fmt.Sprintf("port %d", line.port), Fonts(Monospace...), FontSize(11))
					Label(line.text, Fonts(Monospace...), FontSize(12))
				})
			}
		})
		Label("ITM writes are gated by TER/TCR; DWT CYCCNT and PCSR are readable from firmware.",
			FontSize(11), TextColor(220, 6, 68, 1))
	})
}

// traceText renders the visible trace as plain text for the Copy button.
func traceText(events []rp2350.TraceEvent) string {
	var b strings.Builder
	b.WriteString("rp2350sim trace\n")
	fmt.Fprintf(&b, "Events: %d\n\n", len(events))
	for _, line := range traceLines(events) {
		ms := float64(line.cycle) / 150_000
		if line.kind != "" {
			fmt.Fprintf(&b, "%8.2f ms  %s\n", ms, line.text)
		} else {
			fmt.Fprintf(&b, "%8.2f ms  port %d  %s\n", ms, line.port, line.text)
		}
	}
	return b.String()
}

func traceLines(events []rp2350.TraceEvent) []traceLine {
	var out []traceLine
	current := traceLine{port: -1}
	flush := func() {
		if current.text != "" || current.kind != "" {
			out = append(out, current)
		}
		current = traceLine{port: -1}
	}
	for _, ev := range events {
		if ev.Kind == "itm" {
			if current.port == -1 {
				current.cycle, current.port = ev.Cycle, ev.Port
			}
			if len(current.text) < maxTraceLine {
				current.text += ev.Text
				if len(current.text) > maxTraceLine {
					current.text = current.text[:maxTraceLine]
				}
			}
			if strings.HasSuffix(current.text, "\n") {
				current.text = strings.TrimRight(current.text, "\r\n")
				flush()
			}
			continue
		}
		flush()
		out = append(out, traceLine{cycle: ev.Cycle, port: ev.Port,
			text: fmt.Sprintf("%s pc=%08x", ev.Kind, ev.PC), kind: ev.Kind})
	}
	flush()
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return out
}
