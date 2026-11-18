// Package devices implements external chips attached to the RP2350 buses.
package devices

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// ST7789 models an ST7789-family SPI TFT controller. It decodes the command
// stream produced by TinyGo's st7789 driver and keeps the GRAM contents.
//
// The GRAM is addressed the way the controller sees it: CASET selects the
// column and RASET the row of each pixel. The driver applies rotation through
// MADCTL rather than by transforming pixel data, so rendering in address order
// reproduces the logical image the driver intended (MX/MY/MV then only decide
// how the panel itself scans that memory).
type ST7789 struct {
	Chip *rp2350.Chip

	// Wiring: chip-select, data/command and reset pins.
	CS, DC, RST int

	// GRAM addressable size.
	Width, Height int

	gram   []uint16
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
	// Used window, for rendering.
	minX, minY, maxX, maxY int

	// Logger, when non-nil, records commands and a few data bytes.
	Logger *slog.Logger
	logN   int
}

// NewST7789 creates a display attached to the chip, wired to the given pins.
func NewST7789(chip *rp2350.Chip, cs, dc, rst int) *ST7789 {
	d := &ST7789{Chip: chip, CS: cs, DC: dc, RST: rst, Width: 240, Height: 320}
	d.gram = make([]uint16, d.Width*d.Height)
	d.reset()
	return d
}

// reset restores the post-reset controller state.
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
	d.minX, d.minY, d.maxX, d.maxY = -1, -1, -1, -1
}

// Transfer feeds one byte clocked over SPI and returns the MISO byte.
func (d *ST7789) Transfer(tx byte) byte {
	if !d.Chip.Pins.Level(d.RST) {
		d.reset()
		return 0x00
	}
	if d.Chip.Pins.Level(d.CS) {
		return 0x00
	}
	command := !d.Chip.Pins.Level(d.DC)
	if command {
		loggerOr(d.Logger).Debug("lcd command", slog.String("byte", fmt.Sprintf("%02x", tx)))
	} else if d.logN < 24 {
		loggerOr(d.Logger).Debug("lcd data", slog.String("byte", fmt.Sprintf("%02x", tx)))
		d.logN++
	}
	if command {
		d.command(tx)
	} else {
		d.data(tx)
	}
	return 0x00
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
	case 0x2A, 0x2B: // CASET / RASET: data follows
		d.inData = false
	case 0x01: // SWRESET
		d.reset()
		d.cmd = c
	case 0x28: // DISPOFF
		d.on = false
	case 0x29: // DISPON
		d.on = true
	case 0x20: // INVOFF
		d.inverted = false
	case 0x21: // INVON
		d.inverted = true
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
		d.putPixel(px)
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
	}
}

func (d *ST7789) putPixel(px uint16) {
	x, y := d.x, d.y
	if x >= 0 && x < d.Width && y >= 0 && y < d.Height {
		d.gram[y*d.Width+x] = px
		d.pixels++
		if d.minX < 0 || x < d.minX {
			d.minX = x
		}
		if d.maxX < x {
			d.maxX = x
		}
		if d.minY < 0 || y < d.minY {
			d.minY = y
		}
		if d.maxY < y {
			d.maxY = y
		}
	}
	d.x++
	if d.x > d.casetX[1] {
		d.x = d.casetX[0]
		d.y++
		if d.y > d.casetY[1] {
			d.y = d.casetY[0]
		}
	}
}

// PixelsWritten reports how many pixels were stored since creation.
func (d *ST7789) PixelsWritten() int { return d.pixels }

// On reports whether the display is enabled.
func (d *ST7789) On() bool { return d.on }

// Framebuffer renders the written window as RGBA, in driver (address) order.
func (d *ST7789) Framebuffer() *image.RGBA {
	if d.minX < 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	w := d.maxX - d.minX + 1
	h := d.maxY - d.minY + 1
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := d.minY; y <= d.maxY; y++ {
		for x := d.minX; x <= d.maxX; x++ {
			px := d.gram[y*d.Width+x]
			r := uint8(px>>11) << 3
			g := uint8(px>>5) << 2
			b := uint8(px) << 3
			if d.madctl&0x08 != 0 { // BGR
				r, b = b, r
			}
			// INVOFF/INVON compensate for panel polarity; the driver writes
			// the intended colour into GRAM, so the exported image keeps it
			// as written.
			img.SetRGBA(x-d.minX, y-d.minY, color.RGBA{R: r, G: g, B: b, A: 255})
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
	if err := png.Encode(f, d.Framebuffer()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
