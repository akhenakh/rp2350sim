package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

const testSampleHz = 2e6 // LogicInterval at 150 MHz

// uartTrace builds an 8N1 waveform (idle high, start low, LSB first).
func uartTrace(baud float64, b byte) []TraceSample {
	spb := testSampleHz / baud
	n := int(spb * 12)
	out := make([]TraceSample, n)
	for i := range out {
		bits := float64(i) / spb
		high := true
		switch {
		case bits >= 1 && bits < 2:
			high = false // start
		case bits >= 2 && bits < 10:
			bit := int(bits - 2)
			high = b&(1<<uint(bit)) != 0
		}
		if high {
			out[i].V[0] = 3.3
		}
		out[i].T = uint64(i)
	}
	return out
}

func TestDecodeUART(t *testing.T) {
	frames := DecodeUART(uartTrace(115200, 'A'), 0, 115200, testSampleHz)
	if len(frames) != 1 || frames[0].Data != 'A' {
		t.Fatalf("frames = %+v, want one 'A'", frames)
	}

	frames = DecodeUART(uartTrace(9600, 0x5A), 0, 9600, testSampleHz)
	if len(frames) != 1 || frames[0].Data != 0x5A {
		t.Fatalf("9600 frames = %+v", frames)
	}

	// Concatenated frames: each is decoded at its own start bit.
	a := uartTrace(115200, 'o')
	b := uartTrace(115200, 'k')
	frames = DecodeUART(append(a, b...), 0, 115200, testSampleHz)
	if len(frames) != 2 || frames[0].Data != 'o' || frames[1].Data != 'k' {
		t.Fatalf("two frames = %+v", frames)
	}
}

// i2cTrace builds START, 9-bit address phase, 9-bit data phase and STOP.
func i2cTrace(addr, data byte) []TraceSample {
	const per = 20 // samples per SCL half period
	var out []TraceSample
	add := func(sda, scl bool, n int) {
		for range n {
			var s TraceSample
			if sda {
				s.V[0] = 3.3
			}
			if scl {
				s.V[1] = 3.3
			}
			s.T = uint64(len(out))
			out = append(out, s)
		}
	}
	add(true, true, per)   // idle
	add(false, true, per)  // START: SDA falls while SCL high
	add(false, false, per) // SCL low
	phase := func(v byte, ack bool) {
		for i := 7; i >= 0; i-- {
			add(v&(1<<uint(i)) != 0, false, per)
			add(v&(1<<uint(i)) != 0, true, per)
		}
		add(ack, false, per) // ACK bit set up low
		add(ack, true, per)
	}
	phase(addr, false)
	phase(data, false)
	add(false, false, per) // SDA and SCL low
	add(false, true, per)  // SCL high, SDA still low
	add(true, true, per)   // STOP: SDA rises while SCL high
	add(true, true, per)
	return out
}

func TestDecodeI2C(t *testing.T) {
	events := DecodeI2C(i2cTrace(0x50, 0x00), 0, 1)
	var kinds []I2CEventKind
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []I2CEventKind{I2CStart, I2CAddr, I2CAck, I2CData, I2CAck, I2CStop}
	if len(kinds) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event %d = %v, want %v (%+v)", i, kinds[i], want[i], events)
		}
	}
	if events[1].Byte != 0x50 || events[1].Read {
		t.Fatalf("address = %+v", events[1])
	}
	if !events[2].Ack || !events[4].Ack {
		t.Fatalf("ack bits = %+v", events)
	}
}

// spiTrace clocks one byte: CS low, mode 0, MSB first.
func spiTrace(mosi, miso byte) []TraceSample {
	const half = 10
	var out []TraceSample
	add := func(mo, mi, sck, cs bool, n int) {
		for range n {
			var s TraceSample
			if mo {
				s.V[0] = 3.3
			}
			if mi {
				s.V[1] = 3.3
			}
			if sck {
				s.V[2] = 3.3
			}
			if cs {
				s.V[3] = 3.3
			}
			s.T = uint64(len(out))
			out = append(out, s)
		}
	}
	add(false, false, false, false, half)
	for i := 7; i >= 0; i-- {
		mo := mosi&(1<<uint(i)) != 0
		mi := miso&(1<<uint(i)) != 0
		add(mo, mi, false, false, half)
		add(mo, mi, true, false, half)
	}
	add(false, false, false, true, half)
	return out
}

func TestDecodeSPI(t *testing.T) {
	frames := DecodeSPI(spiTrace(0xA5, 0x3C), 2, 0, 1, 3)
	if len(frames) != 1 || frames[0].MOSI != 0xA5 || frames[0].MISO != 0x3C {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestTraceSamplesPins(t *testing.T) {
	c := rp2350.New()
	tr := NewTrace(c, []int{0, 1}, 10, 4)
	for i := range 20 {
		c.Cycles += 10
		if i%2 == 0 {
			c.Pins.SetAnalog(0, 3.3)
		} else {
			c.Pins.SetAnalog(0, 0)
		}
		c.Pins.SetAnalog(1, 1.65)
		tr.Tick()
	}
	pins, samples := tr.Snapshot()
	if len(pins) != 2 || len(samples) != 4 {
		t.Fatalf("pins=%v samples=%d", pins, len(samples))
	}
	if samples[0].V[1] != 1.65 {
		t.Fatalf("channel 1 = %v", samples[0].V[1])
	}
	// The last sample is i=19, when GP0 was low.
	if samples[3].Bit(0) {
		t.Fatalf("last GP0 sample high: %v", samples[3].V[0])
	}
}

func TestSignalSourceSine(t *testing.T) {
	c := rp2350.New()
	src := NewSignalSource(c, 26, WaveSine, 1000, 1.65, 1.65)
	for range 400 {
		c.Cycles += 150
		src.Tick()
	}
	if v := c.Pins.Voltage(26); v < 0 || v > 3.3 {
		t.Fatalf("voltage = %v", v)
	}
	if !c.Pins.AnalogValid[26] {
		t.Fatal("analog source not applied")
	}
	// A driven pin overrides the external source.
	c.Pins.OE |= 1 << 26
	c.Pins.Out |= 1 << 26
	if v := c.Pins.Voltage(26); v != 3.3 {
		t.Fatalf("driven voltage = %v, want 3.3", v)
	}
}
