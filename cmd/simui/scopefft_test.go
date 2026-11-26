package main

import (
	"math"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
)

func TestScopeSpectrumFindsSine(t *testing.T) {
	const (
		n    = 1024
		rate = 1e6
		k    = 37
	)
	freq := k * rate / n
	samples := make([]devices.TraceSample, n)
	for i := range samples {
		samples[i].V[0] = float32(math.Sin(2 * math.Pi * freq * float64(i) / rate))
	}
	var s scopeSpectrum
	bins, peakHz, peakDB, binHz, ok := s.compute(samples, 0, rate, 3.3)
	if !ok {
		t.Fatal("compute refused a valid window")
	}
	if len(bins) != n/2+1 {
		t.Fatalf("bins = %d, want %d", len(bins), n/2+1)
	}
	if math.Abs(binHz-rate/n) > 1e-9 {
		t.Fatalf("bin width = %v, want %v", binHz, rate/n)
	}
	if math.Abs(peakHz-freq) > binHz/2 {
		t.Fatalf("peak = %.1f Hz, want %.1f Hz", peakHz, freq)
	}
	wantDB := 20 * math.Log10(1.0/3.3)
	if math.Abs(peakDB-wantDB) > 0.5 {
		t.Fatalf("peak = %.2f dBFS, want %.2f", peakDB, wantDB)
	}
	// Reusing the analyzer for a different length must rebuild the plan.
	samples = samples[:512]
	if _, _, _, _, ok := s.compute(samples, 0, rate, 3.3); !ok || len(s.bins) != 257 {
		t.Fatalf("resized analyzer bins = %d, ok=%v", len(s.bins), ok)
	}
}

func TestScopeSpectrumImage(t *testing.T) {
	bins := make([]float64, 257)
	bins[10] = -3
	line := paletteAt(0)
	img := scopeSpectrumImage(bins, 320, 120, -120, 1e6/512, true, line)
	if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 120 {
		t.Fatalf("image bounds = %v", img.Bounds())
	}
	found := false
	for y := range 120 {
		for x := range 320 {
			if img.RGBAAt(x, y) == line {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("spectrum image has no spectrum line")
	}
}
