// Package devices implements external chips attached to the RP2350 buses.
package devices

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/akh/rp2350sim/internal/rp2350"
)

// ST7789 models an ST7789-family SPI TFT controller. It decodes the command
// stream produced by TinyGo's st7789 driver and keeps a framebuffer.
type ST7789 struct {
	Chip *rp2350.Chip

	// Wiring: chip-select, data/command and reset pins.
	CS, DC, RST int

	Width, Height int

	gram   []uint16 // controller GRAM, Width x Height in logical order
	casetX [2]int
	casetY [2]int
	x, y   int
	cmd    byte
	params []byte
	inData bool
	// pending high byte for RGB565 input
	hi       byte
	haveHi   bool
	madctl   byte
	inverted bool
	on       bool
	pixels   int

	// Debug prints command and data bytes to stderr.
	Debug bool
	logN  int
}

// NewST7789 creates a display attached to the chip, wired to the given pins.
func NewST7789(chip *rp2350.Chip, cs, dc, rst int) *ST7789 {
	d := &ST7789{Chip: chip, CS: cs, DC: dc, RST: rst, Width: 240, Height: 320}
	d.gram = make([]uint16, d.Width*d.Height)
	d.casetX = [2]int{0, d.Width - 1}
	d.casetY = [2]int{0, d.Height - 1}
	return d
}

// Transfer feeds one byte clocked over SPI and returns the MISO byte.
func (d *ST7789) Transfer(tx byte) byte {
	if d.Chip.Pins.Level(d.RST) == false {
		d.reset()
		return 0x00
	}
	if d.Chip.Pins.Level(d.CS) {
		return 0x00
	}
	if d.Debug && d.logN < 200 {
		fmt.Fprintf(os.Stderr, "lcd cs=%v dc=%v rst=%v tx=%02x\n", d.Chip.Pins.Level(d.CS), d.Chip.Pins.Level(d.DC), d.Chip.Pins.Level(d.RST), tx)
		d.logN++
	}
	if d.Chip.Pins.Level(d.DC) {
		d.data(tx)
	} else {
		d.command(tx)
	}
	return 0x00
}

func (d *ST7789) reset() {
	d.cmd = 0
	d.params = d.params[:0]
	d.inData = false
	d.haveHi = false
	d.madctl = 0
	d.inverted = false
	d.on = false
	for i := range d.gram {
		d.gram[i] = 0
	}
	d.casetX = [2]int{0, d.Width - 1}
	d.casetY = [2]int{0, d.Height - 1}
}

func (d *ST7789) command(c byte) {
	d.cmd = c
	d.params = d.params[:0]
	d.haveHi = false
	switch c {
	case 0x2C: // RAMWR
		d.inData = true
		d.x = d.casetX[0]
		d.y = d.casetY[0]
	case 0x01: // SWRESET
		d.reset()
	}
}

func (d *ST7789) data(b byte) {
	if d.inData && d.cmd == 0x2C {
		if !d.haveHi {
			d.hi = b
			d.haveHi = true
			return
		}
		px := uint16(d.hi)<<8 | uint16(b)
		d.haveHi = false
		if d.Debug && (d.pixels < 6 || d.pixels%20000 == 0) {
			fmt.Fprintf(os.Stderr, "lcd pixel %#04x at %d,%d\n", px, d.x, d.y)
		}
		if d.x >= 0 && d.x < d.Width && d.y >= 0 && d.y < d.Height {
			d.gram[d.y*d.Width+d.x] = px
			d.pixels++
		}
		d.x++
		if d.x > d.casetX[1] {
			d.x = d.casetX[0]
			d.y++
			if d.y > d.casetY[1] {
				d.y = d.casetY[0]
			}
		}
		return
	}
	d.params = append(d.params, b)
	switch d.cmd {
	case 0x2A: // CASET
		if len(d.params) == 4 {
			d.casetX[0] = int(d.params[0])<<8 | int(d.params[1])
			d.casetX[1] = int(d.params[2])<<8 | int(d.params[3])
		}
	case 0x2B: // RASET
		if len(d.params) == 4 {
			d.casetY[0] = int(d.params[0])<<8 | int(d.params[1])
			d.casetY[1] = int(d.params[2])<<8 | int(d.params[3])
		}
	case 0x36: // MADCTL
		d.madctl = b
	case 0x21:
		d.inverted = true
	case 0x20:
		d.inverted = false
	case 0x28:
		d.on = false
	case 0x29:
		d.on = true
	}
}

// PixelsWritten reports how many pixels were stored since creation.
func (d *ST7789) PixelsWritten() int { return d.pixels }

// On reports whether the display is enabled.
func (d *ST7789) On() bool { return d.on }

// Framebuffer renders the display contents as RGBA, honouring the MADCTL
// mirror bits and RGB/BGR order.
func (d *ST7789) Framebuffer() *image.RGBA {
	w, h := d.Width, d.Height
	if d.madctl&0x20 == 0 { // MV: row/column exchange
		w, h = h, w
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < d.Height; y++ {
		for x := 0; x < d.Width; x++ {
			px := d.gram[y*d.Width+x]
			r := uint8(px>>11) << 3
			g := uint8(px>>5) << 2
			b := uint8(px) << 3
			if d.madctl&0x08 != 0 { // BGR
				r, b = b, r
			}
			if d.inverted {
				r, g, b = ^r, ^g, ^b
			}
			sx, sy := x, y
			if d.madctl&0x40 != 0 { // MX
				sx = d.Width - 1 - x
			}
			if d.madctl&0x80 != 0 { // MY
				sy = d.Height - 1 - y
			}
			if d.madctl&0x20 == 0 {
				sx, sy = sy, sx
				img.SetRGBA(sx, sy, color.RGBA{R: r, G: g, B: b, A: 255})
			} else {
				img.SetRGBA(sx, sy, color.RGBA{R: r, G: g, B: b, A: 255})
			}
		}
	}
	return img
}

// WritePNG saves the current framebuffer.
func (d *ST7789) WritePNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, d.Framebuffer())
}
