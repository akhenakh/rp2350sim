package devices

import "math"

// UARTFrame is one decoded 8N1 byte.
type UARTFrame struct {
	T    uint64
	Data byte
}

// DecodeUART decodes an 8N1 UART stream from one trace channel. The line is
// idle high; each frame starts on a falling edge and is sampled at the middle
// of every bit. A frame with a low stop bit is dropped.
func DecodeUART(samples []TraceSample, ch int, baud, sampleHz float64) []UARTFrame {
	if baud <= 0 || sampleHz <= 0 {
		return nil
	}
	spb := sampleHz / baud // samples per bit
	if spb < 2 || len(samples) < 2 {
		return nil
	}
	var frames []UARTFrame
	// at samples the level a number of bit periods after the start edge.
	at := func(start int, bits float64) (bool, int) {
		idx := start + int(math.Round(bits*spb))
		if idx < 0 || idx >= len(samples) {
			return true, idx
		}
		return samples[idx].Bit(ch), idx
	}
	for i := 1; i < len(samples); i++ {
		falling := samples[i-1].Bit(ch) && !samples[i].Bit(ch)
		if !falling {
			continue
		}
		// The start bit must be low at its middle, otherwise it is noise.
		if high, _ := at(i, 0.5); high {
			continue
		}
		var b byte
		last := i
		for n := range 8 {
			v, idx := at(i, 1.5+float64(n))
			if v {
				b |= 1 << uint(n)
			}
			last = idx
		}
		stop, stopIdx := at(i, 9.5)
		if stop {
			frames = append(frames, UARTFrame{T: samples[i].T, Data: b})
			if stopIdx > i {
				i = stopIdx
			} else {
				i = last
			}
		}
	}
	return frames
}

// I2CEventKind identifies one decoded I2C event.
type I2CEventKind int

const (
	I2CStart I2CEventKind = iota
	I2CAddr
	I2CData
	I2CAck
	I2CStop
)

// I2CEvent is a start/stop condition, address, data byte or acknowledge bit.
type I2CEvent struct {
	T    uint64
	Kind I2CEventKind
	Byte byte
	Read bool
	Ack  bool
}

// DecodeI2C decodes the wire protocol from SDA/SCL: START and STOP conditions,
// 8-bit address/data bytes and the 9th acknowledge bit.
func DecodeI2C(samples []TraceSample, sda, scl int) []I2CEvent {
	if len(samples) == 0 {
		return nil
	}
	var events []I2CEvent
	inFrame, first := false, true
	bits := 0
	var cur byte
	lastSDA, lastSCL := samples[0].Bit(sda), samples[0].Bit(scl)
	for i := 1; i < len(samples); i++ {
		sdaV, sclV := samples[i].Bit(sda), samples[i].Bit(scl)
		if sclV && lastSCL {
			if lastSDA && !sdaV { // START / repeated START
				events = append(events, I2CEvent{T: samples[i].T, Kind: I2CStart})
				inFrame, first, bits, cur = true, true, 0, 0
			} else if !lastSDA && sdaV { // STOP
				events = append(events, I2CEvent{T: samples[i].T, Kind: I2CStop})
				inFrame = false
			}
		}
		if inFrame && sclV && !lastSCL {
			if bits < 8 {
				cur = cur << 1
				if sdaV {
					cur |= 1
				}
			}
			bits++
			if bits == 9 {
				if first {
					events = append(events, I2CEvent{T: samples[i].T, Kind: I2CAddr, Byte: cur, Read: cur&1 != 0})
					first = false
				} else {
					events = append(events, I2CEvent{T: samples[i].T, Kind: I2CData, Byte: cur})
				}
				events = append(events, I2CEvent{T: samples[i].T, Kind: I2CAck, Ack: !sdaV})
				bits, cur = 0, 0
			}
		}
		lastSDA, lastSCL = sdaV, sclV
	}
	return events
}

// SPIFrame is one decoded 8-bit SPI transfer.
type SPIFrame struct {
	T    uint64
	MOSI byte
	MISO byte
}

// DecodeSPI decodes mode-0 SPI (CPOL=0, CPHA=0) on the rising SCK edge while
// CS is low. Pass miso < 0 when the line is not connected, and cs < 0 to ignore
// chip select.
func DecodeSPI(samples []TraceSample, sck, mosi, miso, cs int) []SPIFrame {
	if len(samples) == 0 {
		return nil
	}
	var frames []SPIFrame
	var mo, mi byte
	bits := 0
	active := cs < 0 || !samples[0].Bit(cs)
	lastSCK := samples[0].Bit(sck)
	if cs >= 0 {
		lastCS := samples[0].Bit(cs)
		for i := 1; i < len(samples); i++ {
			if v := samples[i].Bit(cs); v != lastCS {
				lastCS = v
				active = !v
				if !active {
					bits = 0
				}
			}
			sckV := samples[i].Bit(sck)
			if active && sckV && !lastSCK {
				mo = mo<<1 | b2u8(samples[i].Bit(mosi))
				if miso >= 0 {
					mi = mi<<1 | b2u8(samples[i].Bit(miso))
				}
				bits++
				if bits == 8 {
					frames = append(frames, SPIFrame{T: samples[i].T, MOSI: mo, MISO: mi})
					bits, mo, mi = 0, 0, 0
				}
			}
			lastSCK = sckV
		}
		return frames
	}
	for i := 1; i < len(samples); i++ {
		sckV := samples[i].Bit(sck)
		if sckV && !lastSCK {
			mo = mo<<1 | b2u8(samples[i].Bit(mosi))
			if miso >= 0 {
				mi = mi<<1 | b2u8(samples[i].Bit(miso))
			}
			bits++
			if bits == 8 {
				frames = append(frames, SPIFrame{T: samples[i].T, MOSI: mo, MISO: mi})
				bits, mo, mi = 0, 0, 0
			}
		}
		lastSCK = sckV
	}
	return frames
}

func b2u8(v bool) byte {
	if v {
		return 1
	}
	return 0
}
