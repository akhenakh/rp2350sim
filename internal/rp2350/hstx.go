package rp2350

import (
	"image"
	"math/bits"
)

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
	hstxFIFOData    = 0x04
)

const (
	hstxCSRReset         = 1<<28 | 5<<16 | 6<<8
	hstxCSRMask          = 0xFF1F1F73
	hstxExpandShiftReset = 1<<24 | 1<<8
	hstxExpandShiftMask  = 0x1F1F1F1F
	hstxExpandTMDSMask   = 0x00FFFFFF
	hstxFIFODepth        = 8
)

const (
	hstxModeIdle = iota
	hstxModeRaw
	hstxModeTMDS
)

type hstxBlock struct {
	ctrl *regBlock
	fifo *regBlock

	csr       uint32
	shift     uint32
	expandTMD uint32

	fifoData  [hstxFIFODepth]uint32
	fifoHead  int
	fifoLevel int
	wof       bool

	mode         int
	remaining    int // -1 means the command count is infinite
	repeat       bool
	repeatLoaded bool
	repeatWord   uint32
	line         []byte
	lines        [][]byte
	frame        []byte
	frameW       int
	frameH       int
	height       int
	activeW      int
	serial       uint64
}

func newHSTX() *hstxBlock {
	h := &hstxBlock{
		ctrl: newRegBlock(hstxCtrlBase, 0x1000),
		fifo: newRegBlock(hstxFIFOBase, 0x1000),
	}
	h.ctrl.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case hstxCSR:
			return h.csr, true
		case hstxExpandShift:
			return h.shift, true
		case hstxExpandTMDS:
			return h.expandTMD, true
		}
		return 0, false
	}
	h.ctrl.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case hstxCSR:
			wasEnabled := h.csr&1 != 0
			h.csr = v & hstxCSRMask
			if wasEnabled && h.csr&1 == 0 {
				h.resetExpander()
			}
		case hstxExpandShift:
			h.shift = v & hstxExpandShiftMask
		case hstxExpandTMDS:
			h.expandTMD = v & hstxExpandTMDSMask
		}
	}
	h.fifo.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0:
			return h.stat(), true
		case hstxFIFOData:
			return 0, true
		}
		return 0, false
	}
	h.fifo.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0:
			if v&(1<<10) != 0 {
				h.wof = false
			}
		case hstxFIFOData:
			h.push(v)
		}
	}
	h.ctrl.resetFn = func(c *Chip) { h.reset() }
	h.fifo.resetFn = func(c *Chip) { h.reset() }
	h.reset()
	return h
}

func (h *hstxBlock) reset() {
	h.csr = hstxCSRReset
	h.shift = hstxExpandShiftReset
	h.expandTMD = 0
	h.fifoHead, h.fifoLevel = 0, 0
	h.wof = false
	h.resetExpander()
	h.line = h.line[:0]
	h.lines = h.lines[:0]
	h.frame = h.frame[:0]
	h.frameW, h.frameH = 0, 0
	h.height, h.activeW = 0, 0
	h.serial = 0
}

func (h *hstxBlock) resetExpander() {
	h.mode, h.remaining = hstxModeIdle, 0
	h.repeat, h.repeatLoaded = false, false
	h.repeatWord = 0
	h.line = h.line[:0]
}

func (h *hstxBlock) stat() uint32 {
	var v uint32
	if h.fifoLevel == 0 {
		v |= 1 << 9
	}
	if h.fifoLevel == hstxFIFODepth {
		v |= 1 << 8
	}
	if h.wof {
		v |= 1 << 10
	}
	return v | uint32(h.fifoLevel)
}

func (h *hstxBlock) active() bool {
	if h.csr&1 == 0 {
		return false
	}
	if h.fifoLevel != 0 {
		return true
	}
	return h.csr&(1<<1) != 0 && h.mode != hstxModeIdle && h.repeat && h.repeatLoaded && h.remaining != 0
}

func (h *hstxBlock) push(v uint32) {
	if h.fifoLevel == hstxFIFODepth {
		h.wof = true
		return
	}
	h.fifoData[(h.fifoHead+h.fifoLevel)%hstxFIFODepth] = v
	h.fifoLevel++
}

func (h *hstxBlock) pop() uint32 {
	v := h.fifoData[h.fifoHead]
	h.fifoHead = (h.fifoHead + 1) % hstxFIFODepth
	h.fifoLevel--
	return v
}

