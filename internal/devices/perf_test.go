package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// BenchmarkChipIdleAudioAndLED measures an idle chip with the devices that used
// to install a per-cycle hook. The audio window and the WS2812B reset gap are
// calendar events now, so the fast-forward is still available.
func BenchmarkChipIdleAudioAndLED(b *testing.B) {
	c := rp2350.New()
	NewWS2812B(c, 0)
	NewAudio(c, AudioConfig{Kind: AudioPWM, Pin: 1, Rate: 48000})
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	b.ReportAllocs()
	for b.Loop() {
		c.Run(1_000_000)
	}
	if !c.IdleFastForward() {
		b.Fatal("devices forbade the fast-forward")
	}
}

// BenchmarkAudioPWMWindow measures one sample-window callback, the only work
// the PWM decoder does now that pin edges are listener-driven.
func BenchmarkAudioPWMWindow(b *testing.B) {
	c := rp2350.New()
	d := NewAudio(c, AudioConfig{Kind: AudioPWM, Pin: 0, Rate: 48000})
	b.ReportAllocs()
	for b.Loop() {
		c.Cycles += d.pwmWindow
		d.samplePWMWindow()
		d.samples = d.samples[:0]
	}
	if d.pwmWindow == 0 {
		b.Fatal("decoder did not run")
	}
}

// BenchmarkTraceSnapshot measures the per-frame copy the UI does while the
// scope or logic tab is open.
func BenchmarkTraceSnapshot(b *testing.B) {
	c := rp2350.New()
	tr := NewTrace(c, []int{0, 1, 2, 3, 4, 5}, LogicInterval, LogicDepth)
	for range LogicDepth {
		c.Cycles += LogicInterval
		tr.Tick()
	}
	b.ReportAllocs()
	for b.Loop() {
		_, samples := tr.Snapshot()
		if len(samples) == 0 {
			b.Fatal("empty snapshot")
		}
	}
}

// BenchmarkTraceScanSince measures the armed scope-trigger scan when no new
// samples arrived. It must not copy the ring.
func BenchmarkTraceScanSince(b *testing.B) {
	c := rp2350.New()
	tr := NewTrace(c, []int{0, 1, 2, 3, 4, 5}, ScopeInterval, ScopeDepth)
	for range ScopeDepth {
		c.Cycles += ScopeInterval
		tr.Tick()
	}
	after := tr.LatestT()
	visited := 0
	b.ReportAllocs()
	for b.Loop() {
		tr.ScanSince(after, func(TraceSample) bool { visited++; return true })
	}
	if visited != 0 {
		b.Fatalf("scanned %d old samples", visited)
	}
}

// BenchmarkST7789Framebuffer measures the RGB565-to-RGBA conversion the UI
// does whenever the panel content changes.
func BenchmarkST7789Framebuffer(b *testing.B) {
	c := rp2350.New()
	d := NewST7789(c, 9, 8, 12, 240, 320)
	d.minX, d.maxX, d.minY, d.maxY = 0, 239, 0, 319
	d.stride = 240
	for i := range d.gram {
		d.gram[i] = uint16(i*37 + 11)
	}
	d.Brightness = 0.7
	b.ReportAllocs()
	for b.Loop() {
		img := d.Framebuffer()
		if img.Bounds().Dx() != 240 {
			b.Fatal("bad framebuffer")
		}
	}
}
