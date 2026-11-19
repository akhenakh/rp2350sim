package rp2350

import "testing"

// TestFlashIs16MiB guards the board flash size and the top of the XIP window,
// which is where picotool's RP2350-E10 absolute block points.
func TestFlashIs16MiB(t *testing.T) {
	c := New()
	if len(c.Flash) != 16<<20 {
		t.Fatalf("flash size = %d, want %d", len(c.Flash), 16<<20)
	}
	const top = FlashBase + 16<<20 - 4
	c.LoadSegment(top, []byte{0x11, 0x22, 0x33, 0x44})
	if got := c.Read32(top); got != 0x44332211 {
		t.Fatalf("top word = %#x", got)
	}
}
