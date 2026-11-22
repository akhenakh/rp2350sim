package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// BenchmarkAudioPWM measures the per-cycle cost of the audio hook in the
// common PWM/GPIO mode, which runs on every emulated cycle.
func BenchmarkAudioPWM(b *testing.B) {
	c := rp2350.New()
	d := NewAudio(c, AudioConfig{Kind: AudioPWM, Pin: 0, Rate: 48000})
	b.ReportAllocs()
	for b.Loop() {
		d.Tick()
	}
	if d.Len() == 0 && d.pwmCycles == 0 {
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
