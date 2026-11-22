package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var (
	audioMsg    string
	audioErr    bool
	audioCmd    *exec.Cmd
	audioTmpWAV string
)

// playbackGain is applied only to host playback, never to the saved WAV. A
// decoded GPIO/PWM square wave is full scale (0 dBFS), and a pin parked high
// decodes as DC; either can be loud enough to damage speakers or hearing when
// played through a system mixer at normal volume.
const playbackGain = 0.2

// safePlaybackWAV removes the DC offset and applies playbackGain to a PCM16
// WAV capture before it is handed to a host player.
func safePlaybackWAV(wav []byte) []byte {
	out := append([]byte(nil), wav...)
	if len(out) <= 44 {
		return out
	}
	raw := out[44:]
	n := len(raw) / 2
	if n == 0 {
		return out
	}
	var sum float64
	for i := range n {
		sum += float64(int16(binary.LittleEndian.Uint16(raw[i*2:])))
	}
	mean := sum / float64(n)
	for i := range n {
		v := (float64(int16(binary.LittleEndian.Uint16(raw[i*2:]))) - mean) * playbackGain
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(int16(v)))
	}
	return out
}

func audioPanel(snap MachineState) {
	wave := emu.AudioWave(2048)
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("Audio", FontSize(16), FontWeight(700))
			if snap.AudioRate > 0 {
				secs := 0.0
				if snap.AudioRate > 0 {
					secs = float64(snap.AudioSamples) / float64(snap.AudioRate*snap.AudioChannels)
				}
				Label(fmt.Sprintf("%d Hz · %d ch · %d samples · %.2fs",
					snap.AudioRate, snap.AudioChannels, snap.AudioSamples, secs), FontSize(12))
			}
			Filler(1)
			if Button(NoIcon, "Save WAV") {
				name := "audio-" + time.Now().Format("20060102-150405") + ".wav"
				if err := emu.SaveAudio(name); err != nil {
					audioMsg, audioErr = err.Error(), true
				} else {
					abs, _ := filepath.Abs(name)
					audioMsg, audioErr = "saved "+abs, false
				}
				RequestNextFrame()
			}
			if Button(NoIcon, "Play") {
				if err := playAudio(emu.AudioWAV()); err != nil {
					audioMsg, audioErr = err.Error(), true
				} else {
					audioMsg, audioErr = "playing", false
				}
				RequestNextFrame()
			}
			if Button(NoIcon, "Stop") {
				stopAudio()
				audioMsg, audioErr = "stopped", false
				RequestNextFrame()
			}
		})
		if audioMsg != "" {
			c := TextColor(140, 45, 72, 1)
			if audioErr {
				c = TextColor(0, 65, 60, 1)
			}
			Label(audioMsg, FontSize(11), FontWeight(600), c)
		}
		Label("Playback is attenuated and DC-blocked: decoded square waves are full scale and can damage speakers at system volume.",
			FontSize(11), TextColor(220, 6, 62, 1))
		Container(Attrs(Grow(1), Expand, Clip), func() {
			w := int(GetContentWidth())
			if w < 200 {
				w = 1100
			}
			img := audioImage(wave, w, 360)
			id := UseImage("audio", img)
			ImageViewAt(id, Vec2{float32(w), 360})
		})
	})
}

func audioImage(samples []int16, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{0x16, 0x10, 0x1d, 0xff}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bg.R, bg.G, bg.B, 0xff
	}
	mid := h / 2
	for x := range w {
		img.Set(x, mid, color.RGBA{0x30, 0x2a, 0x44, 0xff})
	}
	if len(samples) == 0 || w < 2 {
		return img
	}
	line := color.RGBA{0x6e, 0xe7, 0xb7, 0xff}
	prev := mid
	for x := range w {
		idx := int(float64(x) * float64(len(samples)-1) / float64(w-1))
		v := int(float64(samples[idx]) / 32768 * float64(mid-8))
		y := max(mid-v, 1)
		if y >= h-1 {
			y = h - 2
		}
		if x > 0 {
			drawVLine(img, x, prev, y, line)
		}
		prev = y
	}
	return img
}

func drawVLine(img *image.RGBA, x, y0, y1 int, c color.RGBA) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		img.Set(x, y, c)
	}
}

// playAudio writes the capture to a temporary WAV and starts a host player.
// No cgo or audio library dependency: the player is an ordinary process.
func playAudio(wav []byte) error {
	stopAudio()
	if len(wav) <= 44 {
		return fmt.Errorf("no audio captured yet")
	}
	wav = safePlaybackWAV(wav)
	f, err := os.CreateTemp(os.TempDir(), "rp2350-*.wav")
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err := f.Write(wav); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return err
	}
	_ = f.Close()
	candidates := [][]string{
		{"paplay", name},
		{"aplay", "-q", name},
		{"afplay", name},
		{"ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", name},
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		if err := cmd.Start(); err != nil {
			continue
		}
		audioCmd = cmd
		audioTmpWAV = name
		go func() {
			_ = cmd.Wait()
			_ = os.Remove(name)
		}()
		return nil
	}
	_ = os.Remove(name)
	return fmt.Errorf("no host audio player (paplay, aplay, afplay, ffplay)")
}

func stopAudio() {
	if audioCmd != nil && audioCmd.Process != nil {
		_ = audioCmd.Process.Kill()
	}
	audioCmd = nil
	if audioTmpWAV != "" {
		_ = os.Remove(audioTmpWAV)
		audioTmpWAV = ""
	}
}
