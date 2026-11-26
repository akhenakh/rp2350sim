package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// MaxTraceChannels is the number of signals a Trace can record at once.
const MaxTraceChannels = 16

// Sampling rates used by the UI. The master clock is 150 MHz.
const (
	// ScopeInterval gives a 1 MHz sample rate and a 16 ms window.
	ScopeInterval = 150
	ScopeDepth    = 16384
	// LogicInterval doubles the resolution for protocol decoding; its window
	// is about 8 ms, enough for several UART/I2C/SPI frames.
	LogicInterval = 75
	LogicDepth    = 16384
)

// TraceSample is one sampled instant: the virtual cycle and the voltage of
// each configured channel.
type TraceSample struct {
	T uint64
	V [MaxTraceChannels]float32
}

// Bit reports the digital level of channel ch (3.3 V CMOS threshold).
func (s TraceSample) Bit(ch int) bool {
	if ch < 0 || ch >= MaxTraceChannels {
		return false
	}
	return s.V[ch] > 1.65
}

// Trace samples a set of pins at a fixed virtual-time interval into a ring.
// Register Tick with Chip.AddPeriodicHook; Snapshot must be called with the
// chip (and therefore the trace) locked by the owner.
type Trace struct {
	Chip     *rp2350.Chip
	Interval uint64

	pins        []int
	samples     []TraceSample
	head        int
	count       int
	next        uint64
	lastTick    uint64
	initialized bool
}

// NewTrace allocates a trace for the given pins. Depth is rounded up to at
// least one sample.
func NewTrace(chip *rp2350.Chip, pins []int, interval uint64, depth int) *Trace {
	if interval == 0 {
		interval = ScopeInterval
	}
	if depth <= 0 {
		depth = ScopeDepth
	}
	t := &Trace{
		Chip:     chip,
		Interval: interval,
		samples:  make([]TraceSample, depth),
		next:     chip.Cycles,
	}
	t.SetPins(pins)
	return t
}

// SetPins changes the channel list and clears the ring. Call it with the
// emulator paused or while holding the chip lock.
func (t *Trace) SetPins(pins []int) {
	if len(pins) > MaxTraceChannels {
		pins = pins[:MaxTraceChannels]
	}
	t.pins = append([]int(nil), pins...)
	t.head, t.count = 0, 0
	t.next = t.Chip.Cycles
	t.lastTick = t.Chip.Cycles
	t.initialized = true
}

// Pins returns the channel-to-pin mapping.
func (t *Trace) Pins() []int { return t.pins }

// LatestT returns the timestamp of the newest sample, or zero when the ring
// is empty. Trigger scanners use it to arm on future samples instead of
// replaying the captured history.
func (t *Trace) LatestT() uint64 {
	if t.count == 0 {
		return 0
	}
	return t.samples[(t.head-1+len(t.samples))%len(t.samples)].T
}

// Tick samples at most once per call. Missed slots are skipped while the
// future sampling phase is retained; a virtual-clock rewind clears history.
func (t *Trace) Tick() {
	now := t.Chip.Cycles
	if t.initialized && now < t.lastTick {
		t.head, t.count = 0, 0
		t.next = now
	}
	t.lastTick = now
	t.initialized = true
	if now < t.next {
		return
	}
	if t.Interval == 0 {
		t.Interval = ScopeInterval
	}
	steps := (now-t.next)/t.Interval + 1
	t.next += steps * t.Interval
	var s TraceSample
	s.T = now
	for i, pin := range t.pins {
		s.V[i] = t.Chip.Pins.Voltage(pin)
	}
	t.samples[t.head] = s
	t.head = (t.head + 1) % len(t.samples)
	if t.count < len(t.samples) {
		t.count++
	}
}

// ScanSince visits the samples newer than after in chronological order
// without copying the ring, and stops early when fn returns false. Trigger
// scanners use it so an armed capture costs no per-chunk allocation.
func (t *Trace) ScanSince(after uint64, fn func(TraceSample) bool) {
	if t.count == 0 {
		return
	}
	// Find the tail of samples newer than after by walking back from the
	// newest. This is O(new samples) and does not allocate, unlike a full
	// ring copy per emulator chunk.
	n := 0
	for n < t.count && t.sampleAt(t.count-1-n).T > after {
		n++
	}
	for i := t.count - n; i < t.count; i++ {
		if !fn(t.sampleAt(i)) {
			return
		}
	}
}

// ScanAll visits every sample in chronological order. It is used once when a
// trigger is armed before the first sample, so a transition at T=0 is not
// skipped by the newer-than cursor.
func (t *Trace) ScanAll(fn func(TraceSample) bool) {
	for i := range t.count {
		if !fn(t.sampleAt(i)) {
			return
		}
	}
}

func (t *Trace) sampleAt(i int) TraceSample {
	start := (t.head - t.count + len(t.samples)) % len(t.samples)
	return t.samples[(start+i)%len(t.samples)]
}

// Snapshot returns the channels and the ring in chronological order.
func (t *Trace) Snapshot() (pins []int, samples []TraceSample) {
	pins = append([]int(nil), t.pins...)
	if t.count == 0 {
		return pins, nil
	}
	samples = make([]TraceSample, t.count)
	start := (t.head - t.count + len(t.samples)) % len(t.samples)
	first := min(t.count, len(t.samples)-start)
	for i := range first {
		samples[i] = t.samples[start+i]
	}
	for i := first; i < t.count; i++ {
		samples[i] = t.samples[i-first]
	}
	return pins, samples
}
