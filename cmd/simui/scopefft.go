package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/cmplx"

	"github.com/go-fft/fft"
	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"

	"github.com/akhenakh/rp2350sim/internal/devices"
)

// scopeSpectrum analyzes the visible scope window with a Hann window and a
// one-sided real FFT. The plan and window are reused while the window length
// stays the same (zoom changes it), so a live spectrum does not allocate per
// frame. fullScale is the scope's volts-per-division range; a full-scale sine
// sits at 0 dB.
type scopeSpectrum struct {
	n      int
	plan   *fft.RealPlan
	window []float64
	gain   float64
	src    []float64
	dst    []complex128
	bins   []float64
}

// compute fills s.bins with dBFS values for bins 0..n/2 and returns the peak
// (above DC) and the bin width in Hz.
func (s *scopeSpectrum) compute(samples []devices.TraceSample, ch int, rate, fullScale float64) (bins []float64, peakHz, peakDB, binHz float64, ok bool) {
	n := len(samples)
	if n < 8 || ch < 0 || ch >= devices.MaxTraceChannels || rate <= 0 || fullScale <= 0 {
		return nil, 0, 0, 0, false
	}
	if s.n != n {
		s.n = n
		s.plan = fft.NewRealPlan(n)
		s.window = fft.Hann(n)
		s.gain = 0
		for _, w := range s.window {
			s.gain += w
		}
		s.gain /= float64(n)
		s.src = make([]float64, n)
		s.dst = make([]complex128, n/2+1)
		s.bins = make([]float64, n/2+1)
	}
	mean := 0.0
	for i := range samples {
		mean += float64(samples[i].V[ch])
	}
	mean /= float64(n)
	weighted := 0.0
	for i := range samples {
		s.src[i] = (float64(samples[i].V[ch]) - mean) * s.window[i]
		weighted += s.src[i]
	}
	// The window itself has a DC gain; a non-integer number of cycles leaves a
	// weighted DC residue that would show as a spurious low-frequency lobe.
	// Remove the window-weighted mean as well.
	if s.gain > 0 {
		weighted /= float64(n) * s.gain
		for i := range s.src {
			s.src[i] -= weighted * s.window[i]
		}
	}
	s.plan.RFFT(s.dst, s.src)

	norm := float64(n) * s.gain
	const floor = -140.0
	peakDB = floor
	peakK := 0
	for k := range s.bins {
		mag := cmplx.Abs(s.dst[k])
		amp := mag / norm
		if k > 0 && k < n/2 {
			amp *= 2 // one-sided spectrum: fold the negative frequency
		}
		db := 20 * math.Log10(math.Max(amp/fullScale, 1e-7))
		if db < floor {
			db = floor
		}
		s.bins[k] = db
		if k > 0 && db > peakDB {
			peakDB = db
			peakK = k
		}
	}
	binHz = rate / float64(n)
	peakHz = float64(peakK) * binHz
	// Parabolic interpolation keeps the readout between bin centres (a 1 kHz
	// tone in a 61 Hz grid should read ~1 kHz, not 977 Hz).
	if peakK > 0 && peakK < len(s.bins)-1 {
		l, c, r := s.bins[peakK-1], s.bins[peakK], s.bins[peakK+1]
		if denom := l - 2*c + r; denom != 0 {
			delta := 0.5 * (l - r) / denom
			delta = math.Max(-0.5, math.Min(0.5, delta))
			peakHz = (float64(peakK) + delta) * binHz
		}
	}
	return s.bins, peakHz, peakDB, binHz, true
}

