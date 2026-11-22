package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// newAudioDecoder builds the decoder selected by the three audio source flags.
// Exactly one source must be set.
func newAudioDecoder(c *rp2350.Chip, i2s, pdm string, pwmPin, rate int) (*devices.AudioDecoder, error) {
	set := 0
	if i2s != "" {
		set++
	}
	if pdm != "" {
		set++
	}
	if pwmPin >= 0 {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("select exactly one of -audio-i2s, -audio-pdm or -audio-pwm")
	}
	if rate < 1000 || rate > 384000 {
		return nil, fmt.Errorf("-audio-rate must be 1000..384000")
	}
	cfg := devices.AudioConfig{Rate: rate}
	if i2s != "" {
		parts := strings.Split(i2s, ",")
		if len(parts) < 3 || len(parts) > 4 {
			return nil, fmt.Errorf("-audio-i2s expects bclk,lrclk,data[,bits]")
		}
		nums, err := parseInts(parts)
		if err != nil {
			return nil, fmt.Errorf("-audio-i2s: %w", err)
		}
		cfg.Kind = devices.AudioI2S
		cfg.BCLK, cfg.LRCLK, cfg.Data = nums[0], nums[1], nums[2]
		if len(nums) == 4 {
			cfg.Bits = nums[3]
		}
		cfg.Delay = 1
		cfg.Channels = 2
		for _, pin := range []int{cfg.BCLK, cfg.LRCLK, cfg.Data} {
			if pin < 0 || pin > 47 {
				return nil, fmt.Errorf("audio pin %d is not a GPIO", pin)
			}
		}
	}
	if pdm != "" {
		parts := strings.Split(pdm, ",")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("-audio-pdm expects data,clock[,decimation]")
		}
		nums, err := parseInts(parts)
		if err != nil {
			return nil, fmt.Errorf("-audio-pdm: %w", err)
		}
		cfg.Kind = devices.AudioPDM
		cfg.Data, cfg.Clock = nums[0], nums[1]
		if len(nums) == 3 {
			cfg.Decimation = nums[2]
		}
		cfg.Channels = 1
		for _, pin := range []int{cfg.Data, cfg.Clock} {
			if pin < 0 || pin > 47 {
				return nil, fmt.Errorf("audio pin %d is not a GPIO", pin)
			}
		}
	}
	if pwmPin >= 0 {
		if pwmPin > 47 {
			return nil, fmt.Errorf("-audio-pwm must be a GPIO")
		}
		cfg.Kind = devices.AudioPWM
		cfg.Pin = pwmPin
		cfg.Channels = 1
	}
	return devices.NewAudio(c, cfg), nil
}

func parseInts(parts []string) ([]int, error) {
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}
