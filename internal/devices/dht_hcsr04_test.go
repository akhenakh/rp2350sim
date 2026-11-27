package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

const sioOutSet = 0xd0000018

func padsOutput(c *rp2350.Chip, pin int) {
	c.Write32(0x40038004+uint32(pin)*4, 1<<6)
}

func padsPullUp(c *rp2350.Chip, pin int) {
	c.Write32(0x40038004+uint32(pin)*4, 1<<3|1<<6)
}

func hostLow(c *rp2350.Chip, pin int, us uint64) {
	bit := uint32(1) << pin
	c.Write32(sioOutClr, bit)
	c.Write32(sioOeSet, bit)
	runUS(c, us)
}

func hostHigh(c *rp2350.Chip, pin int, us uint64) {
	bit := uint32(1) << pin
	c.Write32(sioOutSet, bit)
	c.Write32(sioOeSet, bit)
	runUS(c, us)
}

func TestDHTEncode(t *testing.T) {
	c := rp2350.New()
	d := NewDHT(NewWire(c, 6), DHTConfig{Humidity: 45, Temperature: 23})
	d.encode()
	want := [5]byte{0x01, 0xC2, 0x00, 0xE6, 0xA9}
	if d.data != want {
		t.Fatalf("dht22 = % X, want % X", d.data, want)
	}
	d11 := NewDHT(NewWire(c, 7), DHTConfig{Kind: "dht11", Humidity: 45, Temperature: 23})
	d11.encode()
	want11 := [5]byte{0, 45, 0, 23, 68}
	if d11.data != want11 {
		t.Fatalf("dht11 = % X, want % X", d11.data, want11)
	}
}

func TestDHTResponsePulse(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	padsPullUp(c, 6)
	NewDHT(NewWire(c, 6), DHTConfig{Humidity: 45, Temperature: 23})

	var edges []uint64
	var levels []bool
	c.Pins.AddChangeListener(func(pin int, level bool) {
		if pin == 6 {
			edges = append(edges, c.Cycles)
			levels = append(levels, level)
		}
	})

	hostLow(c, 6, 1000)
	c.Write32(sioOeClr, 1<<6)
	runUS(c, 400)

	if len(edges) < 6 {
		t.Fatalf("edges = %d", len(edges))
	}
	// The host release and the device response low share a cycle; find the
	// following rise and the first bit's fall.
	lowStart := edges[1]
	highStart := edges[0]
	for i := 2; i < len(edges); i++ {
		if levels[i] && edges[i] > lowStart {
			highStart = edges[i]
			break
		}
	}
	if highStart == 0 {
		t.Fatalf("handshake levels = %v", levels[:6])
	}
	if us := (highStart - lowStart) / uint64(rp2350.CyclesPerMicrosecond); us < 70 || us > 90 {
		t.Fatalf("response low = %d us", us)
	}
	fall := uint64(0)
	for i := 2; i < len(edges); i++ {
		if !levels[i] && edges[i] > highStart {
			fall = edges[i]
			break
		}
	}
	if fall == 0 {
		t.Fatal("no first-bit fall")
	}
	if us := (fall - highStart) / uint64(rp2350.CyclesPerMicrosecond); us < 70 || us > 90 {
		t.Fatalf("response high = %d us", us)
	}
}

func TestHCSR04Echo(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	padsOutput(c, 6)
	trig := NewWire(c, 6)
	echo := NewWire(c, 7)
	NewHCSR04(trig, echo, 100)

	var rise, fall uint64
	c.Pins.AddChangeListener(func(pin int, level bool) {
		if pin != 7 {
			return
		}
		if level {
			rise = c.Cycles
		} else {
			fall = c.Cycles
		}
	})

	hostHigh(c, 6, 10)
	c.Write32(sioOeClr, 1<<6)
	runUS(c, 6000)
	if rise == 0 || fall <= rise {
		t.Fatalf("echo edges rise=%d fall=%d", rise, fall)
	}
	width := (fall - rise) / uint64(rp2350.CyclesPerMicrosecond)
	if width < 5700 || width > 5900 {
		t.Fatalf("echo width = %d us, want ~5800", width)
	}
}

func TestDHTNegativeTemperature(t *testing.T) {
	c := rp2350.New()
	d := NewDHT(NewWire(c, 6), DHTConfig{Temperature: -5.0, Humidity: 45})
	d.encode()
	if d.data[2] != 0x80 || d.data[3] != 0x32 {
		t.Fatalf("DHT22 negative = % X", d.data)
	}
	d11 := NewDHT(NewWire(c, 7), DHTConfig{Kind: "dht11", Temperature: -5})
	d11.encode()
	if d11.data[3] != 0 {
		t.Fatalf("DHT11 negative = % X", d11.data)
	}
}

func TestDHT11PulseLength(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	padsPullUp(c, 6)
	NewDHT(NewWire(c, 6), DHTConfig{Kind: "dht11", Humidity: 45, Temperature: 23})
	edges := 0
	c.Pins.AddChangeListener(func(pin int, level bool) {
		if pin == 6 {
			edges++
		}
	})
	hostLow(c, 6, 1000)
	c.Write32(sioOeClr, 1<<6)
	runUS(c, 100)
	if edges > 2 { // host low + release only
		t.Fatalf("1 ms start pulse triggered DHT11: %d edges", edges)
	}
	edges = 0
	hostLow(c, 6, 18000)
	c.Write32(sioOeClr, 1<<6)
	runUS(c, 400)
	if edges < 6 {
		t.Fatalf("18 ms start pulse did not trigger DHT11: %d edges", edges)
	}
}

func TestHCSR04RejectsShortTrigger(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	padsOutput(c, 6)
	NewHCSR04(NewWire(c, 6), NewWire(c, 7), 100)
	rise := false
	c.Pins.AddChangeListener(func(pin int, level bool) {
		if pin == 7 && level {
			rise = true
		}
	})
	hostHigh(c, 6, 5)
	c.Write32(sioOeClr, 1<<6)
	runUS(c, 1000)
	if rise {
		t.Fatal("5 us trigger produced an echo")
	}
}

func TestHCSR04OutOfRange(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	padsOutput(c, 6)
	NewHCSR04(NewWire(c, 6), NewWire(c, 7), 401)
	rise := false
	c.Pins.AddChangeListener(func(pin int, level bool) {
		if pin == 7 && level {
			rise = true
		}
	})
	hostHigh(c, 6, 10)
	c.Write32(sioOeClr, 1<<6)
	runUS(c, 100000)
	if rise {
		t.Fatal("distance above 400 cm produced an echo")
	}
}

func TestDHTResetHook(t *testing.T) {
	c := rp2350.New()
	d := NewDHT(NewWire(c, 6), DHTConfig{Humidity: 45, Temperature: 23})
	d.state = dhtResponseLow
	d.bit = 17
	d.data[0] = 0xFF
	c.Reset()
	if d.state != dhtIdle || d.bit != 0 {
		t.Fatalf("DHT state survived reset: state=%d bit=%d", d.state, d.bit)
	}
	// The configured reading must survive a reset (the UI resets after attach).
	if want := [5]byte{0x01, 0xC2, 0x00, 0xE6, 0xA9}; d.data != want {
		t.Fatalf("DHT frame after reset = % X, want % X", d.data, want)
	}
}
