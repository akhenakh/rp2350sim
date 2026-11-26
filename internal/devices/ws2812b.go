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
// The decoder observes edges through a pin-change listener, so it costs
// nothing on a cycle where the line is steady. The reset gap is a one-shot
// Deadline on the chip's calendar and does not forbid the idle fast-forward.
//
// Timing thresholds follow the WS2812B datasheet at the emulated 150 MHz
// clock: T0H is nominally 0.4 us and T1H 0.8 us (split at 0.6 us), and a low
// period longer than 50 us is a reset.
type WS2812B struct {
	Chip *rp2350.Chip
	Pin  int

	// Logger, when non-nil, records every latched frame.
	Logger *slog.Logger

	deadline    *rp2350.Deadline
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
	w := &WS2812B{Chip: chip, Pin: pin}
	chip.Pins.AddChangeListener(w.onPin)
	w.deadline = chip.AddDeadline(w.latchIfReset)
	return w
}

// onPin records one edge at the virtual cycle the pin model reported it.
func (w *WS2812B) onPin(pin int, level bool) {
	if pin != w.Pin {
		return
	}
	now := w.Chip.Cycles
	if !w.initialized || now < w.lastCycle {
		w.reset(now, level)
		return
	}
	w.lastCycle = now
	if level == w.lastLevel {
		return
	}
	if level {
		// A low gap longer than the reset threshold is a frame boundary even
		// if the deadline has not run yet (a caller can drive the pin model by
		// hand). The deadline at lowStart+reset+1 normally handles this.
		if now-w.lowStart > ws2812ResetCycles {
			w.latch()
		}
		w.riseCycle = now
		w.deadline.Cancel()
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
		// One cycle after the 50 us threshold, so a rising edge exactly at
		// 50 us still belongs to the frame and cancels the latch.
		w.deadline.Schedule(now + ws2812ResetCycles + 1)
	}
	w.lastLevel = level
}

func (w *WS2812B) reset(now uint64, level bool) {
	w.bits = w.bits[:0]
	w.lastLevel = level
	w.riseCycle = now
	w.lowStart = now
	w.lastCycle = now
	w.initialized = true
	w.deadline.Cancel()
}

// latchIfReset is the deadline callback: the line has been low long enough to
// end the frame. A chip reset rewinds the clock, so a stale partial frame is
// dropped instead of being latched against the old timeline.
func (w *WS2812B) latchIfReset() {
	now := w.Chip.Cycles
	if now < w.lastCycle {
		w.reset(now, w.Chip.Pins.Level(w.Pin))
		return
	}
	if w.Chip.Pins.Level(w.Pin) {
		return
	}
	if len(w.bits) > 0 && now-w.lowStart >= ws2812ResetCycles {
		w.latch()
	}
}

// Tick applies the reset-gap check at the current cycle. NewWS2812B schedules
// this as a deadline; the method is exposed for tests and for callers that
// drive the pin model without the chip clock.
func (w *WS2812B) Tick() { w.latchIfReset() }

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
