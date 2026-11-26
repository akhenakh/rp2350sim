// Package coverage turns raw PC execution counts and PC samples into a
// function/line report and LCOV output. It depends only on the ELF debug view,
// never on the emulator, so the CLI and the UI share one implementation.
package coverage

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/akhenakh/rp2350sim/internal/debug"
)

// StackWater is the lowest stack pointer seen for one core, mirroring
// rp2350.ProfileData.
type StackWater struct {
	MSPMin   uint32
	PSPMin   uint32
	MSPValid bool
	PSPValid bool
}

// Input is the raw data a report is built from.
type Input struct {
	// Counts is the exact PC execution map; nil disables instruction and line
	// coverage, leaving only the sampled profile.
	Counts map[uint32]uint64
	// Samples maps a sampled PC to its sample count.
	Samples map[uint32]uint64
	// SampleCycles is the PC sampling period in chip cycles.
	SampleCycles uint64
	// ElapsedCycles is the profiled virtual time.
	ElapsedCycles uint64
	IRQHist       []uint64
	IRQSlowest    uint64
	IRQCount      uint64
	Stack         [2]StackWater
}

// FunctionStat is one function's profile and coverage.
type FunctionStat struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
	Addr uint32 `json:"addr"`
	Size uint32 `json:"size"`
	// Instructions is the decoded static instruction count, Executed the
	// number of distinct instructions that ran, and Hits the total execution
	// count (what LCOV's FNDA reports).
	Instructions uint64 `json:"instructions"`
	Executed     uint64 `json:"executed"`
	Hits         uint64 `json:"hits"`
	Samples      uint64 `json:"samples"`
	Cycles       uint64 `json:"cycles"`
}

// LineStat is one source line's execution count.
type LineStat struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Count uint64 `json:"count"`
}

// Report is a built profile view.
type Report struct {
	Functions []FunctionStat `json:"functions"`
	Lines     []LineStat     `json:"lines"`

	TotalInstructions    uint64 `json:"total_instructions"`
	ExecutedInstructions uint64 `json:"executed_instructions"`
	TotalHits            uint64 `json:"total_hits"`
	TotalSamples         uint64 `json:"total_samples"`
	SampleCycles         uint64 `json:"sample_cycles"`
	ElapsedCycles        uint64 `json:"elapsed_cycles"`

	IRQHist    []uint64      `json:"irq_hist"`
	IRQSlowest uint64        `json:"irq_slowest"`
	IRQCount   uint64        `json:"irq_count"`
	Stack      [2]StackWater `json:"stack"`
}

// Symbolizer is the subset of debug.Info the report needs.
type Symbolizer interface {
	CoverageFunctions() []debug.Function
	CoverageLines() []debug.LineRow
}

// Build maps raw counts and samples onto functions and lines. read fetches
// code bytes for the static instruction count and may be nil; when it is, the
// instruction totals stay zero and only sample/line data is available.
func Build(src Symbolizer, in Input, read func(addr uint32, size int) ([]byte, bool)) *Report {
	rep := &Report{
		SampleCycles:  in.SampleCycles,
		ElapsedCycles: in.ElapsedCycles,
		IRQHist:       append([]uint64(nil), in.IRQHist...),
		IRQSlowest:    in.IRQSlowest,
		IRQCount:      in.IRQCount,
		Stack:         in.Stack,
	}
	if src == nil {
		return rep
	}

	fns := src.CoverageFunctions()
	ranges := make([]fnRange, 0, len(fns))
	stats := make([]FunctionStat, 0, len(fns))
	for _, fn := range fns {
		if fn.Addr == 0 {
			continue
		}
		stats = append(stats, FunctionStat{
			Name: fn.Name, File: fn.File, Line: fn.Line, Addr: fn.Addr, Size: fn.Size,
		})
	}
	// Effective end: the symbol size, or the next function when the symbol
	// table leaves it at zero.
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Addr == stats[j].Addr {
			return stats[i].Name < stats[j].Name
		}
		return stats[i].Addr < stats[j].Addr
	})
	for i := range stats {
		end := stats[i].Addr + stats[i].Size
		if stats[i].Size == 0 && i+1 < len(stats) && stats[i+1].Addr > stats[i].Addr {
			end = stats[i+1].Addr
		}
		if end > stats[i].Addr {
			ranges = append(ranges, fnRange{idx: i, start: stats[i].Addr, end: end})
		}
	}

	if read != nil {
		for i := range stats {
			end := stats[i].Addr + stats[i].Size
			if stats[i].Size == 0 && i+1 < len(stats) && stats[i+1].Addr > stats[i].Addr {
				end = stats[i+1].Addr
			}
			stats[i].Instructions = staticInstructions(read, stats[i].Addr, end)
		}
	}

	assign := func(pc uint32) int {
		i := sort.Search(len(ranges), func(i int) bool { return ranges[i].start > pc })
		if i == 0 {
			return -1
		}
		r := ranges[i-1]
		if pc < r.end {
			return r.idx
		}
		return -1
	}

	if in.Counts != nil {
		lines := src.CoverageLines()
		lineCounts := make(map[lineKey]uint64, len(lines))
		for _, pc := range sortedKeys(in.Counts) {
			n := in.Counts[pc]
			rep.TotalHits += n
			idx := assign(pc)
			if idx >= 0 {
				stats[idx].Hits += n
				stats[idx].Executed++
			}
			if k, ok := lineAt(lines, pc); ok {
				lineCounts[k] += n
			}
		}
		// Every line-table row is an executable line, even at count zero.
		seen := make(map[lineKey]bool, len(lines))
		for _, row := range lines {
			k := lineKey{file: row.File, line: row.Line}
			if seen[k] {
				continue
			}
			seen[k] = true
			rep.Lines = append(rep.Lines, LineStat{File: row.File, Line: row.Line, Count: lineCounts[k]})
		}
		sort.Slice(rep.Lines, func(i, j int) bool {
			if rep.Lines[i].File == rep.Lines[j].File {
				return rep.Lines[i].Line < rep.Lines[j].Line
			}
			return rep.Lines[i].File < rep.Lines[j].File
		})
	}

	if in.Samples != nil {
		for _, pc := range sortedKeys(in.Samples) {
			rep.TotalSamples += in.Samples[pc]
			if idx := assign(pc); idx >= 0 {
				stats[idx].Samples += in.Samples[pc]
			}
		}
	}

	for i := range stats {
		stats[i].Cycles = stats[i].Samples * in.SampleCycles
		rep.TotalInstructions += stats[i].Instructions
		rep.ExecutedInstructions += stats[i].Executed
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Cycles != stats[j].Cycles {
			return stats[i].Cycles > stats[j].Cycles
		}
		if stats[i].Samples != stats[j].Samples {
			return stats[i].Samples > stats[j].Samples
		}
		return stats[i].Name < stats[j].Name
	})
	rep.Functions = stats
	return rep
}

