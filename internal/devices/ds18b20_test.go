package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

const oneWirePin = 6

func runUS(c *rp2350.Chip, us uint64) {
	c.Run(us * uint64(rp2350.CyclesPerMicrosecond))
}

func oneWireMaster(c *rp2350.Chip) {
	// PADS_BANK0: pull-up enable + input enable, isolation released.
	c.Write32(0x40038004+uint32(oneWirePin)*4, 1<<3|1<<6)
}

const (
	sioOutClr = 0xd0000020
	sioOeSet  = 0xd0000038
	sioOeClr  = 0xd0000040
)

func masterLow(c *rp2350.Chip, us uint64) {
	bit := uint32(1) << oneWirePin
	c.Write32(sioOutClr, bit)
	c.Write32(sioOeSet, bit)
	runUS(c, us)
}

func resetPulse(c *rp2350.Chip) {
	masterLow(c, 500)
	c.Write32(sioOeClr, 1<<oneWirePin)
	runUS(c, 50)
	if c.Pins.Level(oneWirePin) {
		panic("ds18b20: no presence pulse")
	}
	runUS(c, 180)
	if !c.Pins.Level(oneWirePin) {
		panic("ds18b20: presence did not release")
	}
}

func writeBit(c *rp2350.Chip, bit byte) {
	if bit == 0 {
		masterLow(c, 60)
		c.Write32(sioOeClr, 1<<oneWirePin)
		runUS(c, 10)
		return
	}
	masterLow(c, 5)
	c.Write32(sioOeClr, 1<<oneWirePin)
	runUS(c, 65)
}

func writeByte(c *rp2350.Chip, v byte) {
	for i := range 8 {
		writeBit(c, (v>>uint(i))&1)
	}
}

func readBit(c *rp2350.Chip) byte {
	masterLow(c, 2)
	c.Write32(sioOeClr, 1<<oneWirePin)
	runUS(c, 13)
	bit := byte(0)
	if c.Pins.Level(oneWirePin) {
		bit = 1
	}
	runUS(c, 20)
	return bit
}

func readByte(c *rp2350.Chip) byte {
	var v byte
	for i := range 8 {
		v |= readBit(c) << uint(i)
	}
	return v
}

func TestDS18B20Scratchpad(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	oneWireMaster(c)
	NewDS18B20(NewWire(c, oneWirePin), DS18B20Config{TemperatureC: 22.5})

	resetPulse(c)
	writeByte(c, 0xCC)
	writeByte(c, 0xBE)

	got := make([]byte, 9)
	for i := range got {
		got[i] = readByte(c)
	}
	want := []byte{0x68, 0x01, 0x4B, 0x46, 0x7F, 0xFF, 0xFF, 0xFF}
	want = append(want, crc8(want))
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scratchpad = % X, want % X", got, want)
		}
	}
	if crc8(got[:8]) != got[8] {
		t.Fatalf("scratchpad CRC = %#x, want %#x", got[8], crc8(got[:8]))
	}
}

func TestDS18B20SearchAndMatch(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Parked = true
	oneWireMaster(c)
	serial := [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	NewDS18B20(NewWire(c, oneWirePin), DS18B20Config{TemperatureC: 22.5, Serial: serial})

	rom := []byte{0x28}
	rom = append(rom, serial[:]...)
	rom = append(rom, crc8(rom))
	_ = rom

	resetPulse(c)
	writeByte(c, 0xF0) // Search ROM
	for i := range 64 {
		bit := (rom[i/8] >> uint(i%8)) & 1
		a := readBit(c)
		b := readBit(c)
		if a != bit || b != 1-bit {
			t.Fatalf("search bit %d = (%d,%d), want (%d,%d)", i, a, b, bit, 1-bit)
		}
		writeBit(c, bit)
	}

	resetPulse(c)
	writeByte(c, 0x55) // Match ROM
	for _, b := range rom {
		writeByte(c, b)
	}
	writeByte(c, 0xBE) // Read scratchpad

	got := make([]byte, 9)
	for i := range got {
		got[i] = readByte(c)
	}
	if got[0] != 0x68 || got[1] != 0x01 {
		t.Fatalf("matched device scratchpad = % X", got)
	}
}
