package devices

import (
	"fmt"
	"image/color"
	"log/slog"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// WS2812B decodes the WS2812B single-wire protocol on a GPIO pin. The line is
// normally driven by a PIO state machine, but any source works: each bit is a
// high pulse whose width selects 0 or 1, and a long low period latches a frame
// of 24-bit GRB values (MSB first).
//
// Timing thresholds follow the WS2812B datasheet at the emulated 150 MHz
// clock: T0H is nominally 0.4 us and T1H 0.8 us (split at 0.6 us), and a low
// period longer than 50 us is a reset.
type WS2812B struct {
	Chip *rp2350.Chip
	Pin  int

	// Logger, when non-nil, records every latched frame.
	Logger *slog.Logger

	lastLevel   bool
	riseCycle   uint64
	lowStart    uint64
	lastCycle   uint64
	initialized bool
	bits        []byte
	colors      []color.RGBA
	frames      uint64
}

const (
	ws2812BitCycles   = 90   // 0.6 us at 150 MHz: longer high pulse is a 1
	ws2812ResetCycles = 7500 // 50 us low period latches the frame
)

// NewWS2812B creates a monitor for the strip wired to pin.
func NewWS2812B(chip *rp2350.Chip, pin int) *WS2812B {
	return &WS2812B{Chip: chip, Pin: pin}
}

// Tick samples the pin; register it with Chip.CycleHooks.
func (w *WS2812B) Tick() {
	level := w.Chip.Pins.Level(w.Pin)
	now := w.Chip.Cycles
	if !w.initialized || now < w.lastCycle {
		w.bits = w.bits[:0]
		w.lastLevel = level
		w.riseCycle = now
		w.lowStart = now
		w.lastCycle = now
		w.initialized = true
		return
	}
	w.lastCycle = now
	if level != w.lastLevel {
		if level {
			w.riseCycle = now
		} else {
			width := now - w.riseCycle
			bit := byte(0)
			if width > ws2812BitCycles {
				bit = 1
			}
			w.bits = append(w.bits, bit)
			w.lowStart = now
			if len(w.bits) > 24*1024 {
				w.bits = w.bits[:0]
			}
		}
		w.lastLevel = level
		return
	}
	if !level && len(w.bits) > 0 && now-w.lowStart > ws2812ResetCycles {
		w.latch()
	}
}

func (w *WS2812B) latch() {
	n := len(w.bits) / 24
	if n == 0 {
		w.bits = w.bits[:0]
		return
	}
	colors := make([]color.RGBA, 0, n)
	for i := range n {
		var g, r, b uint8
		base := i * 24
		for k := range 8 {
			g = g<<1 | w.bits[base+k]
		}
		for k := range 8 {
			r = r<<1 | w.bits[base+8+k]
		}
		for k := range 8 {
			b = b<<1 | w.bits[base+16+k]
		}
		colors = append(colors, color.RGBA{R: r, G: g, B: b, A: 255})
	}
	w.colors = colors
	w.frames++
	w.bits = w.bits[:0]
	desc := make([]string, len(colors))
	for i, c := range colors {
		desc[i] = fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	loggerOr(w.Logger).Info("ws2812 frame",
		slog.Uint64("frame", w.frames), slog.Int("leds", len(colors)), slog.String("colors", strings.Join(desc, " ")))
}

// Colors returns the most recently latched frame.
func (w *WS2812B) Colors() []color.RGBA { return w.colors }

// Frames reports how many frames have been latched.
func (w *WS2812B) Frames() uint64 { return w.frames }
