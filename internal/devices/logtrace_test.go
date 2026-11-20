package devices

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteLogicTrace(t *testing.T) {
	when := time.Date(2026, 11, 19, 21, 20, 30, 0, time.UTC)
	tr := LogicTrace{
		BoardID:   "pico2-analog",
		BoardName: "RP2350 + analog + protocols",
		Captured:  when,
		Interval:  LogicInterval,
		Pins:      []int{0, 1},
		Samples: []TraceSample{
			{T: 100, V: [MaxTraceChannels]float32{3.3, 0}},
			{T: 175, V: [MaxTraceChannels]float32{0, 3.3}},
			{T: 250, V: [MaxTraceChannels]float32{3.3, 0}},
		},
		Sections: []LogicSection{{Name: "UART", Detail: "115200 baud", Text: `"hi"`}},
	}
	var b bytes.Buffer
	if err := WriteLogicTrace(&b, tr); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# rp2350sim logic analyzer trace",
		"board: pico2-analog",
		"sample_interval_cycles: 75",
		"samples: 3",
		"channels: GP0 GP1",
		"# time_us GP0 GP1",
		"0.000 1 0",
		"0.500 0 1",
		"1.000 1 0",
		"# UART",
		`"hi"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trace does not contain %q:\n%s", want, out)
		}
	}
	if got := LogicTraceFilename("pico2-analog", when); got != "logic-analyze-trace-20261119-212030-pico2-analog.txt" {
		t.Fatalf("filename = %q", got)
	}
	if got := LogicTraceFilename("a/b c", when); got != "logic-analyze-trace-20261119-212030-a-b-c.txt" {
		t.Fatalf("sanitized filename = %q", got)
	}
}