// scopeSpectrumPanel draws the live FFT of one visible scope channel.
func scopeSpectrumPanel(pins []int, samples []devices.TraceSample, interval uint64, vmax float32) {
	var (
		ch             int
		bins           []float64
		peakHz, peakDB float64
		binHz          float64
		ok             bool
	)
	if len(pins) > 0 && interval > 0 && len(samples) > 0 {
		ch = min(max(scopeFFTChannel, 0), len(pins)-1)
		bins, peakHz, peakDB, binHz, ok = scopeAnalyzer.compute(
			samples, ch, 150e6/float64(interval), float64(vmax))
	}
	Container(Attrs(Grow(1), Expand, Gap(4)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("Spectrum", FontSize(12), FontWeight(700))
			if !ok {
				Label("waiting for samples", FontSize(11), TextColor(220, 6, 62, 1))
				return
			}
			Label(fmt.Sprintf("GP%d · Hann · %.0f Hz/bin", pins[ch], binHz),
				FontSize(11), TextColor(220, 6, 62, 1))
			if scopeFFTLog {
				Label("log", FontSize(11), TextColor(220, 6, 62, 1))
			} else {
				Label("linear", FontSize(11), TextColor(220, 6, 62, 1))
			}
			Filler(1)
			Label(fmt.Sprintf("peak %s · %.1f dBFS", formatFreq(peakHz), peakDB),
				FontSize(11), FontWeight(600), TextColor(20, 60, 80, 1))
		})
		Container(Attrs(Row, Gap(6), Expand, Grow(1)), func() {
			h := int(GetContentHeight())
			if h < 40 {
				h = 150
			}
			Container(Attrs(FixWidth(38), FixHeight(float32(h))), func() {
				Label("0 dB", FontSize(10))
				Filler(1)
				Label("-120", FontSize(10))
			})
			Container(Attrs(Grow(1), Expand, Clip), func() {
				w := GetContentWidth()
				if w < 200 {
					w = 1100
				}
				img := scopeSpectrumImage(bins, int(w), h, -120, binHz, scopeFFTLog, paletteAt(ch))
				id := UseImage("scopefft", img)
				ImageViewAt(id, Vec2{w, float32(h)})
			})
		})
		Container(Attrs(Row, Gap(8), Pad4(0, 0, 0, 44)), func() {
			if !ok {
				return
			}
			if scopeFFTLog {
				Label(formatFreq(binHz), FontSize(10))
			} else {
				Label("0 Hz", FontSize(10))
			}
			Filler(1)
			Label(formatFreq(150e6/float64(interval)/2), FontSize(10))
		})
	})
}

// scopeSpectrumImage draws a filled dB spectrum. minDB is the bottom of the
// vertical scale (the top is 0 dBFS). A logarithmic frequency axis maps binHz
// to the left edge and the Nyquist bin to the right edge; grid lines sit on
// decades so low-frequency peaks remain visible on a 1 MHz scope.
func scopeSpectrumImage(bins []float64, width, height int, minDB, binHz float64, logFreq bool, line color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, 0, 0, width, height, plotBackground)
	if len(bins) < 2 || width <= 0 || height <= 0 || binHz <= 0 {
		return img
	}
	if minDB >= 0 {
		minDB = -120
	}
	fmin := binHz
	fmax := binHz * float64(len(bins)-1)
	if logFreq && fmin > 0 && fmax > fmin {
		for decade := math.Pow(10, math.Ceil(math.Log10(fmin))); decade <= fmax; decade *= 10 {
			x := int(math.Log(decade/fmin) / math.Log(fmax/fmin) * float64(width-1))
			if x > 0 && x < width {
				drawV(img, x, 0, height-1, plotGrid)
			}
		}
	} else {
		for i := 1; i < 10; i++ {
			drawV(img, i*width/10, 0, height-1, plotGrid)
		}
	}
	for i := 1; i < 8; i++ {
		drawH(img, 0, width-1, i*height/8, plotGrid)
	}
	fill := color.RGBA{R: line.R * 45 / 100, G: line.G * 45 / 100, B: line.B * 45 / 100, A: 255}
	lastY := height - 1
	for x := range width {
		var k0, k1 int
		if logFreq && fmax > fmin {
			f0 := fmin * math.Pow(fmax/fmin, float64(x)/float64(width))
			f1 := fmin * math.Pow(fmax/fmin, float64(x+1)/float64(width))
			k0 = int(f0 / binHz)
			k1 = int(math.Ceil(f1/binHz)) + 1
		} else {
			k0 = x * len(bins) / width
			k1 = (x + 1) * len(bins) / width
		}
		k0 = max(1, min(k0, len(bins)-1))
		k1 = max(k0+1, min(k1, len(bins)))
		db := minDB
		for k := k0; k < k1; k++ {
			if bins[k] > db {
				db = bins[k]
			}
		}
		y := height - 1 - int((db-minDB)/(0-minDB)*float64(height-1))
		y = max(0, min(height-1, y))
		if x > 0 {
			// Fill from the curve down so the display is readable at a glance.
			drawV(img, x, y, height-1, fill)
			if lastY >= 0 && lastY <= height-1 {
				y0, y1 := min(y, lastY), max(y, lastY)
				drawV(img, x, y0, y1, line)
			}
		}
		lastY = y
	}
	return img
}
