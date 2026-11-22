package rp2350

import "testing"

func TestHSTXFrame(t *testing.T) {
	c := New()
	// RGB332: lane widths 2/3/3 bits (register value = bits-1).
	c.Write32(hstxCtrlBase+hstxExpandTMDS, 2<<21|2<<13|1<<5)
	h := c.hstx

	pixelWord := uint32(0xE0) | uint32(0x1C)<<8 | uint32(0x03)<<16 | uint32(0xFF)<<24
	// Two active lines of four pixels each.
	for range 2 {
		h.push(2<<12 | 4) // TMDS, 4 pixels
		h.push(pixelWord)
	}
	h.push(0xF << 12) // NOP: blanking line ends the frame
	h.push(2<<12 | 4) // first line of the next frame

	img := c.HSTXFrame()
	if img == nil {
		t.Fatal("no DVI frame")
	}
	if b := img.Bounds(); b.Dx() != 4 || b.Dy() != 2 {
		t.Fatalf("frame = %dx%d, want 4x2", b.Dx(), b.Dy())
	}
	// RGB332 0xE0 is red (111 000 00), 0x1C is green-ish, 0x03 is blue.
	if r, g, b, _ := img.At(0, 0).RGBA(); r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Fatalf("pixel 0 = %d,%d,%d", r>>8, g>>8, b>>8)
	}
	if _, _, b, _ := img.At(2, 0).RGBA(); b>>8 < 200 {
		t.Fatalf("blue pixel = %d", b>>8)
	}
}
