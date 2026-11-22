// Command wavcheck asserts basic properties of a PCM WAV file. It is the
// audio counterpart of pngcheck used by the smoke suite.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
)

func main() {
	rate := flag.Int("rate", 0, "required sample rate (0 = any)")
	channels := flag.Int("channels", 0, "required channel count (0 = any)")
	minRMS := flag.Float64("min-rms", 0, "minimum RMS in 0..1")
	freq := flag.Float64("freq", 0, "expected fundamental in Hz (0 = skip)")
	tol := flag.Float64("freq-tol", 20, "frequency tolerance in Hz")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: wavcheck [options] file.wav")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	info, samples, err := parseWAV(data)
	if err != nil {
		fail(err)
	}
	if *rate != 0 && info.rate != *rate {
		failf("rate %d, want %d", info.rate, *rate)
	}
	if *channels != 0 && info.channels != *channels {
		failf("channels %d, want %d", info.channels, *channels)
	}
	if len(samples) == 0 {
		failf("no samples")
	}
	var sum float64
	for _, v := range samples {
		f := float64(v) / 32768
		sum += f * f
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	if rms < *minRMS {
		failf("RMS %.4f, want >= %.4f", rms, *minRMS)
	}
	measured := 0.0
	if *freq > 0 {
		measured = fundamental(samples, info.rate)
		if math.Abs(measured-*freq) > *tol {
			failf("fundamental %.1f Hz, want %.1f +/- %.1f", measured, *freq, *tol)
		}
	}
	fmt.Printf("wavcheck: %dx%d %d Hz %.2fs rms=%.4f", info.channels, 16, info.rate, float64(len(samples))/float64(info.rate*info.channels), rms)
	if *freq > 0 {
		fmt.Printf(" freq=%.1f", measured)
	}
	fmt.Println()
}

type wavInfo struct {
	rate     int
	channels int
}

func parseWAV(data []byte) (wavInfo, []int16, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return wavInfo{}, nil, fmt.Errorf("not a RIFF/WAVE file")
	}
	var info wavInfo
	var raw []byte
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		n := int(binary.LittleEndian.Uint32(data[off+4:]))
		body := off + 8
		if body+n > len(data) {
			return info, nil, fmt.Errorf("truncated %q chunk", id)
		}
		switch id {
		case "fmt ":
			if n < 16 {
				return info, nil, fmt.Errorf("short fmt chunk")
			}
			if format := binary.LittleEndian.Uint16(data[body:]); format != 1 {
				return info, nil, fmt.Errorf("format %d, want PCM", format)
			}
			info.channels = int(binary.LittleEndian.Uint16(data[body+2:]))
			info.rate = int(binary.LittleEndian.Uint32(data[body+4:]))
			if bits := binary.LittleEndian.Uint16(data[body+14:]); bits != 16 {
				return info, nil, fmt.Errorf("bits %d, want 16", bits)
			}
		case "data":
			raw = data[body : body+n]
		}
		off = body + n
		if n&1 != 0 {
			off++
		}
	}
	if info.channels == 0 || info.rate == 0 {
		return info, nil, fmt.Errorf("missing fmt chunk")
	}
	if len(raw)%2 != 0 {
		raw = raw[:len(raw)-1]
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return info, samples, nil
}

// fundamental estimates the tone by counting positive-going zero crossings
// with a small hysteresis.
func fundamental(samples []int16, rate int) float64 {
	if len(samples) < 2 || rate <= 0 {
		return 0
	}
	mean := 0.0
	for _, v := range samples {
		mean += float64(v)
	}
	mean /= float64(len(samples))
	hysteresis := 0.02 * 32768
	crossings := 0
	above := false
	first := -1
	last := -1
	for i, v := range samples {
		f := float64(v) - mean
		if !above && f > hysteresis {
			above = true
			if first < 0 {
				first = i
			} else {
				last = i
			}
			crossings++
		} else if above && f < -hysteresis {
			above = false
		}
	}
	if crossings < 2 || first < 0 || last <= first {
		return 0
	}
	period := float64(last-first) / float64(crossings-1)
	if period <= 0 {
		return 0
	}
	return float64(rate) / period
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wavcheck:", err)
	os.Exit(1)
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wavcheck: "+format+"\n", args...)
	os.Exit(1)
}
