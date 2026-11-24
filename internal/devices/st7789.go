// Package devices implements external chips attached to the RP2350 buses.
package devices

import (
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

const (
	st7789GramWidth  = 240
	st7789GramHeight = 320
)

// ST7789 models an ST7789-family SPI TFT controller. It decodes the command
// stream produced by TinyGo's st7789 driver and keeps the GRAM contents.
//
// The controller has a full 240x320 GRAM, while a panel such as the GEEK only
// exposes a centred 240x135 window in landscape. CASET/RASET address the full
// controller memory; Framebuffer crops the visible panel rectangle. The driver
// applies rotation through MADCTL rather than by transforming pixel data, so
// rendering in address order reproduces the logical image it intended.
type ST7789 struct {
	Chip *rp2350.Chip

	// Wiring: chip-select, data/command and reset pins.
	CS, DC, RST int

	// Width and Height are the visible panel dimensions in native orientation.
	Width, Height int
	// The full controller GRAM is independent of the visible panel. MV swaps
	// the addressable axes, but never the physical row pitch.
	gramWidth, gramHeight int
	stride                int

	gram             []uint16
	casetX           [2]int
	casetY           [2]int
	visibleOriginX   int
	visibleOriginY   int
	visibleWindowSet bool
	x, y             int
	cmd              byte
	params           []byte
	inData           bool
	// pending high byte for RGB565 input
	hi       byte
	haveHi   bool
	madctl   byte
	inverted bool
	on       bool
	sleeping bool
	// visualGen changes whenever the exported image can change, including
	// configuration-only changes such as MADCTL and sleep transitions.
	visualGen uint64
	// Brightness scales the exported image, matching a PWM backlight.
	Brightness float32
	pixels     int
	// Used window, for rendering.
	minX, minY, maxX, maxY int

	// Logger, when non-nil, records commands and a few data bytes.
	Logger *slog.Logger
	logN   int
}

// NewST7789 creates a display attached to the chip, wired to the given pins.
// width and height are the visible panel geometry in native orientation
// (240x320 when unknown); the controller GRAM remains 240x320.
func NewST7789(chip *rp2350.Chip, cs, dc, rst, width, height int) *ST7789 {
	if width <= 0 || height <= 0 {
		width, height = st7789GramWidth, st7789GramHeight
	}
	d := &ST7789{
		Chip: chip, CS: cs, DC: dc, RST: rst,
		Width: width, Height: height, Brightness: 1,
		gramWidth: st7789GramWidth, gramHeight: st7789GramHeight,
		stride: st7789GramWidth,
	}
	d.gram = make([]uint16, d.gramWidth*d.gramHeight)
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
	d.sleeping = true
	d.visualGen++
	for i := range d.gram {
		d.gram[i] = 0
	}
	maxX, maxY := d.addressSize()
	d.casetX = [2]int{0, maxX - 1}
	d.casetY = [2]int{0, maxY - 1}
	d.visibleWindowSet = false
	d.updateVisibleOrigin()
	d.minX, d.minY, d.maxX, d.maxY = -1, -1, -1, -1
}

func (d *ST7789) addressSize() (width, height int) {
	if d.madctl&0x20 != 0 { // MV exchanges rows and columns
		return d.gramHeight, d.gramWidth
	}
	return d.gramWidth, d.gramHeight
}

func (d *ST7789) visibleSize() (width, height int) {
	width, height = d.Width, d.Height
	if d.madctl&0x20 != 0 {
		width, height = height, width
	}
	return width, height
}

// updateVisibleOrigin follows the last full visible CASET/RASET window. TinyGo
// uses logical windows starting at (0,0) unless panel offsets are configured;
// offset-aware drivers use the physical GEEK window at (40,53). Partial writes
// retain the origin selected by the last full-frame window.
func (d *ST7789) updateVisibleOrigin() {
	width, height := d.visibleSize()
	fullWindow := d.casetX[1]-d.casetX[0]+1 == width && d.casetY[1]-d.casetY[0]+1 == height
	if fullWindow {
		d.visibleOriginX, d.visibleOriginY = d.casetX[0], d.casetY[0]
		d.visibleWindowSet = true
		return
	}
	if d.visibleWindowSet {
		return
	}
	addressWidth, addressHeight := d.addressSize()
	d.visibleOriginX = max(0, (addressWidth-width+1)/2)
	d.visibleOriginY = max(0, (addressHeight-height+1)/2)
}

func (d *ST7789) visibleGeometry() (width, height, originX, originY int) {
	width, height = d.visibleSize()
	return width, height, d.visibleOriginX, d.visibleOriginY
}

// Transfer feeds one byte clocked over SPI and returns the MISO byte.
func (d *ST7789) Transfer(tx byte) byte {
	if resetAsserted(d.Chip, d.RST) {
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
	case 0x10: // SLPIN
		if !d.sleeping {
			d.sleeping = true
			d.visualGen++
		}
	case 0x11: // SLPOUT
		if d.sleeping {
			d.sleeping = false
			d.visualGen++
		}
	case 0x28: // DISPOFF
		if d.on {
			d.on = false
			d.visualGen++
		}
	case 0x29: // DISPON
		if !d.on {
			d.on = true
			d.visualGen++
		}
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
			d.updateVisibleOrigin()
		}
	case 0x2B: // RASET
		if len(d.params) == 4 {
			d.casetY[0] = int(d.params[0])<<8 | int(d.params[1])
			d.casetY[1] = int(d.params[2])<<8 | int(d.params[3])
			d.updateVisibleOrigin()
		}
	case 0x36: // MADCTL
		if d.madctl != b {
			oldMV := d.madctl & 0x20
			d.madctl = b
			if oldMV != b&0x20 {
				d.visibleWindowSet = false
				d.updateVisibleOrigin()
			}
			d.visualGen++
		}
	}
}

