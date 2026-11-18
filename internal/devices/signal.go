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
)

// SignalSource drives a pin with an analog waveform so the scope and ADC have
// a signal to measure. It is a CycleHook and only samples the clock; the
// voltage is updated every Interval cycles.
type SignalSource struct {
	Chip      *rp2350.Chip
	Pin       int
	Wave      SignalWave
	Hz        float64
	Amplitude float32
	Offset    float32
	Interval  uint64

	next uint64
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
		next:      chip.Cycles,
	}
}

// WaveName returns the value used in the board YAML.
func (w SignalWave) String() string {
	switch w {
	case WaveSquare:
		return "square"
	case WaveTriangle:
		return "triangle"
	default:
		return "sine"
	}
}

// Tick updates the pin's analog voltage.
func (s *SignalSource) Tick() {
	now := s.Chip.Cycles
	if now < s.next {
		return
	}
	s.next += s.Interval
	if now > s.next+s.Interval*8 {
		s.next = now + s.Interval
	}
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
	default:
		wave = float32(math.Sin(2 * math.Pi * phase))
	}
	s.Chip.Pins.SetAnalog(s.Pin, s.Offset+s.Amplitude*wave)
}
