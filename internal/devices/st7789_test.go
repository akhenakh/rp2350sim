package devices

import (
	"testing"

	"github.com/akh/rp2350sim/internal/rp2350"
)

func newTestDisplay() *ST7789 {
	d := NewST7789(rp2350.New(), 9, 8, 12)
	return d
}

func (d *ST7789) sendCmd(c byte, params ...byte) {
	d.command(c)
	for _, p := range params {
		d.data(p)
	}
}

func TestST7789Framebuffer(t *testing.T) {
	d := newTestDisplay()
	d.sendCmd(0x11)             // SLPOUT
	d.sendCmd(0x3A, 0x55)       // COLMOD RGB565
	d.sendCmd(0x36, 0x60)       // MADCTL MX|MV
	d.sendCmd(0x21)             // INVON: must not affect the exported image
	d.sendCmd(0x2A, 0, 0, 0, 3) // CASET 0..3
	d.sendCmd(0x2B, 0, 0, 0, 1) // RASET 0..1
	d.sendCmd(0x2C)             // RAMWR
	d.data(0xF8)                // red  0xF800
	d.data(0x00)
	for i := 0; i < 7; i++ { // 3 more red pixels + 4 green pixels
		if i < 3 {
			d.data(0xF8)
			d.data(0x00)
		} else {
			d.data(0x07)
			d.data(0xE0)
		}
	}
	d.sendCmd(0x29) // DISPON

	if !d.On() {
		t.Fatal("display did not turn on")
	}
	if got := d.PixelsWritten(); got != 8 {
		t.Fatalf("pixels = %d, want 8", got)
	}
	img := d.Framebuffer()
	if img.Bounds().Dx() != 4 || img.Bounds().Dy() != 2 {
		t.Fatalf("size = %v, want 4x2", img.Bounds().Size())
	}
	want := []struct{ r, g, b uint8 }{
		{248, 0, 0}, {248, 0, 0}, {248, 0, 0}, {248, 0, 0},
		{0, 252, 0}, {0, 252, 0}, {0, 252, 0}, {0, 252, 0},
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			c := img.RGBAAt(x, y)
			w := want[y*4+x]
			if c.R != w.r || c.G != w.g || c.B != w.b {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, c, w)
			}
		}
	}
}

func TestST7789Reset(t *testing.T) {
	d := newTestDisplay()
	d.sendCmd(0x29)
	if !d.On() {
		t.Fatal("expected display on")
	}
	d.sendCmd(0x01) // SWRESET
	if d.On() {
		t.Fatal("SWRESET left the display on")
	}
}
