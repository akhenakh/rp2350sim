package devices

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func setExt(c *rp2350.Chip, pin int, level bool) {
	c.Pins.SetExternal(pin, level)
}

func clockPulse(c *rp2350.Chip, d *AudioDecoder, pin int) {
	setExt(c, pin, false)
	setExt(c, pin, true)
}

func TestSampleFromBits(t *testing.T) {
	cases := []struct {
		v    uint32
		bits int
		want int16
	}{
		{0x1234, 16, 0x1234},
		{0xEDCC, 16, -4660},
		{0x123456, 24, 0x1234},
		{0xEDCCBA, 24, -4660}, // sign extension of a negative 24-bit slot
		{0x12345678, 32, 0x1234},
		{0xEDCCBA98, 32, -4660},
		{0x07FF, 12, 32752},  // 12-bit slot scaled into the top bits
		{0x0800, 12, -32768}, // the 12-bit sign bit
	}
	for _, c := range cases {
		if got := sampleFromBits(c.v, c.bits); got != c.want {
			t.Errorf("sampleFromBits(%#x, %d) = %d, want %d", c.v, c.bits, got, c.want)
		}
	}
}

func TestAudioI2S(t *testing.T) {
	c := rp2350.New()
	d := NewAudio(c, AudioConfig{Kind: AudioI2S, BCLK: 0, LRCLK: 1, Data: 2, Bits: 16, Delay: 1, Rate: 48000})
	// The first frame starts with LRCLK low; it must still get the delay bit.
	send := func(ws bool, sample uint16) {
		setExt(c, 1, ws)
		setExt(c, 0, false)
		// Philips I2S: one delay bit after the WS edge.
		setExt(c, 2, false)
		clockPulse(c, d, 0)
		for i := 15; i >= 0; i-- {
			setExt(c, 2, sample&(1<<uint(i)) != 0)
			clockPulse(c, d, 0)
		}
	}
	send(false, 0x1234)
	send(true, 0xEDCC)

	got := d.Samples()
	if len(got) != 2 || got[0] != 0x1234 || got[1] != -4660 {
		t.Fatalf("I2S samples = %v", got)
	}
}

func TestAudioPDM(t *testing.T) {
	c := rp2350.New()
	d := NewAudio(c, AudioConfig{Kind: AudioPDM, Data: 0, Clock: 1, Decimation: 8, Rate: 48000})
	for i := range 8 {
		setExt(c, 0, i < 6) // 75% ones
		clockPulse(c, d, 1)
	}
	got := d.Samples()
	if len(got) != 1 {
		t.Fatalf("PDM samples = %d", len(got))
	}
	if got[0] < 16000 || got[0] > 16600 {
		t.Fatalf("PDM sample = %d, want ~16383", got[0])
	}
}

func TestAudioPWM(t *testing.T) {
	c := rp2350.New()
	d := NewAudio(c, AudioConfig{Kind: AudioPWM, Pin: 0, Rate: 1000})
	c.CPU0.Sleeping = true
	c.CPU1.Sleeping = true
	window := int(d.pwmWindow)
	setExt(c, 0, true)
	c.Run(uint64(window * 3 / 4))
	setExt(c, 0, false)
	c.Run(uint64(window - window*3/4))
	got := d.Samples()
	if len(got) != 1 {
		t.Fatalf("PWM samples = %d", len(got))
	}
	if got[0] < 16000 || got[0] > 16600 {
		t.Fatalf("PWM sample = %d, want ~16383", got[0])
	}
}

func TestAudioWAVHeader(t *testing.T) {
	c := rp2350.New()
	d := NewAudio(c, AudioConfig{Kind: AudioPDM, Data: 0, Clock: 1, Decimation: 2, Rate: 8000, Channels: 1})
	for i := range 4 {
		setExt(c, 0, i%2 == 0)
		clockPulse(c, d, 1)
	}
	path := filepath.Join(t.TempDir(), "out.wav")
	if err := d.WriteWAV(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("bad WAV magic: % x", data[:12])
	}
	if got := binary.LittleEndian.Uint16(data[22:]); got != 1 {
		t.Fatalf("channels = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[24:]); got != 8000 {
		t.Fatalf("rate = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[28:]); got != 16000 {
		t.Fatalf("byte rate = %d", got)
	}
	if got := binary.LittleEndian.Uint16(data[32:]); got != 2 {
		t.Fatalf("block align = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[40:]); got != uint32(len(data)-44) {
		t.Fatalf("data size = %d, file %d", got, len(data))
	}
}
