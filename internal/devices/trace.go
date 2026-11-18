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
// It is a CycleHook; Snapshot must be called with the chip (and therefore the
// trace) locked by the owner.
type Trace struct {
	Chip     *rp2350.Chip
	Interval uint64

	pins    []int
	samples []TraceSample
	head    int
	count   int
	next    uint64
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
}

// Pins returns the channel-to-pin mapping.
func (t *Trace) Pins() []int { return t.pins }

// Tick samples every Interval cycles.
func (t *Trace) Tick() {
	now := t.Chip.Cycles
	if now < t.next {
		return
	}
	// Keep the phase when a long step overshoots so the effective sample
	// rate does not drift.
	t.next += t.Interval
	if now > t.next+t.Interval*8 {
		t.next = now + t.Interval
	}
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

// Snapshot returns the channels and the ring in chronological order.
func (t *Trace) Snapshot() (pins []int, samples []TraceSample) {
	pins = append([]int(nil), t.pins...)
	if t.count == 0 {
		return pins, nil
	}
	samples = make([]TraceSample, t.count)
	start := (t.head - t.count + len(t.samples)) % len(t.samples)
	for i := 0; i < t.count; i++ {
		samples[i] = t.samples[(start+i)%len(t.samples)]
	}
	return pins, samples
}
