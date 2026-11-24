package main

import (
	"image/color"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func lcdTestWrite(c *rp2350.Chip, d *devices.ST7789, command bool, data ...byte) {
	const cs = uint64(1 << 9)
	const dc = uint64(1 << 8)
	for _, pin := range []uint32{8, 9} {
		// Clear pad isolation and enable the input, as firmware does.
		c.Write32(0x40038004+4*pin, 0x056)
	}
	c.Pins.OE |= cs | dc
	c.Pins.Out &^= cs
	if command {
		c.Pins.Out &^= dc
	} else {
		c.Pins.Out |= dc
	}
	for _, b := range data {
		d.Transfer(b)
	}
}

func lcdTestCommand(c *rp2350.Chip, d *devices.ST7789, cmd byte, params ...byte) {
	lcdTestWrite(c, d, true, cmd)
	if len(params) != 0 {
		lcdTestWrite(c, d, false, params...)
	}
}

func TestLCDSnapshotVisualGeneration(t *testing.T) {
	c := rp2350.New()
	d := devices.NewST7789(c, 9, 8, 12, 240, 320)
	e := &Emu{chip: c, lcd: d, blPin: -1}

	lcdTestCommand(c, d, 0x11) // SLPOUT
	lcdTestCommand(c, d, 0x36, 0x00)
	lcdTestCommand(c, d, 0x2A, 0, 0, 0, 0)
	lcdTestCommand(c, d, 0x2B, 0, 0, 0, 0)
	lcdTestCommand(c, d, 0x2C)
	lcdTestWrite(c, d, false, 0xF8, 0x00)
	lcdTestCommand(c, d, 0x29) // DISPON

	first := e.Snapshot().LCD
	if first == nil || first.RGBAAt(0, 0) != (color.RGBA{R: 248, A: 255}) {
		t.Fatalf("initial LCD = %v", first)
	}
	if again := e.Snapshot().LCD; again != first {
		t.Fatal("unchanged LCD replaced its cached image")
	}

	lcdTestCommand(c, d, 0x36, 0x08) // BGR only
	bgr := e.Snapshot().LCD
	if bgr == first {
		t.Fatal("BGR-only MADCTL change did not refresh the LCD cache")
	}
	if got := bgr.RGBAAt(0, 0); got != (color.RGBA{B: 248, A: 255}) {
		t.Fatalf("BGR pixel = %v, want blue", got)
	}

	lcdTestCommand(c, d, 0x36, 0x28) // MV|BGR
	rotated := e.Snapshot().LCD
	if rotated == bgr || rotated.Bounds().Dx() != 320 || rotated.Bounds().Dy() != 240 {
		t.Fatalf("rotation did not refresh LCD geometry: %v", rotated.Bounds())
	}

	lcdTestCommand(c, d, 0x10) // SLPIN
	sleeping := e.Snapshot().LCD
	if sleeping == rotated {
		t.Fatal("sleep transition did not refresh the LCD cache")
	}
	if got := sleeping.RGBAAt(0, 0); got != (color.RGBA{A: 255}) {
		t.Fatalf("sleeping pixel = %v, want opaque black", got)
	}

	lcdTestCommand(c, d, 0x11) // SLPOUT
	woken := e.Snapshot().LCD
	if woken == sleeping {
		t.Fatal("wake transition did not refresh the LCD cache")
	}
	if got := woken.RGBAAt(0, 0); got != (color.RGBA{B: 248, A: 255}) {
		t.Fatalf("woken pixel = %v, want blue", got)
	}
}
