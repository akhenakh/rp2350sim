package main

import (
	"image"
	"image/color"

	"github.com/akhenakh/rp2350sim/internal/devices"
)

// tracePalette assigns one colour per scope/logic channel.
var tracePalette = []color.RGBA{
	{R: 0x3e, G: 0xff, B: 0x88, A: 0xff}, // green
	{R: 0x4f, G: 0xd6, B: 0xff, A: 0xff}, // cyan
	{R: 0xff, G: 0xd7, B: 0x4f, A: 0xff}, // yellow
	{R: 0xff, G: 0x6f, B: 0xd0, A: 0xff}, // magenta
	{R: 0xff, G: 0x9f, B: 0x43, A: 0xff}, // orange
	{R: 0x8f, G: 0xa8, B: 0xff, A: 0xff}, // blue
	{R: 0xff, G: 0x5f, B: 0x6f, A: 0xff}, // red
	{R: 0xe8, G: 0xe8, B: 0xe8, A: 0xff}, // white
}

var (
	plotBackground = color.RGBA{R: 0x0d, G: 0x11, B: 0x18, A: 0xff}
	plotGrid       = color.RGBA{R: 0x1d, G: 0x26, B: 0x33, A: 0xff}
	triggerColor   = color.RGBA{R: 0xff, G: 0x9f, B: 0x43, A: 0xff}
)

func paletteAt(i int) color.RGBA { return tracePalette[i%len(tracePalette)] }

// scopeImage renders voltage-versus-time traces. Each pixel column spans the
// min/max voltage of its sample range, so square waves stay solid and analog
// waveforms keep their shape without aliasing.
func scopeImage(pins []int, samples []devices.TraceSample, width, height int, vmax float32, marker int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, 0, 0, width, height, plotBackground)
	for i := 1; i < 10; i++ {
		drawV(img, i*width/10, 0, height-1, plotGrid)
	}
	for i := 1; i < 8; i++ {
		drawH(img, 0, width-1, i*height/8, plotGrid)
	}
	n := len(samples)
	if n < 2 || len(pins) == 0 {
		return img
	}
	for x := range width {
		i0 := x * n / width
		i1 := (x + 1) * n / width
		if i1 <= i0 {
			i1 = i0 + 1
		}
		if i1 > n {
			i1 = n
		}
		for ch := 0; ch < len(pins) && ch < devices.MaxTraceChannels; ch++ {
			lo, hi := samples[i0].V[ch], samples[i0].V[ch]
			for i := i0 + 1; i < i1; i++ {
				v := samples[i].V[ch]
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			ytop := height - 1 - int(float64(clampV(hi, vmax))/float64(vmax)*float64(height-1))
			ybot := height - 1 - int(float64(clampV(lo, vmax))/float64(vmax)*float64(height-1))
			drawV(img, x, ytop, ybot, paletteAt(ch))
		}
	}
	if marker >= 0 && marker < n {
		x := marker * (width - 1) / max(n-1, 1)
		for y := 0; y < height; y += 8 {
			drawV(img, x, y, min(y+4, height-1), triggerColor)
		}
	}
	return img
}

// logicLine is one digital signal drawn in a logic-analyser lane.
type logicLine struct {
	Pin   int
	Label string
	Color color.RGBA
}

// logicImage renders digital lanes for the given signals.
func logicImage(pins []int, samples []devices.TraceSample, lines []logicLine, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, 0, 0, width, height, plotBackground)
	n := len(samples)
	if n < 2 || len(lines) == 0 {
		return img
	}
	lane := height / len(lines)
	for li, line := range lines {
		ch := channelOf(pins, line.Pin)
		if ch < 0 {
			continue
		}
		top := li*lane + 4
		bottom := (li+1)*lane - 4
		if bottom <= top {
			bottom = top + 1
		}
		drawH(img, 0, width-1, li*lane, plotGrid)
		prevHigh := samples[0].Bit(ch)
		for x := range width {
			i := x * n / width
			high := samples[i].Bit(ch)
			if high != prevHigh {
				drawV(img, x, top, bottom, line.Color)
				prevHigh = high
			}
			y := bottom
			if high {
				y = top
			}
			drawH(img, x, x+1, y, line.Color)
		}
	}
	return img
}

// channelOf maps a pin to its trace channel, or -1.
func channelOf(pins []int, pin int) int {
	for i, p := range pins {
		if p == pin {
			return i
		}
	}
	return -1
}

func clampV(v, vmax float32) float32 {
	if v < 0 {
		return 0
	}
	if v > vmax {
		return vmax
	}
	return v
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func drawV(img *image.RGBA, x, y0, y1 int, c color.RGBA) {
	if x < 0 || x >= img.Bounds().Dx() {
		return
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		if y >= 0 && y < img.Bounds().Dy() {
			img.SetRGBA(x, y, c)
		}
	}
}

func drawH(img *image.RGBA, x0, x1, y int, c color.RGBA) {
	if y < 0 || y >= img.Bounds().Dy() {
		return
	}
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	for x := x0; x <= x1; x++ {
		if x >= 0 && x < img.Bounds().Dx() {
			img.SetRGBA(x, y, c)
		}
	}
}
