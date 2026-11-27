package devices

import (
	"math"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// DHTConfig describes a DHT11/DHT22 single-wire sensor.
type DHTConfig struct {
	Kind        string // dht11 or dht22; empty means dht22
	Humidity    float64
	Temperature float64
	BadChecksum bool
	// OnRead is called when the host starts a conversion, with the decoded
	// values and the raw five-byte frame.
	OnRead func(kind string, humidity, temperature float64, raw []byte)
}

// DHT is a DHT11/DHT22 model. The host pulls the line low for at least 1 ms,
// releases it, and the sensor answers with an 80 us low/high handshake followed
// by 40 bits (50 us low, 26-28 us high for 0, 70 us for 1).
type DHT struct {
	w    *Wire
	kind string
	hum  float64
	temp float64
	bad  bool

	state  uint8
	bit    int
	data   [5]byte
	action *rp2350.Deadline
	fallAt uint64
	onRead func(kind string, humidity, temperature float64, raw []byte)
}

const (
	dhtIdle uint8 = iota
	dhtResponseLow
	dhtResponseHigh
	dhtBitLow
	dhtBitHigh
)

// NewDHT attaches a DHT sensor to a wire.
func NewDHT(w *Wire, cfg DHTConfig) *DHT {
	if cfg.Kind == "" {
		cfg.Kind = "dht22"
	}
	if cfg.Humidity == 0 {
		cfg.Humidity = 45
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 23
	}
	w.EnablePullUp()
	d := &DHT{w: w, kind: cfg.Kind, hum: cfg.Humidity, temp: cfg.Temperature, bad: cfg.BadChecksum, onRead: cfg.OnRead}
	d.action = w.chip.AddDeadline(d.onAction)
	d.encode()
	w.OnEdge(d.onEdge)
	w.chip.AddResetHook(d.onReset)
	return d
}

func (d *DHT) onReset() {
	d.state = dhtIdle
	d.bit = 0
	// Keep the configured reading: the UI resets the chip after attaching
	// devices, and a cleared frame made every later read return 0 % / 0 C.
	d.encode()
	d.fallAt = 0
	d.w.Release()
}

func (d *DHT) onEdge(level bool) {
	now := d.w.Now()
	if !level {
		d.fallAt = now
		return
	}
	need := uint64(1000)
	if d.kind == "dht11" {
		need = 18000
	}
	if d.state == dhtIdle && now-d.fallAt >= need*uint64(rp2350.CyclesPerMicrosecond) {
		d.start()
	}
}

func (d *DHT) start() {
	if d.onRead != nil {
		d.onRead(d.kind, d.hum, d.temp, d.data[:])
	}
	d.state = dhtResponseLow
	d.w.DriveLow()
	d.action.Schedule(d.w.Now() + 80*uint64(rp2350.CyclesPerMicrosecond))
}

func (d *DHT) onAction() {
	switch d.state {
	case dhtResponseLow:
		d.w.DriveHigh()
		d.state = dhtResponseHigh
		d.action.Schedule(d.w.Now() + 80*uint64(rp2350.CyclesPerMicrosecond))
	case dhtResponseHigh:
		d.bit = 0
		d.bitLow()
	case dhtBitLow:
		d.w.DriveHigh()
		d.state = dhtBitHigh
		high := 26
		if d.dataBit(d.bit) == 1 {
			high = 70
		}
		d.action.Schedule(d.w.Now() + uint64(high)*uint64(rp2350.CyclesPerMicrosecond))
	case dhtBitHigh:
		d.bit++
		if d.bit == 40 {
			d.w.Release()
			d.state = dhtIdle
			return
		}
		d.bitLow()
	}
}

func (d *DHT) bitLow() {
	d.w.DriveLow()
	d.state = dhtBitLow
	d.action.Schedule(d.w.Now() + 50*uint64(rp2350.CyclesPerMicrosecond))
}

func (d *DHT) dataBit(i int) byte {
	return (d.data[i/8] >> uint(i%8)) & 1
}

func (d *DHT) encode() {
	if d.kind == "dht11" {
		hum := int(math.Round(d.hum))
		temp := int(math.Round(d.temp))
		if hum < 0 {
			hum = 0
		} else if hum > 100 {
			hum = 100
		}
		if temp < 0 {
			temp = 0
		} else if temp > 50 {
			temp = 50
		}
		d.data[0], d.data[1], d.data[2], d.data[3] = 0, byte(hum), 0, byte(temp)
	} else {
		hum := uint16(math.Round(d.hum * 10))
		temp := int(math.Round(d.temp * 10))
		var sign uint16
		if temp < 0 {
			sign = 0x8000
			temp = -temp
		}
		if temp > 1250 {
			temp = 1250
		}
		d.data[0], d.data[1] = byte(hum>>8), byte(hum)
		d.data[2] = byte(sign>>8 | uint16(temp)>>8)
		d.data[3] = byte(temp)
	}
	var sum byte
	for _, b := range d.data[:4] {
		sum += b
	}
	if d.bad {
		sum++
	}
	d.data[4] = sum
}