// pixelBits is the sum of the three TMDS lane widths. The register fields
// store (bits - 1), so RGB332 is 2+3+3 = 8 bits.
func (h *hstxBlock) pixelBits() int {
	n := int((h.expandTMD>>5)&7) + int((h.expandTMD>>13)&7) + int((h.expandTMD>>21)&7) + 3
	if n < 1 || n > 32 {
		return 8
	}
	return n
}

func (h *hstxBlock) tick() {
	if h.csr&1 == 0 {
		return
	}
	if h.csr&(1<<1) != 0 && h.mode != hstxModeIdle && h.repeat && h.repeatLoaded {
		h.output(h.repeatWord)
		h.rotateRepeat()
		return
	}
	if h.fifoLevel == 0 {
		return
	}
	v := h.pop()
	if h.csr&(1<<1) == 0 {
		return
	}
	if h.mode == hstxModeIdle {
		h.command(v)
		return
	}
	if h.repeat {
		h.repeatWord = v
		h.repeatLoaded = true
	}
	h.output(v)
	h.rotateRepeat()
}

func (h *hstxBlock) command(v uint32) {
	cmd := (v >> 12) & 0xF
	count := int(v & 0xFFF)
	if count == 0 {
		count = -1
	}
	h.repeat = cmd&1 != 0
	h.repeatLoaded = false
	h.remaining = count
	switch cmd & 0xE {
	case 0x0: // RAW, RAW_REPEAT
		h.mode = hstxModeRaw
	case 0x2: // TMDS, TMDS_REPEAT
		h.mode = hstxModeTMDS
		if count > 0 && h.activeW != count {
			// The active width changed (a mode switch): relearn the frame
			// height and start the current frame again.
			h.activeW = count
			h.height = dviHeightForWidth(count)
			h.lines = h.lines[:0]
		}
		h.line = h.line[:0]
	default:
		h.mode, h.remaining = hstxModeIdle, 0
		h.repeat = false
	}
}

func (h *hstxBlock) output(v uint32) {
	switch h.mode {
	case hstxModeRaw:
		if h.remaining > 0 {
			h.remaining--
			if h.remaining == 0 {
				h.endCommand()
			}
		}
	case hstxModeTMDS:
		h.consumePixels(v)
	}
}

func (h *hstxBlock) rotateRepeat() {
	if !h.repeat || h.mode == hstxModeIdle {
		return
	}
	shift := int((h.shift >> 16) & 0x1F)
	if h.mode == hstxModeRaw {
		shift = int(h.shift & 0x1F)
	}
	h.repeatWord = bits.RotateLeft32(h.repeatWord, -shift)
}

func (h *hstxBlock) endCommand() {
	h.mode, h.remaining = hstxModeIdle, 0
	h.repeat, h.repeatLoaded = false, false
	h.repeatWord = 0
}

func (h *hstxBlock) lane(v uint32, rot, width uint32) uint32 {
	return bits.RotateLeft32(v, int(rot)) & ((1 << width) - 1)
}

// decodePixel reconstructs the packed little-endian pixel from the three TMDS
// lane inputs. The lane rotations select each component from the expander word.
func (h *hstxBlock) decodePixel(v uint32) uint32 {
	n0 := (h.expandTMD >> 5) & 7
	n1 := (h.expandTMD >> 13) & 7
	n2 := (h.expandTMD >> 21) & 7
	n0++
	n1++
	n2++
	r := h.lane(v, h.expandTMD&0x1F, n0)
	g := h.lane(v, (h.expandTMD>>8)&0x1F, n1)
	b := h.lane(v, (h.expandTMD>>16)&0x1F, n2)
	total := n0 + n1 + n2
	return r<<(total-n0) | g<<(total-n0-n1) | b
}

// consumePixels takes the pixels of one expander output word. HSTX packs
// pixels least-significant byte first; 8-bit RGB332 stores four per word,
// RGB565 two. ENC_SHIFT selects the next pixel within the word.
func (h *hstxBlock) consumePixels(v uint32) {
	if h.remaining == 0 {
		return
	}
	pixelBits := h.pixelBits()
	perWord := max(32/pixelBits, 1)
	bytesPer := (pixelBits + 7) / 8
	encShift := int((h.shift >> 16) & 0x1F)
	for i := 0; i < perWord && h.remaining != 0; i++ {
		data := bits.RotateLeft32(v, -i*encShift)
		px := h.decodePixel(data)
		for b := range bytesPer {
			h.line = append(h.line, byte(px>>uint(b*8)))
		}
		if h.remaining > 0 {
			h.remaining--
		}
	}
	if h.remaining == 0 {
		h.endCommand()
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