type fnRange struct {
	idx        int
	start, end uint32
}

type lineKey struct {
	file string
	line int
}

func sortedKeys(m map[uint32]uint64) []uint32 {
	keys := make([]uint32, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// lineAt finds the last statement row at or before pc, matching debug.LineAt.
func lineAt(rows []debug.LineRow, pc uint32) (lineKey, bool) {
	i := sort.Search(len(rows), func(i int) bool { return rows[i].Addr > pc })
	if i == 0 {
		return lineKey{}, false
	}
	row := rows[i-1]
	if row.File == "" || row.Line <= 0 {
		return lineKey{}, false
	}
	return lineKey{file: row.File, line: row.Line}, true
}

// staticInstructions counts Thumb instructions in [start, end) using the same
// width rule as the executor: a first halfword of 0xE800 or above is the first
// half of a 32-bit instruction.
func staticInstructions(read func(addr uint32, size int) ([]byte, bool), start, end uint32) uint64 {
	if end <= start {
		return 0
	}
	size := min(int(end-start), 1<<20)
	data, ok := read(start, size)
	if !ok {
		return 0
	}
	var n uint64
	for i := 0; i+1 < len(data); {
		hw := uint16(data[i]) | uint16(data[i+1])<<8
		if hw >= 0xE800 {
			if i+3 >= len(data) {
				break
			}
			i += 4
		} else {
			i += 2
		}
		n++
	}
	return n
}

// WriteLCOV writes the report as LCOV tracefile text. Lines are emitted for
// every DWARF statement row; function hit counts are instruction executions.
func WriteLCOV(w io.Writer, rep *Report) error {
	bw := bufio.NewWriter(w)
	files := make(map[string][]LineStat)
	for _, l := range rep.Lines {
		files[l.File] = append(files[l.File], l)
	}
	fnFiles := make(map[string][]FunctionStat)
	for _, f := range rep.Functions {
		if f.File != "" {
			fnFiles[f.File] = append(fnFiles[f.File], f)
		}
	}
	names := make([]string, 0, len(files)+len(fnFiles))
	seen := map[string]bool{}
	for name := range files {
		names = append(names, name)
		seen[name] = true
	}
	for name := range fnFiles {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, file := range names {
		if _, err := fmt.Fprintf(bw, "TN:\nSF:%s\n", file); err != nil {
			return err
		}
		fns := fnFiles[file]
		sort.Slice(fns, func(i, j int) bool {
			if fns[i].Line == fns[j].Line {
				return fns[i].Name < fns[j].Name
			}
			return fns[i].Line < fns[j].Line
		})
		for _, f := range fns {
			if _, err := fmt.Fprintf(bw, "FN:%d,%s\nFNDA:%d,%s\n", f.Line, f.Name, f.Hits, f.Name); err != nil {
				return err
			}
		}
		var found, hit uint64
		for _, f := range fns {
			if f.Hits > 0 {
				hit++
			}
		}
		found = uint64(len(fns))
		if _, err := fmt.Fprintf(bw, "FNF:%d\nFNH:%d\n", found, hit); err != nil {
			return err
		}
		lines := files[file]
		var lf, lh uint64
		for _, l := range lines {
			if _, err := fmt.Fprintf(bw, "DA:%d,%d\n", l.Line, l.Count); err != nil {
				return err
			}
			lf++
			if l.Count > 0 {
				lh++
			}
		}
		if _, err := fmt.Fprintf(bw, "LF:%d\nLH:%d\nend_of_record\n", lf, lh); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// IRQBucketLabels names the latency histogram buckets in cycles.
func IRQBucketLabels(n int) []string {
	edges := []uint64{32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072}
	out := make([]string, 0, n)
	low := uint64(0)
	for i := 0; i < n-1 && i < len(edges); i++ {
		out = append(out, fmt.Sprintf("%d-%d", low, edges[i]-1))
		low = edges[i]
	}
	out = append(out, fmt.Sprintf("%d+", low))
	return out
}
