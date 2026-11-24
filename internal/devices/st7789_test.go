package devices

import (
	"image/color"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func newTestDisplay() *ST7789 {
	d := NewST7789(rp2350.New(), 9, 8, 12, 240, 320)
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
	for i := range 7 { // 3 more red pixels + 4 green pixels
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
	// MADCTL MV swaps the 240x320 panel to 320x240.
	if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 240 {
		t.Fatalf("size = %v, want 320x240", img.Bounds().Size())
	}
	want := []struct{ r, g, b uint8 }{
		{248, 0, 0}, {248, 0, 0}, {248, 0, 0}, {248, 0, 0},
		{0, 252, 0}, {0, 252, 0}, {0, 252, 0}, {0, 252, 0},
	}
	for y := range 2 {
		for x := range 4 {
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

func TestST7789GEEKFullGRAM(t *testing.T) {
	c := rp2350.New()
	d := NewST7789(c, 9, 8, 12, 135, 240)
	d.sendCmd(0x11)                 // SLPOUT
	d.sendCmd(0x36, 0x70)           // MADCTL MX|MY|MV
	d.sendCmd(0x2A, 0, 40, 1, 0x17) // CASET 40..279
	d.sendCmd(0x2B, 0, 53, 0, 0xBB) // RASET 53..187
	d.sendCmd(0x2C)                 // RAMWR
	for y := 53; y <= 187; y++ {
		for x := 40; x <= 279; x++ {
			var px uint16
			switch {
			case x == 40 && y == 53:
				px = 0xF800 // red at the visible top-left
			case x == 279 && y == 187:
				px = 0x07E0 // green at the visible bottom-right
			case x == 159 && y == 120:
				px = 0x001F // blue at the visible centre
			}
			d.data(byte(px >> 8))
			d.data(byte(px))
		}
	}
	d.sendCmd(0x29) // DISPON

	if got := d.PixelsWritten(); got != 240*135 {
		t.Fatalf("pixels = %d, want %d", got, 240*135)
	}
	img := d.Framebuffer()
	if got := img.Bounds().Size(); got.X != 240 || got.Y != 135 {
		t.Fatalf("size = %v, want 240x135", got)
	}
	for _, want := range []struct {
		x, y int
		c    color.RGBA
	}{
		{0, 0, color.RGBA{R: 248, A: 255}},
		{239, 134, color.RGBA{G: 252, A: 255}},
		{119, 67, color.RGBA{B: 248, A: 255}},
	} {
		if got := img.RGBAAt(want.x, want.y); got != want.c {
			t.Errorf("pixel (%d,%d) = %v, want %v", want.x, want.y, got, want.c)
		}
	}
}

func TestST7789GEEKLogicalWindow(t *testing.T) {
	c := rp2350.New()
	d := NewST7789(c, 9, 8, 12, 135, 240)
	d.sendCmd(0x11)               // SLPOUT
	d.sendCmd(0x36, 0x70)         // MADCTL MX|MY|MV
	d.sendCmd(0x2A, 0, 0, 0, 239) // TinyGo logical CASET 0..239
	d.sendCmd(0x2B, 0, 0, 0, 134) // TinyGo logical RASET 0..134
	d.sendCmd(0x2C)
	for y := range 135 {
		for x := range 240 {
			var px uint16
			switch {
			case x == 0 && y == 0:
				px = 0xF800
			case x == 239 && y == 134:
				px = 0x07E0
			case x == 119 && y == 67:
				px = 0x001F
			}
			d.data(byte(px >> 8))
			d.data(byte(px))
		}
	}
	d.sendCmd(0x29)

	img := d.Framebuffer()
	if got := d.PixelsWritten(); got != 240*135 {
		t.Fatalf("pixels = %d, want %d", got, 240*135)
	}
	for _, want := range []struct {
		x, y int
		c    color.RGBA
	}{
		{0, 0, color.RGBA{R: 248, A: 255}},
		{239, 134, color.RGBA{G: 252, A: 255}},
		{119, 67, color.RGBA{B: 248, A: 255}},
	} {
		if got := img.RGBAAt(want.x, want.y); got != want.c {
			t.Errorf("logical pixel (%d,%d) = %v, want %v", want.x, want.y, got, want.c)
		}
	}

	// A partial update must not move the exported full-frame origin.
	d.sendCmd(0x2A, 0, 10, 0, 10)
	d.sendCmd(0x2B, 0, 20, 0, 20)
	d.sendCmd(0x2C)
	d.data(0xFF)
	d.data(0xE0)
	img = d.Framebuffer()
	if got := img.RGBAAt(0, 0); got != (color.RGBA{R: 248, A: 255}) {
		t.Fatalf("partial update moved logical origin: %v", got)
	}
	if got := img.RGBAAt(10, 20); got != (color.RGBA{R: 248, G: 252, A: 255}) {
		t.Fatalf("partial pixel = %v, want yellow", got)
	}
}

func TestST7789SleepRetainsGRAM(t *testing.T) {
	d := newTestDisplay()
	d.sendCmd(0x11)             // SLPOUT
	d.sendCmd(0x2A, 0, 0, 0, 0) // CASET 0..0
	d.sendCmd(0x2B, 0, 0, 0, 0) // RASET 0..0
	d.sendCmd(0x2C)             // RAMWR
	d.data(0xF8)
	d.data(0x00)
	d.sendCmd(0x29) // DISPON

	colored := d.Framebuffer().RGBAAt(0, 0)
	if colored.R != 248 || colored.G != 0 || colored.B != 0 {
		t.Fatalf("awake pixel = %v, want red", colored)
	}
	pixels := d.PixelsWritten()
	d.sendCmd(0x10) // SLPIN
	if got := d.Framebuffer().RGBAAt(0, 0); got.R != 0 || got.G != 0 || got.B != 0 {
		t.Fatalf("sleeping pixel = %v, want black", got)
	}
	if got := d.PixelsWritten(); got != pixels {
		t.Fatalf("sleep changed pixel count from %d to %d", pixels, got)
	}
	d.sendCmd(0x11) // SLPOUT
	if got := d.Framebuffer().RGBAAt(0, 0); got != colored {
		t.Fatalf("woken pixel = %v, want %v", got, colored)
	}
}
