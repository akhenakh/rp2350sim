package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// DS18B20Config describes one 1-Wire temperature sensor.
type DS18B20Config struct {
	TemperatureC float64
	// Serial is the six-byte ROM serial. Zero means a deterministic default.
	Serial [6]byte
	// OnRead is called when the host reads the scratchpad.
	OnRead func(temperatureC float64)
}

// DS18B20 is a bit-level DS18B20 model on a single Wire. It implements reset/
// presence, the common ROM commands and the scratchpad, with Dallas CRC8.
type DS18B20 struct {
	w     *Wire
	cfg   DS18B20Config
	state uint8

	fallAt uint64
	reset  bool

	sample   *rp2350.Deadline
	check    *rp2350.Deadline
	action   *rp2350.Deadline
	readSlot *rp2350.Deadline
	release  *rp2350.Deadline

	bitIndex   int
	byteVal    byte
	searchSlot int
	searchEdge int
	matchIndex int

	read     []byte
	readByte int
	readBit  int

	temp int16
	th   byte
	tl   byte
	conf byte
}

const (
	dsIdle uint8 = iota
	dsCommand
	dsRead
	dsSearch
	dsMatch
)

// NewDS18B20 attaches one sensor to a wire.
func NewDS18B20(w *Wire, cfg DS18B20Config) *DS18B20 {
	if cfg.Serial == ([6]byte{}) {
		cfg.Serial = [6]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	}
	if cfg.TemperatureC == 0 {
		cfg.TemperatureC = 22.5
	}
	w.EnablePullUp()
	d := &DS18B20{w: w, cfg: cfg, th: 0x4B, tl: 0x46, conf: 0x7F}
	d.temp = int16(cfg.TemperatureC * 16)
	d.sample = w.chip.AddDeadline(d.onSample)
	d.check = w.chip.AddDeadline(d.onResetCheck)
	d.action = w.chip.AddDeadline(d.onPresence)
	d.readSlot = w.chip.AddDeadline(d.onReadSlot)
	d.release = w.chip.AddDeadline(d.onRelease)
	w.OnEdge(d.onEdge)
	w.chip.AddResetHook(d.onReset)
	return d
}

// TemperatureC returns the programmed temperature.
func (d *DS18B20) TemperatureC() float64 { return float64(d.temp) / 16 }

// SetTemperature changes the temperature returned by Convert T.
func (d *DS18B20) SetTemperature(c float64) { d.temp = int16(c * 16) }

func (d *DS18B20) onReset() {
	d.state = dsIdle
	d.bitIndex, d.searchSlot, d.searchEdge, d.matchIndex = 0, 0, 0, 0
	d.byteVal = 0
	d.read = d.read[:0]
	d.reset = false
	d.w.Release()
}

func (d *DS18B20) onEdge(level bool) {
	now := d.w.Now()
	if !level {
		d.fallAt = now
		d.reset = false
		d.check.Schedule(now + 480*uint64(rp2350.CyclesPerMicrosecond))
		switch d.state {
		case dsCommand, dsMatch:
			d.sample.Schedule(now + 15*uint64(rp2350.CyclesPerMicrosecond))
		case dsRead:
			d.readSlot.Schedule(now + 2*uint64(rp2350.CyclesPerMicrosecond))
		case dsSearch:
			// Search slots come in triples: true bit, complement, then the
			// master's selection write. Only the first two are read slots.
			if d.searchEdge%3 != 2 {
				d.readSlot.Schedule(now + 2*uint64(rp2350.CyclesPerMicrosecond))
			}
			d.searchEdge++
		}
		return
	}
	d.check.Cancel()
	if now-d.fallAt >= 480*uint64(rp2350.CyclesPerMicrosecond) {
		d.reset = true
		d.startPresence()
	}
}

func (d *DS18B20) onResetCheck() {
	if !d.w.Level() {
		d.reset = true
	}
}

func (d *DS18B20) startPresence() {
	d.state = dsCommand
	d.bitIndex = 0
	d.byteVal = 0
	d.searchSlot = 0
	d.searchEdge = 0
	d.matchIndex = 0
	d.w.Release()
	d.action.Schedule(d.w.Now() + 30*uint64(rp2350.CyclesPerMicrosecond))
}

