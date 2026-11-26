package coverage

import (
	"bytes"
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/debug"
)

type fakeSymbols struct {
	fns   []debug.Function
	lines []debug.LineRow
}

func (f fakeSymbols) CoverageFunctions() []debug.Function { return f.fns }
func (f fakeSymbols) CoverageLines() []debug.LineRow      { return f.lines }

func TestBuildFunctionsLinesAndLCOV(t *testing.T) {
	code := []byte{
		0x01, 0x30, // adds r0, #1
		0x01, 0x30, // adds r0, #1
		0x02, 0x30, // adds r0, #2
		0xFE, 0xE7, // b .
	}
	src := fakeSymbols{
		fns: []debug.Function{
			{Name: "f1", File: "a.c", Line: 10, Addr: 0x1000, Size: 4},
			{Name: "f2", File: "a.c", Line: 12, Addr: 0x1004, Size: 4},
		},
		lines: []debug.LineRow{
			{Addr: 0x1000, File: "a.c", Line: 10},
			{Addr: 0x1002, File: "a.c", Line: 11},
			{Addr: 0x1004, File: "a.c", Line: 12},
			{Addr: 0x1006, File: "a.c", Line: 13},
		},
	}
	read := func(addr uint32, size int) ([]byte, bool) {
		if addr < 0x1000 || addr+uint32(size) > 0x1000+uint32(len(code)) {
			return nil, false
		}
		off := int(addr - 0x1000)
		return code[off : off+size], true
	}
	rep := Build(src, Input{
		Counts:        map[uint32]uint64{0x1000: 3, 0x1002: 2, 0x1004: 1},
		Samples:       map[uint32]uint64{0x1000: 4, 0x1004: 2},
		SampleCycles:  100,
		ElapsedCycles: 10_000,
	}, read)

	if len(rep.Functions) != 2 {
		t.Fatalf("functions = %d, want 2", len(rep.Functions))
	}
	f1, f2 := rep.Functions[0], rep.Functions[1]
	if f1.Name != "f1" || f1.Hits != 5 || f1.Executed != 2 || f1.Instructions != 2 {
		t.Fatalf("f1 = %+v", f1)
	}
	if f1.Samples != 4 || f1.Cycles != 400 {
		t.Fatalf("f1 samples = %d cycles = %d", f1.Samples, f1.Cycles)
	}
	if f2.Name != "f2" || f2.Hits != 1 || f2.Samples != 2 {
		t.Fatalf("f2 = %+v", f2)
	}
	if rep.TotalInstructions != 4 || rep.ExecutedInstructions != 3 || rep.TotalHits != 6 {
		t.Fatalf("totals = %+v", rep)
	}
	if len(rep.Lines) != 4 {
		t.Fatalf("lines = %d, want 4", len(rep.Lines))
	}
	counts := map[int]uint64{}
	for _, l := range rep.Lines {
		counts[l.Line] = l.Count
	}
	if counts[10] != 3 || counts[11] != 2 || counts[12] != 1 || counts[13] != 0 {
		t.Fatalf("line counts = %v", counts)
	}

	var out bytes.Buffer
	if err := WriteLCOV(&out, rep); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"SF:a.c\n",
		"FN:10,f1\nFNDA:5,f1\n",
		"FN:12,f2\nFNDA:1,f2\n",
		"FNF:2\nFNH:2\n",
		"DA:10,3\n",
		"DA:13,0\n",
		"LF:4\nLH:3\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("LCOV missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSampledOnly(t *testing.T) {
	src := fakeSymbols{fns: []debug.Function{
		{Name: "hot", Addr: 0x2000, Size: 16},
	}}
	rep := Build(src, Input{
		Samples:      map[uint32]uint64{0x2000: 3, 0x3000: 1},
		SampleCycles: 50,
	}, nil)
	if len(rep.Functions) != 1 || rep.Functions[0].Samples != 3 || rep.Functions[0].Cycles != 150 {
		t.Fatalf("functions = %+v", rep.Functions)
	}
	if rep.TotalSamples != 4 || rep.TotalInstructions != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.SampledPC(0x3000) {
		t.Fatal("PC outside every symbol should not attribute")
	}
}

// SampledPC is a test-only helper: it reports whether pc falls in a function.
func (r *Report) SampledPC(pc uint32) bool {
	for _, f := range r.Functions {
		if pc >= f.Addr && (f.Size == 0 || pc < f.Addr+f.Size) {
			return true
		}
	}
	return false
}