func (d *ST7789) putPixel(px uint16) {
	x, y := d.x, d.y
	maxX, maxY := d.addressSize()
	if x >= 0 && x < maxX && y >= 0 && y < maxY {
		d.gram[y*d.stride+x] = px
		d.pixels++
		d.visualGen++
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

// Sleeping reports whether SLPIN is holding the panel in sleep mode.
func (d *ST7789) Sleeping() bool { return d.sleeping }

// VisualGeneration changes whenever the exported framebuffer can change.
func (d *ST7789) VisualGeneration() uint64 { return d.visualGen }

// Framebuffer renders the visible panel rectangle as RGBA, in driver
// (address) order. With MADCTL MV the panel is addressed with rows and columns
// exchanged. Sleep blanks the output without discarding GRAM.
func (d *ST7789) Framebuffer() *image.RGBA {
	w, h, originX, originY := d.visibleGeometry()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	brightness := d.Brightness
	if !d.on || d.sleeping {
		brightness = 0
	}
	if brightness < 0 {
		brightness = 0
	}
	if brightness > 1 {
		brightness = 1
	}
	// Per-channel lookup tables apply the backlight once instead of doing a
	// float multiply for every pixel.
	var rTab [32]uint8
	var gTab [64]uint8
	var bTab [32]uint8
	for i := range rTab {
		rTab[i] = uint8(float32(uint8(i)<<3) * brightness)
	}
	for i := range gTab {
		gTab[i] = uint8(float32(uint8(i)<<2) * brightness)
	}
	for i := range bTab {
		bTab[i] = uint8(float32(uint8(i)<<3) * brightness)
	}
	bgr := d.madctl&0x08 != 0
	addressWidth, addressHeight := d.addressSize()
	pix := img.Pix
	for y := range h {
		gramY := originY + y
		o := y * w * 4
		for x := range w {
			var px uint16
			gramX := originX + x
			if gramX >= 0 && gramX < addressWidth && gramY >= 0 && gramY < addressHeight {
				px = d.gram[gramY*d.stride+gramX]
			}
			r := rTab[(px>>11)&0x1f]
			g := gTab[(px>>5)&0x3f]
			b := bTab[px&0x1f]
			if bgr {
				r, b = b, r
			}
			// INVOFF/INVON compensate for panel polarity; the driver writes
			// the intended colour into GRAM, so the exported image keeps it
			// as written.
			pix[o] = r
			pix[o+1] = g
			pix[o+2] = b
			pix[o+3] = 255
			o += 4
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
