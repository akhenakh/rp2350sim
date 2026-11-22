package main

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestNewAudioDecoderSources(t *testing.T) {
	c := rp2350.New()
	cases := []struct {
		name string
		i2s  string
		pdm  string
		pwm  int
		ok   bool
	}{
		{"i2s", "4,5,6", "", -1, true},
		{"i2s 24-bit", "4,5,6,24", "", -1, true},
		{"pdm", "", "6,7", -1, true},
		{"pdm decimation", "", "6,7,32", -1, true},
		{"pwm", "", "", 0, true},
		{"pwm 47", "", "", 47, true},
		{"none", "", "", -1, false},
		{"two sources", "4,5,6", "", 0, false},
		{"i2s fields", "4,5", "", -1, false},
		{"i2s pin", "4,5,48", "", -1, false},
		{"pdm fields", "", "6", -1, false},
		{"pwm pin", "", "", 48, false},
	}
	for _, tc := range cases {
		dec, err := newAudioDecoder(c, tc.i2s, tc.pdm, tc.pwm, 48000)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
		if tc.ok && dec == nil {
			t.Errorf("%s: nil decoder", tc.name)
		}
	}
}

func TestNewAudioDecoderDefaults(t *testing.T) {
	c := rp2350.New()
	_, err := newAudioDecoder(c, "4,5,6", "", -1, 0)
	if err == nil {
		t.Fatal("rate 0 should be rejected")
	}
	dec, err := newAudioDecoder(c, "4,5,6,24", "", -1, 44100)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dec.Config()
	if cfg.Kind != devices.AudioI2S || cfg.Bits != 24 || cfg.Channels != 2 || cfg.Delay != 1 || cfg.Rate != 44100 {
		t.Fatalf("config = %+v", cfg)
	}
}
