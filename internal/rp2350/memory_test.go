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

// TestXIPAliases checks the RP2350 nocache/maintenance/notranslate windows all
// read the same flash and that maintenance writes are accepted as no-ops.
func TestXIPAliases(t *testing.T) {
	c := New()
	const off = 0x1234
	c.LoadSegment(FlashBase+off, []byte{0x11, 0x22, 0x33, 0x44})
	for _, base := range []uint32{XIPNoCacheBase, XIPMaintenanceBase, XIPNoTranslateBase} {
		if got := c.Read32(base + off); got != 0x44332211 {
			t.Fatalf("alias %#x read %#x", base, got)
		}
	}
	c.Write32(XIPMaintenanceBase+off, 0)
	if got := c.Read32(FlashBase + off); got != 0x44332211 {
		t.Fatalf("maintenance write changed flash: %#x", got)
	}
	if len(c.Unhandled) != 0 {
		t.Fatalf("alias access was reported unhandled: %v", c.Unhandled)
	}
}