func (d *DS18B20) onPresence() {
	// The presence pulse drives low for 120 us, then releases and waits for
	// the ROM command.
	if d.w.Level() {
		d.w.DriveLow()
		d.action.Schedule(d.w.Now() + 120*uint64(rp2350.CyclesPerMicrosecond))
		return
	}
	d.w.Release()
	d.reset = false
	d.state = dsCommand
}

func (d *DS18B20) onReadSlot() { d.readSlotBit() }

func (d *DS18B20) onRelease() {
	d.w.Release()
}

func (d *DS18B20) onSample() {
	if d.reset || (d.state != dsCommand && d.state != dsMatch) ||
		d.w.Now()-d.fallAt > 120*uint64(rp2350.CyclesPerMicrosecond) {
		return
	}
	bit := byte(0)
	if d.w.Level() {
		bit = 1
	}
	d.byteVal |= bit << uint(d.bitIndex)
	d.bitIndex++
	if d.bitIndex < 8 {
		return
	}
	cmd := d.byteVal
	d.bitIndex = 0
	d.byteVal = 0
	if d.state == dsMatch {
		// Match ROM: eight address bytes follow before the next command.
		d.matchIndex++
		if d.matchIndex == 8 {
			d.state = dsCommand
		}
		return
	}
	d.command(cmd)
}

func (d *DS18B20) command(cmd byte) {
	switch cmd {
	case 0xCC: // Skip ROM
		// The next eight write slots are another command.
	case 0x33: // Read ROM
		d.beginRead(d.rom())
	case 0x55: // Match ROM
		d.state = dsMatch
		d.matchIndex = 0
	case 0xF0: // Search ROM
		d.read = append(d.read[:0], d.rom()...)
		d.searchSlot = 0
		d.searchEdge = 0
		d.state = dsSearch
	case 0xBE: // Read scratchpad
		if d.cfg.OnRead != nil {
			d.cfg.OnRead(d.TemperatureC())
		}
		d.beginRead(d.scratchpad())
	case 0x44: // Convert T
		d.state = dsIdle
	default:
		d.state = dsIdle
	}
}

func (d *DS18B20) beginRead(data []byte) {
	d.read = append(d.read[:0], data...)
	d.readByte = 0
	d.readBit = 0
	d.state = dsRead
}

// readSlotBit drives a zero bit low; the release deadline ends the slot.
func (d *DS18B20) readSlotBit() {
	if d.reset {
		return
	}
	if d.state == dsSearch {
		if d.searchSlot >= 128 {
			d.state = dsCommand
			d.w.Release()
			return
		}
		bit := (d.read[(d.searchSlot/2)/8] >> uint((d.searchSlot/2)%8)) & 1
		if d.searchSlot%2 == 1 {
			bit ^= 1 // the search reads the bit then its complement
		}
		if bit == 0 {
			d.w.DriveLow()
		}
		d.release.Schedule(d.w.Now() + 30*uint64(rp2350.CyclesPerMicrosecond))
		d.searchSlot++
		return
	}
	if d.state != dsRead {
		return
	}
	if d.readByte >= len(d.read) {
		d.state = dsIdle
		d.w.Release()
		return
	}
	bit := (d.read[d.readByte] >> uint(d.readBit)) & 1
	if bit == 0 {
		d.w.DriveLow()
	}
	d.release.Schedule(d.w.Now() + 30*uint64(rp2350.CyclesPerMicrosecond))
	d.readBit++
	if d.readBit == 8 {
		d.readBit = 0
		d.readByte++
	}
}

func (d *DS18B20) rom() []byte {
	rom := make([]byte, 8)
	rom[0] = 0x28
	copy(rom[1:7], d.cfg.Serial[:])
	rom[7] = crc8(rom[:7])
	return rom
}

func (d *DS18B20) scratchpad() []byte {
	s := make([]byte, 9)
	s[0] = byte(d.temp)
	s[1] = byte(uint16(d.temp) >> 8)
	s[2] = d.th
	s[3] = d.tl
	s[4] = d.conf
	s[5], s[6], s[7] = 0xFF, 0xFF, 0xFF
	s[8] = crc8(s[:8])
	return s
}

func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc ^= b
		for range 8 {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8C
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
