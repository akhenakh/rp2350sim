package devices

import (
	"encoding/binary"
	"errors"
	"os"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// AudioKind is the wire format an AudioDecoder follows.
type AudioKind int

const (
	AudioI2S AudioKind = iota
	AudioPDM
	AudioPWM
)

// AudioConfig describes the pins and the WAV output format.
type AudioConfig struct {
	Kind AudioKind
	// I2S pins.
	BCLK, LRCLK, Data int
	// PDM: Data and Clock; PWM/GPIO: Pin.
	Clock, Pin int
	// I2S slot width in bits (16, 24 or 32). PDM ignores it.
	Bits int
	// I2S data delay in BCLK edges after WS (1 for Philips I2S, 0 for
	// left-justified).
	Delay int
	// PDM decimation (ones per sample).
	Decimation int
	// Output sample rate for the WAV file. Defaults to 48000.
	Rate int
	// Channels defaults to 2 for I2S and 1 otherwise.
	Channels int
}

// AudioDecoder samples audio pins on every emulated cycle and accumulates
// 16-bit PCM samples. Register Tick as a Chip.CycleHook.
type AudioDecoder struct {
	chip *rp2350.Chip
	cfg  AudioConfig

	samples []int16
	limit   int

	lastBCLK bool
	lastWS   bool
	skip     int
	shift    uint32
	bits     int
	left     int16
	right    int16
	haveL    bool
	haveR    bool

	pdmBits  int
	pdmOnes  int
	pdmClock bool

	pwmHigh   uint64
	pwmCycles uint64
	pwmWindow uint64
}

// NewAudio creates a decoder. cfg.Rate/Channels/Bits/Decimation are filled in
// with defaults when zero.
func NewAudio(chip *rp2350.Chip, cfg AudioConfig) *AudioDecoder {
	if cfg.Rate <= 0 {
		cfg.Rate = 48000
	}
	if cfg.Bits <= 0 {
		cfg.Bits = 16
	}
	if cfg.Decimation <= 0 {
		cfg.Decimation = 64
	}
	if cfg.Channels <= 0 {
		if cfg.Kind == AudioI2S {
			cfg.Channels = 2
		} else {
			cfg.Channels = 1
		}
	}
	d := &AudioDecoder{chip: chip, cfg: cfg}
	// Ten minutes is plenty for a test capture and bounds memory.
	d.limit = cfg.Rate * cfg.Channels * 60 * 10
	d.pwmWindow = uint64(rp2350.SysClkHz) / uint64(cfg.Rate)
	if d.pwmWindow == 0 {
		d.pwmWindow = 1
	}
	return d
}

// Config returns the effective configuration.
func (d *AudioDecoder) Config() AudioConfig { return d.cfg }

// Tick samples the configured pins. It must run every cycle.
func (d *AudioDecoder) Tick() {
	if len(d.samples) >= d.limit {
		return
	}
	switch d.cfg.Kind {
	case AudioI2S:
		d.tickI2S()
	case AudioPDM:
		d.tickPDM()
	case AudioPWM:
		d.tickPWM()
	}
}

func (d *AudioDecoder) tickI2S() {
	bclk := d.chip.Pins.Level(d.cfg.BCLK)
	rise := bclk && !d.lastBCLK
	d.lastBCLK = bclk
	if !rise {
		return
	}
	ws := d.chip.Pins.Level(d.cfg.LRCLK)
	if ws != d.lastWS {
		d.lastWS = ws
		d.skip = d.cfg.Delay
		d.bits = 0
		d.shift = 0
	}
	if d.skip > 0 {
		d.skip--
		return
	}
	bit := uint32(0)
	if d.chip.Pins.Level(d.cfg.Data) {
		bit = 1
	}
	d.shift = d.shift<<1 | bit
	d.bits++
	if d.bits < d.cfg.Bits {
		return
	}
	sample := sampleFromBits(d.shift, d.cfg.Bits)
	d.bits = 0
	d.shift = 0
	if d.cfg.Channels == 1 {
		d.append(sample)
		return
	}
	if ws {
		d.right = sample
		d.haveR = true
	} else {
		d.left = sample
		d.haveL = true
	}
	if d.haveL && d.haveR {
		d.append(d.left)
		d.append(d.right)
		d.haveL, d.haveR = false, false
	}
}

func (d *AudioDecoder) tickPDM() {
	clock := d.chip.Pins.Level(d.cfg.Clock)
	rise := clock && !d.pdmClock
	d.pdmClock = clock
	if !rise {
		return
	}
	if d.chip.Pins.Level(d.cfg.Data) {
		d.pdmOnes++
	}
	d.pdmBits++
	if d.pdmBits < d.cfg.Decimation {
		return
	}
	// Average density over the decimation window, then map 0..1 to -1..1.
	density := float32(d.pdmOnes) / float32(d.pdmBits)
	d.append(int16((density*2 - 1) * 32767))
	d.pdmBits = 0
	d.pdmOnes = 0
}

func (d *AudioDecoder) tickPWM() {
	if d.chip.PinIsPWM(d.cfg.Pin) {
		d.pwmCycles++
		if d.pwmCycles < d.pwmWindow {
			return
		}
		duty := d.chip.PinBrightness(d.cfg.Pin)
		d.append(int16((duty*2 - 1) * 32767))
		d.pwmCycles = 0
		return
	}
	if d.chip.Pins.Level(d.cfg.Pin) {
		d.pwmHigh++
	}
	d.pwmCycles++
	if d.pwmCycles < d.pwmWindow {
		return
	}
	duty := float32(d.pwmHigh) / float32(d.pwmCycles)
	d.append(int16((duty*2 - 1) * 32767))
	d.pwmHigh = 0
	d.pwmCycles = 0
}

func (d *AudioDecoder) append(v int16) {
	if len(d.samples) < d.limit {
		d.samples = append(d.samples, v)
	}
}

func sampleFromBits(v uint32, bits int) int16 {
	if bits <= 0 || bits > 32 {
		return 0
	}
	// Sign-extend the slot, then left-justify it in 16 bits.
	signed := int32(v<<uint(32-bits)) >> uint(32-bits)
	if bits >= 16 {
		return int16(signed >> uint(bits-16))
	}
	return int16(signed << uint(16-bits))
}

// Samples returns a copy of the PCM buffer.
func (d *AudioDecoder) Samples() []int16 {
	out := make([]int16, len(d.samples))
	copy(out, d.samples)
	return out
}

// Len reports the number of samples captured.
func (d *AudioDecoder) Len() int { return len(d.samples) }

// WAV packs the samples as a mono/stereo 16-bit PCM WAV file.
func (d *AudioDecoder) WAV() []byte {
	const header = 44
	dataLen := len(d.samples) * 2
	out := make([]byte, header+dataLen)
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+dataLen))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], uint16(d.cfg.Channels))
	binary.LittleEndian.PutUint32(out[24:], uint32(d.cfg.Rate))
	binary.LittleEndian.PutUint32(out[28:], uint32(d.cfg.Rate*d.cfg.Channels*2))
	binary.LittleEndian.PutUint16(out[32:], uint16(d.cfg.Channels*2)) // block align
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(dataLen))
	for i, v := range d.samples {
		binary.LittleEndian.PutUint16(out[header+i*2:], uint16(v))
	}
	return out
}

// WriteWAV writes the capture to path.
func (d *AudioDecoder) WriteWAV(path string) error {
	if path == "" {
		return errors.New("empty WAV path")
	}
	return os.WriteFile(path, d.WAV(), 0o644)
}
