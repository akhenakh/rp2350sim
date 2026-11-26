package main

import (
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/coverage"
)

func TestProfileTextReport(t *testing.T) {
	rep := &coverage.Report{
		Symbolized:           true,
		ElapsedCycles:        150_000, // 1 ms at 150 MHz
		TotalSamples:         10,
		SampleCycles:         2048,
		TotalInstructions:    20,
		ExecutedInstructions: 10,
		IRQCount:             2,
		IRQSlowest:           300,
		IRQHist:              []uint64{1, 2},
		Functions: []coverage.FunctionStat{{
			Name: "main.hot", Cycles: 150_000, Samples: 5, Hits: 7,
			Instructions: 10, Executed: 5,
		}},
		Stack: [2]coverage.StackWater{{MSPMin: 0x20001000, MSPValid: true}},
	}

	got := profileText(rep, "build/app.elf")
	for _, want := range []string{
		"rp2350sim profile",
		"Firmware: build/app.elf",
		"Virtual time: 1.000 ms",
		"PC samples: 10 (every 2048 cycles)",
		"Instructions: 10 / 20 (50.0%)",
		"IRQ entries: 2, slowest 2.00 us",
		"core0  MSP 0x20001000  PSP —",
		"Functions by sampled cycles:",
		"main.hot",
		"50.0%",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("profile text missing %q:\n%s", want, got)
		}
	}

	rep.Symbolized = false
	if got := profileText(rep, ""); !strings.Contains(got, "PC buckets by sampled cycles:") {
		t.Fatalf("unsymbolized report has no bucket heading:\n%s", got)
	}
}
