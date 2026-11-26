package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestWS2812BRewindDropsPartialFrame(t *testing.T) {
	c := rp2350.New()
	configurePin(c, 0)
	w := NewWS2812B(c, 0)
	setLevel := func(high bool) { c.Pins.SetExternal(0, high) }
	for range 23 {
		c.Cycles++
		setLevel(true)
		c.Cycles += 10
		setLevel(false)
	}
	c.Cycles = 1000
	w.Tick()
	if w.Frames() != 0 {
		t.Fatal("partial frame latched before rewind")
	}

	c.Cycles = 1
	setLevel(false)
	w.Tick()
	if w.Frames() != 0 || len(w.bits) != 0 {
		t.Fatalf("rewind retained a partial frame: frames=%d bits=%d", w.Frames(), len(w.bits))
	}
}
