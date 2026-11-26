package rp2350

import "maps"

// Profiling collects two independent views of a run:
//
//   - exact instruction coverage: PC -> execution count, opt-in because it
//     touches a map on every instruction;
//   - a PC sample every SampleCycles virtual cycles, the basis for per-function
//     cycle attribution and a flat profile.
//
// It also records the exception latency (peripheral pend -> handler entry) and
// the low-water mark of each stack pointer, both sampled at the same period as
// the PC samples. Nothing here uses the wall clock, so profiling a run twice
// yields the same numbers.

// irqHistogramBuckets counts entries by cycles from pend to handler entry.
// Boundaries are 32, 64, ... up to 131072 cycles (0.21 us to 0.87 ms at
// 150 MHz); the last bucket is everything slower.
const (
	irqHistogramBuckets = 14
	irqIrqCount         = 64
)

var irqBucketEdges = [irqHistogramBuckets - 1]uint64{
	32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072,
}

// ProfileConfig selects what a profile run records.
type ProfileConfig struct {
	// SampleCycles is the PC sampling period in chip cycles. Zero uses 4096
	// (about 27 us at 150 MHz).
	SampleCycles uint64
	// Coverage enables the exact PC execution map.
	Coverage bool
}

// StackWater is the lowest stack pointer seen for one core.
type StackWater struct {
	MSPMin   uint32
	PSPMin   uint32
	MSPValid bool
	PSPValid bool
}

// ProfileData is an immutable snapshot of a profile.
type ProfileData struct {
	SampleCycles  uint64
	StartCycles   uint64
	ElapsedCycles uint64
	// Samples maps a sampled PC to its sample count.
	Samples map[uint32]uint64
	// Coverage maps an executed PC to its instruction count; nil when exact
	// coverage was not enabled.
	Coverage map[uint32]uint64
	// CoreSamples is the number of samples taken while each core ran.
	CoreSamples [2]uint64
	// IRQHist counts handler entries by latency bucket; IRQSlowest is the
	// largest observed latency in cycles and IRQCount the entries measured.
	IRQHist    [irqHistogramBuckets]uint64
	IRQSlowest uint64
	IRQCount   uint64
	Stack      [2]StackWater
}

type profileState struct {
	sampleCycles uint64
	coverage     bool
	startCycles  uint64
	nextSample   uint64

	samples     map[uint32]uint64
	counts      map[uint32]uint64
	coreSamples [2]uint64

	irqHist    [irqHistogramBuckets]uint64
	irqSlowest uint64
	irqCount   uint64
	// pendAt/pendSet timestamp a pending peripheral IRQ until its handler
	// entry, per core.
	pendAt  [2][irqIrqCount]uint64
	pendSet [2][irqIrqCount]bool

	stack [2]StackWater
}

// StartProfile begins a new profile run, discarding any previous data.
func (c *Chip) StartProfile(cfg ProfileConfig) {
	period := cfg.SampleCycles
	if period == 0 {
		period = 4096
	}
	p := &profileState{
		sampleCycles: period,
		coverage:     cfg.Coverage,
		startCycles:  c.Cycles,
		nextSample:   c.Cycles + period,
		samples:      make(map[uint32]uint64, 1024),
	}
	if cfg.Coverage {
		p.counts = make(map[uint32]uint64, 4096)
	}
	c.profile = p
	c.profActive = true
}

// StopProfile stops collecting but keeps the data for ProfileData.
func (c *Chip) StopProfile() { c.profActive = false }

// Profiling reports whether profile collection is running.
func (c *Chip) Profiling() bool { return c.profActive && c.profile != nil }

// ClearProfile discards the current profile.
func (c *Chip) ClearProfile() {
	c.profActive = false
	c.profile = nil
}

// ProfileData returns a copy of the collected profile.
func (c *Chip) ProfileData() (ProfileData, bool) {
	p := c.profile
	if p == nil {
		return ProfileData{}, false
	}
	elapsed := uint64(0)
	if c.Cycles >= p.startCycles {
		elapsed = c.Cycles - p.startCycles
	}
	d := ProfileData{
		SampleCycles:  p.sampleCycles,
		StartCycles:   p.startCycles,
		ElapsedCycles: elapsed,
		CoreSamples:   p.coreSamples,
		IRQHist:       p.irqHist,
		IRQSlowest:    p.irqSlowest,
		IRQCount:      p.irqCount,
		Stack:         p.stack,
	}
	if p.samples != nil {
		d.Samples = maps.Clone(p.samples)
	}
	if p.counts != nil {
		d.Coverage = maps.Clone(p.counts)
	}
	return d, true
}

// observe records one instruction about to execute.
func (p *profileState) observe(c *Chip, core int, pc uint32, sp uint32, psp bool) {
	if p.counts != nil {
		p.counts[pc]++
	}
	if c.Cycles < p.nextSample {
		return
	}
	p.samples[pc]++
	p.coreSamples[core]++
	p.nextSample = c.Cycles + p.sampleCycles
	w := &p.stack[core]
	if psp {
		if !w.PSPValid || sp < w.PSPMin {
			w.PSPMin = sp
			w.PSPValid = true
		}
	} else {
		if !w.MSPValid || sp < w.MSPMin {
			w.MSPMin = sp
			w.MSPValid = true
		}
	}
}

// notePend timestamps a newly pending peripheral IRQ on one core.
func (c *Chip) noteIRQPend(core, irq int) {
	p := c.profile
	if !c.profActive || p == nil || core < 0 || core > 1 || irq < 0 || irq >= irqIrqCount {
		return
	}
	if !p.pendSet[core][irq] {
		p.pendSet[core][irq] = true
		p.pendAt[core][irq] = c.Cycles
	}
}

// noteIRQUnpend drops a timestamp when an IRQ line deasserts before entry.
func (c *Chip) noteIRQUnpend(core, irq int) {
	p := c.profile
	if !c.profActive || p == nil || core < 0 || core > 1 || irq < 0 || irq >= irqIrqCount {
		return
	}
	p.pendSet[core][irq] = false
}

// noteIRQEntry records the latency of an external exception that just entered.
func (c *Chip) noteIRQEntry(core int, exc uint32) {
	p := c.profile
	if !c.profActive || p == nil || core < 0 || core > 1 || exc < 16 {
		return
	}
	irq := int(exc) - 16
	if irq >= irqIrqCount || !p.pendSet[core][irq] {
		return
	}
	p.pendSet[core][irq] = false
	latency := c.Cycles - p.pendAt[core][irq]
	if latency > p.irqSlowest {
		p.irqSlowest = latency
	}
	p.irqCount++
	for i, edge := range irqBucketEdges {
		if latency < edge {
			p.irqHist[i]++
			return
		}
	}
	p.irqHist[irqHistogramBuckets-1]++
}

// profileOnReset drops pending timestamps and rebases the profile clock. A
// chip reset returns Cycles to zero; without the rebase ElapsedCycles would
// underflow by the pre-reset virtual time.
func (c *Chip) profileOnReset() {
	p := c.profile
	if p == nil {
		return
	}
	p.pendSet = [2][irqIrqCount]bool{}
	p.startCycles = c.Cycles
	p.nextSample = c.Cycles + p.sampleCycles
}
