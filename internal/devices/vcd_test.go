package devices

import (
	"strings"
	"testing"
)

func TestWriteVCD(t *testing.T) {
	samples := []TraceSample{
		{T: 0, V: [MaxTraceChannels]float32{0, 3.3}},
		{T: 150, V: [MaxTraceChannels]float32{3.3, 3.3}},
		{T: 300, V: [MaxTraceChannels]float32{3.3, 0}},
		{T: 450, V: [MaxTraceChannels]float32{0, 0}},
	}
	var b strings.Builder
	if err := WriteVCD(&b, []int{5, 6}, samples, 150); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"$timescale 1ns $end",
		"$comment sample interval 1000 ns $end",
		"$var wire 1 ! GP5 $end",
		"$var wire 1 \" GP6 $end",
		"$dumpvars",
		"0!",
		"1\"",
		"#1000",
		"1!",
		"#2000",
		"0\"",
		"#3000",
		"0!",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("VCD missing %q:\n%s", want, out)
		}
	}
}

func TestWriteVCDEmpty(t *testing.T) {
	var b strings.Builder
	if err := WriteVCD(&b, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "$enddefinitions $end") {
		t.Fatalf("bad empty VCD:\n%s", b.String())
	}
}
