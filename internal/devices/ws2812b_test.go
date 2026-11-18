package devices

import (
	"image/color"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// ws2812Program drives one WS2812B strip with the same PIO program piolib uses
// (shift left, autopull at 24 bits).
var ws2812Program = [8]uint16{
	0x80E0, // pull ifempty block
	0xE001, // set pins, 1
	0x6041, // out y, 1
	0x0065, // jmp !y, 5
	0x0206, // jmp 6 [2]
	0xE200, // set pins, 0 [2]
	0xE000, // set pins, 0
	0x01E1, // jmp !osre, 1 [1]
}

func setupWS2812PIO(c *rp2350.Chip) {
	const base = 0x50200000
	for i, instr := range ws2812Program {
		c.Write32(base+0x48+uint32(i)*4, uint32(instr))
	}
	c.Write32(base+0xDC, 1<<26)              // SET pins base 0, count 1
	c.Write32(base+0xCC, 7<<12)              // wrap top 7, bottom 0
	c.Write32(base+0xD0, 1<<30|1<<17|24<<25) // join TX, shift left, autopull 24
	c.Write32(base+0xC8, 20<<16)             // divide by 20
	c.Pins.Func[0] = 6                       // PIO0 function select
	c.Write32(base+0xD8, 0xE081)             // set pindirs, 1
	c.Write32(base+0xD8, 0x0000)             // jmp 0
	c.Write32(base+0x00, 1)                  // enable SM0
}

func pushWS2812(c *rp2350.Chip, word uint32) { c.Write32(0x50200010, word) }

func TestWS2812BDecode(t *testing.T) {
	c := rp2350.New()
	ws := NewWS2812B(c, 0)
	c.CycleHooks = append(c.CycleHooks, ws.Tick)
	setupWS2812PIO(c)

	// GRB words: red, green, blue.
	pushWS2812(c, 0x00FF0000)
	pushWS2812(c, 0xFF000000)
	pushWS2812(c, 0x0000FF00)

	for range 25000 {
		c.Step()
	}
	if got := ws.Frames(); got != 1 {
		t.Fatalf("frames = %d, want 1", got)
	}
	want := []color.RGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
	}
	got := ws.Colors()
	if len(got) != len(want) {
		t.Fatalf("decoded %d LEDs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LED %d = %v, want %v", i, got[i], want[i])
		}
	}
}
