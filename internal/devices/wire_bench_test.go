package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// BenchmarkChipIdleWire confirms a slept core with an idle timed-wire device
// still takes the virtual-time fast-forward.
func BenchmarkChipIdleWire(b *testing.B) {
	c := rp2350.New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	NewDHT(NewWire(c, 6), DHTConfig{Humidity: 45, Temperature: 23})
	b.ResetTimer()
	for range b.N {
		c.Run(1000)
	}
}
