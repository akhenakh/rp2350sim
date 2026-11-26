package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// TestAttachedDevicesAllowIdleFastForward pins the scheduler contract: a board
// with a WS2812B decoder, an audio decoder, both Ethernet models and the UI's
// scope/logic traces must still let an idle chip jump instead of polling every
// cycle. Before the deadline/listener split these devices registered a
// CycleHook and made the fast-forward impossible on those boards.
func TestAttachedDevicesAllowIdleFastForward(t *testing.T) {
	cases := []struct {
		name string
		cfg  AudioConfig
	}{
		{"pwm", AudioConfig{Kind: AudioPWM, Pin: 0, Rate: 48000}},
		{"i2s", AudioConfig{Kind: AudioI2S, BCLK: 0, LRCLK: 1, Data: 2, Bits: 16, Rate: 48000}},
		{"pdm", AudioConfig{Kind: AudioPDM, Data: 0, Clock: 1, Rate: 48000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := rp2350.New()
			NewWS2812B(c, 3)
			NewAudio(c, tc.cfg)
			w5 := NewW5500(c, 5)
			t.Cleanup(w5.Close)
			w6 := NewW6300(c, 6)
			t.Cleanup(w6.Close)

			tr := NewTrace(c, []int{3}, ScopeInterval, ScopeDepth)
			c.AddPeriodicHook(tr.Interval, tr.Tick)

			c.CPU0.Sleeping = true
			c.CPU1.Sleeping = true
			if !c.IdleFastForward() {
				t.Fatal("attached devices forbade the idle fast-forward")
			}
			c.Run(1_000_000)
			if c.Cycles != 1_000_000 {
				t.Fatalf("Run stopped at %d cycles", c.Cycles)
			}
		})
	}
}
