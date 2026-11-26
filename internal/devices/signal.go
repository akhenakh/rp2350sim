package devices

import (
	"math"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// SignalWave selects a generated waveform.
type SignalWave int

const (
	WaveSine SignalWave = iota
	WaveSquare
	WaveTriangle
	WaveRamp
	WaveNoise
)

// SignalSource drives a pin with an analog waveform so the scope and ADC have
// a signal to measure. Register Tick with Chip.AddPeriodicHook: it only samples
// the clock, and the voltage changes every Interval cycles.
type SignalSource struct {
	Chip      *rp2350.Chip
	Pin       int
	Wave      SignalWave
	Hz        float64
	Amplitude float32
	Offset    float32
	Interval  uint64
	Enabled   bool

	next        uint64
	lastTick    uint64
	initialized bool
	rng         uint64
}

// NewSignalSource creates a generator with a 1 MHz update rate.
func NewSignalSource(chip *rp2350.Chip, pin int, wave SignalWave, hz, amplitude, offset float64) *SignalSource {
	return &SignalSource{
		Chip:      chip,
		Pin:       pin,
		Wave:      wave,
		Hz:        hz,
		Amplitude: float32(amplitude),
		Offset:    float32(offset),
		Interval:  150,
		Enabled:   true,
		next:      chip.Cycles,
		rng:       uint64(pin)*0x9e3779b97f4a7c15 + 1,
	}
}

// WaveName returns the value used in the board YAML.
func (w SignalWave) String() string {
	switch w {
	case WaveSquare:
		return "square"
	case WaveTriangle:
		return "triangle"
	case WaveRamp:
		return "ramp"
	case WaveNoise:
		return "noise"
	default:
		return "sine"
	}
}

// Tick updates the pin's analog voltage. Missed slots produce one update, and
// a virtual-clock rewind restarts the schedule and reapplies the waveform.
func (s *SignalSource) Tick() {
	now := s.Chip.Cycles
	if s.initialized && now < s.lastTick {
		s.next = now
	}
	s.lastTick = now
	s.initialized = true
	if now < s.next {
		return
	}
	if s.Interval == 0 {
		s.Interval = ScopeInterval
	}
	steps := (now-s.next)/s.Interval + 1
	s.next += steps * s.Interval
	if s.Enabled {
		s.apply(now)
	} else {
		s.Chip.Pins.SetAnalog(s.Pin, s.Offset)
	}
}

func (s *SignalSource) apply(now uint64) {
	t := float64(now) / 150e6
	phase := math.Mod(t*s.Hz, 1)
	var wave float32
	switch s.Wave {
	case WaveSquare:
		if phase < 0.5 {
			wave = 1
		} else {
			wave = -1
		}
	case WaveTriangle:
		wave = float32(1 - 4*math.Abs(phase-0.5))
	case WaveRamp:
		wave = float32(2*phase - 1)
	case WaveNoise:
		s.rng ^= s.rng << 13
		s.rng ^= s.rng >> 7
		s.rng ^= s.rng << 17
		wave = float32(float64(s.rng>>40)/float64(1<<24)*2 - 1)
	default:
		wave = float32(math.Sin(2 * math.Pi * phase))
	}
	s.Chip.Pins.SetAnalog(s.Pin, s.Offset+s.Amplitude*wave)
}
