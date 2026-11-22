package rp2350

import "image"

// HSTX is the RP2350 high-speed serial transmit block: a command expander and
// TMDS encoder fed from a 32-bit FIFO (0x50600000). This model decodes the
// command stream far enough to recover the pixel data, so DVI demos render in
// the UI and headless PNG export. The serial/TMDS output itself is not
// modelled.
const (
	hstxCtrlBase = 0x400C0000
	hstxFIFOBase = 0x50600000
)

const (
	hstxCSR         = 0x00
	hstxBit0        = 0x04
	hstxExpandShift = 0x24
	hstxExpandTMDS  = 0x28
)

const (
	hstxModeIdle = iota
	hstxModeRawWait
	hstxModeTMDS
)

type hstxBlock struct {
	ctrl *regBlock
	fifo *regBlock

	csr       uint32
	expand    uint32
	shift     uint32
	expandTMD uint32

	mode      int
	remaining int
	line      []byte
	lines     [][]byte
	frame     []byte
	frameW    int
	frameH    int
	height    int
	serial    uint64
}

func newHSTX() *hstxBlock {
	h := &hstxBlock{
		ctrl: newRegBlock(hstxCtrlBase, 0x1000),
		fifo: newRegBlock(hstxFIFOBase, 0x1000),
	}
	h.ctrl.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case hstxCSR:
			h.csr = v
		case hstxExpandShift:
			h.shift = v
		case hstxExpandTMDS:
			h.expandTMD = v
		}
	}
	h.fifo.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off == 0 { // STAT: empty, level 0
			return 1 << 9, true
		}
		return 0, false
	}
	h.fifo.writeFn = func(c *Chip, off, v uint32) {
		if off == 4 {
			h.push(v)
		}
	}
	h.ctrl.resetFn = func(c *Chip) { h.reset() }
	h.fifo.resetFn = func(c *Chip) { h.reset() }
	return h
}

func (h *hstxBlock) reset() {
	h.csr, h.expand, h.shift, h.expandTMD = 0, 0, 0, 0
	h.mode, h.remaining = hstxModeIdle, 0
	h.line = h.line[:0]
	h.lines = h.lines[:0]
	h.frame = h.frame[:0]
	h.frameW, h.frameH = 0, 0
	h.height = 0
}

// pixelBits is the sum of the three TMDS lane widths. The register fields
// store (bits - 1), so RGB332 is 2+3+3 = 8 bits.
func (h *hstxBlock) pixelBits() int {
	bits := int((h.expandTMD>>5)&7) + int((h.expandTMD>>13)&7) + int((h.expandTMD>>21)&7) + 3
	if bits < 1 || bits > 32 {
		return 8
	}
	return bits
}

func (h *hstxBlock) push(v uint32) {
	switch h.mode {
	case hstxModeRawWait:
		h.mode = hstxModeIdle
		return
	case hstxModeTMDS:
		h.consumePixels(v)
		return
	}
	cmd := (v >> 12) & 0xF
	count := int(v & 0xFFF)
	switch cmd {
	case 0x0, 0x1: // RAW, RAW_REPEAT
		h.mode = hstxModeRawWait
	case 0x2, 0x3: // TMDS, TMDS_REPEAT
		if h.height == 0 {
			h.height = dviHeightForWidth(count)
		}
		h.line = h.line[:0]
		h.remaining = count
		h.mode = hstxModeTMDS
	}
}

// consumePixels takes the bytes of one packed pixel word. HSTX packs pixels
// least-significant byte first; 8-bit RGB332 stores four per word, RGB565 two.
func (h *hstxBlock) consumePixels(v uint32) {
	bits := h.pixelBits()
	perWord := max(32/bits, 1)
	bytesPer := (bits + 7) / 8
	for i := 0; i < perWord && h.remaining > 0; i++ {
		var px [4]byte
		for b := 0; b < bytesPer && b < 4; b++ {
			px[b] = byte(v >> uint(i*bytesPer*8+b*8))
		}
		h.line = append(h.line, px[:bytesPer]...)
		h.remaining--
	}
	if h.remaining <= 0 {
		h.mode = hstxModeIdle
		h.lines = append(h.lines, append([]byte(nil), h.line...))
		if h.height > 0 && len(h.lines) >= h.height {
			h.finalize()
		}
	}
}

func (h *hstxBlock) finalize() {
	w := 0
	for _, l := range h.lines {
		if len(l) > w {
			w = len(l)
		}
	}
	if w == 0 {
		return
	}
	frame := make([]byte, w*len(h.lines))
	for i, l := range h.lines {
		copy(frame[i*w:], l)
	}
	h.frame = frame
	h.frameW = w
	h.frameH = len(h.lines)
	h.lines = h.lines[:0]
	h.serial++
}

// dviHeightForWidth maps the common DVI modes to their active line count.
// HSTX does not encode vertical timing in the command stream; the software's
// line callback owns it, so the model derives it from the active width.
func dviHeightForWidth(width int) int {
	switch width {
	case 320:
		return 240
	case 640:
		return 480
	case 720:
		return 480
	case 800:
		return 600
	case 1280:
		return 720
	case 1920:
		return 1080
	}
	return 480
}

// FrameRGBA converts the last complete DVI frame (or the lines accumulated so
// far) into an image. 8-bit pixels are RGB332, 16-bit RGB565, 24-bit RGB888.
func (h *hstxBlock) FrameRGBA() *image.RGBA {
	data, w, rows := h.frame, h.frameW, h.frameH
	if len(data) == 0 && len(h.lines) > 0 {
		w = 0
		for _, l := range h.lines {
			if len(l) > w {
				w = len(l)
			}
		}
		rows = len(h.lines)
		data = make([]byte, w*rows)
		for i, l := range h.lines {
			copy(data[i*w:], l)
		}
	}
	if w == 0 || rows == 0 {
		return nil
	}
	bits := h.pixelBits()
	bytesPer := (bits + 7) / 8
	if bytesPer < 1 || bytesPer > 4 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, rows))
	for y := 0; y < rows; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * bytesPer
			if off+bytesPer > len(data) {
				continue
			}
			var r, g, b uint8
			switch bytesPer {
			case 1:
				px := data[off]
				r = px >> 5
				g = (px >> 2) & 7
				b = px & 3
				r, g, b = uint8(uint32(r)*255/7), uint8(uint32(g)*255/7), uint8(uint32(b)*255/3)
			case 2:
				px := uint16(data[off]) | uint16(data[off+1])<<8
				r = uint8(px>>11) * 255 / 31
				g = uint8((px>>5)&0x3F) * 255 / 63
				b = uint8(px&0x1F) * 255 / 31
			default:
				r, g, b = data[off], data[off+1], data[off+2]
			}
			i := img.PixOffset(x, y)
			img.Pix[i] = r
			img.Pix[i+1] = g
			img.Pix[i+2] = b
			img.Pix[i+3] = 0xFF
		}
	}
	return img
}

// HSTXFrame returns the last complete DVI frame captured from the HSTX
// command stream, or nil before the first frame.
// HSTXSerial changes whenever a new complete DVI frame is captured.
func (c *Chip) HSTXSerial() uint64 {
	if c.hstx == nil {
		return 0
	}
	return c.hstx.serial
}

func (c *Chip) HSTXFrame() *image.RGBA {
	if c.hstx == nil {
		return nil
	}
	return c.hstx.FrameRGBA()
}
