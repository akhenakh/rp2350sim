package main

import (
	"fmt"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/coverage"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// profilePanel shows the sampled PC profile, exact instruction/line coverage,
// IRQ entry latency and the sampled stack watermarks.
func profilePanel(snap MachineState) {
	active := emu.Profiling()
	rep := emu.ProfileReport()
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("Profile", FontSize(16), FontWeight(700))
			if active {
				if Button(NoIcon, "Stop") {
					emu.SetProfiling(false)
					RequestNextFrame()
				}
				Label("sampling", FontSize(12), TextColor(40, 75, 65, 1))
			} else {
				if Button(NoIcon, "Start") {
					emu.SetProfiling(true)
					RequestNextFrame()
				}
				Label("stopped", FontSize(12), TextColor(220, 6, 62, 1))
			}
			Label("PC every 2048 cycles · instruction coverage exact while running",
				FontSize(11), TextColor(220, 6, 68, 1))
			Filler(1)
			if snap.Firmware != "" {
				Label(snap.Firmware, FontSize(11), TextColor(220, 6, 68, 1))
			}
		})

		if rep == nil {
			Label("No profile data. Load an ELF firmware and press Start.", FontSize(13),
				TextColor(220, 6, 68, 1))
			return
		}

		profileSummary(rep)
		Container(Attrs(Row, Grow(1), Expand, Gap(10)), func() {
			profileFunctions(rep)
			Container(Attrs(FixWidth(float32(viewW)*0.34), Expand, Gap(10)), func() {
				profileIRQ(rep)
				profileStacks(rep)
			})
		})
	})
}

func profileSummary(rep *coverage.Report) {
	cover := 0.0
	if rep.TotalInstructions > 0 {
		cover = 100 * float64(rep.ExecutedInstructions) / float64(rep.TotalInstructions)
	}
	Container(Attrs(Row, Gap(10)), func() {
		statCard("Virtual time", fmt.Sprintf("%.1f ms", float64(rep.ElapsedCycles)/150_000))
		statCard("PC samples", fmt.Sprintf("%d", rep.TotalSamples))
		statCard("Instructions", fmt.Sprintf("%d / %d (%.1f%%)",
			rep.ExecutedInstructions, rep.TotalInstructions, cover))
		statCard("IRQ entries", fmt.Sprintf("%d · slowest %.2f µs",
			rep.IRQCount, float64(rep.IRQSlowest)/150))
	})
}

func statCard(title, value string) {
	Container(Attrs(Grow(1), Gap(2), Pad(10), Corners(8), Background(220, 12, 12, 1),
		BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
		Label(title, FontSize(11), TextColor(220, 6, 68, 1))
		Label(value, FontSize(17), FontWeight(600), Fonts(Monospace...))
	})
}

func profileFunctions(rep *coverage.Report) {
	Container(Attrs(Grow(1), Expand, Gap(4), Pad2(4, 8), Background(220, 10, 12, 1),
		Corners(6), Clip, Viewport), func() {
		ScrollOnInput()
		Label("Functions by sampled cycles", FontSize(12), FontWeight(700))
		Container(Attrs(Row, Gap(8)), func() {
			colLabel(240, "function", FontSize(10), TextColor(220, 6, 68, 1))
			colLabel(90, "cycles", FontSize(10), TextColor(220, 6, 68, 1))
			colLabel(150, "share", FontSize(10), TextColor(220, 6, 68, 1))
			colLabel(70, "samples", FontSize(10), TextColor(220, 6, 68, 1))
			colLabel(70, "instr %", FontSize(10), TextColor(220, 6, 68, 1))
			Label("hits", FontSize(10), TextColor(220, 6, 68, 1))

		})
		limit := min(len(rep.Functions), 300)
		for _, f := range rep.Functions[:limit] {
			if f.Samples == 0 && f.Hits == 0 {
				continue
			}
			profileFunctionRow(rep, f)
		}
	})
}

func profileFunctionRow(rep *coverage.Report, f coverage.FunctionStat) {
	var share float64
	if rep.TotalSamples > 0 {
		share = 100 * float64(f.Samples) / float64(rep.TotalSamples)
	} else if rep.TotalHits > 0 {
		share = 100 * float64(f.Hits) / float64(rep.TotalHits)
	}
	instr := 0.0
	if f.Instructions > 0 {
		instr = 100 * float64(f.Executed) / float64(f.Instructions)
	}
	name := f.Name
	if len(name) > 44 {
		name = "…" + name[len(name)-43:]
	}
	bars := min(int(share/2+0.5), 50)
	Container(Attrs(Row, Gap(8)), func() {
		colLabel(240, name, Fonts(Monospace...), FontSize(11))
		colLabel(90, fmt.Sprintf("%.1f ms", float64(f.Cycles)/150_000), Fonts(Monospace...), FontSize(11))
		colLabel(150, strings.Repeat("▇", bars), Fonts(Monospace...), FontSize(11),
			TextColor(150, 55, 70, 1))
		colLabel(70, fmt.Sprintf("%5.1f%%", share), Fonts(Monospace...), FontSize(11))
		colLabel(70, fmt.Sprintf("%5.1f%%", instr), Fonts(Monospace...), FontSize(11))
		Label(fmt.Sprintf("%d", f.Hits), Fonts(Monospace...), FontSize(11),
			TextColor(220, 6, 68, 1))
	})
}

func profileIRQ(rep *coverage.Report) {
	Container(Attrs(Gap(4), Pad(10), Corners(8), Background(220, 12, 12, 1),
		BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
		Label("IRQ entry latency", FontSize(12), FontWeight(700))
		Label(fmt.Sprintf("%d entries · slowest %.2f µs", rep.IRQCount,
			float64(rep.IRQSlowest)/150), FontSize(11), TextColor(220, 6, 68, 1))
		if rep.IRQCount == 0 {
			Label("no peripheral interrupt entered", FontSize(11))
			return
		}
		var max uint64
		for _, n := range rep.IRQHist {
			if n > max {
				max = n
			}
		}
		labels := coverage.IRQBucketLabels(len(rep.IRQHist))
		for i, n := range rep.IRQHist {
			if n == 0 {
				continue
			}
			bars := 0
			if max > 0 {
				bars = int(n * 24 / max)
			}
			Label(fmt.Sprintf("%10s cyc %5d %s", labels[i], n, strings.Repeat("▇", bars)),
				Fonts(Monospace...), FontSize(10))
		}
	})
}

func profileStacks(rep *coverage.Report) {
	Container(Attrs(Gap(4), Pad(10), Corners(8), Background(220, 12, 12, 1),
		BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
		Label("Stack low-water (sampled)", FontSize(12), FontWeight(700))
		for core, w := range rep.Stack {
			msp := "—"
			if w.MSPValid {
				msp = fmt.Sprintf("%#08x", w.MSPMin)
			}
			psp := "—"
			if w.PSPValid {
				psp = fmt.Sprintf("%#08x", w.PSPMin)
			}
			Label(fmt.Sprintf("core%d  MSP %s  PSP %s", core, msp, psp),
				Fonts(Monospace...), FontSize(11))
		}
	})
}

// colLabel renders a fixed-width table cell.
func colLabel(w float32, text string, attrs ...TextStyleFn) {
	Container(Attrs(FixWidth(w), Clip), func() {
		Label(text, attrs...)
	})
}
