package main

import (
	"encoding/binary"
	"testing"
)

func pcmWAV(samples ...int16) []byte {
	out := make([]byte, 44+len(samples)*2)
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(samples)*2))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], 48000)
	binary.LittleEndian.PutUint32(out[28:], 96000)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(samples)*2))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(out[44+i*2:], uint16(v))
	}
	return out
}

func TestSafePlaybackWAVRemovesDCAndAttenuates(t *testing.T) {
	in := pcmWAV(1000, 3000)
	out := safePlaybackWAV(in)
	if &out[0] == &in[0] {
		t.Fatal("safePlaybackWAV must not modify the saved capture")
	}
	got := []int16{
		int16(binary.LittleEndian.Uint16(out[44:])),
		int16(binary.LittleEndian.Uint16(out[46:])),
	}
	// mean 2000, then *0.2: (-1000, 1000) -> (-200, 200).
	if got[0] != -200 || got[1] != 200 {
		t.Fatalf("playback samples = %v, want [-200 200]", got)
	}
}

func TestSafePlaybackWAVHeadroom(t *testing.T) {
	in := pcmWAV(0, 32767, -32768, 32767)
	out := safePlaybackWAV(in)
	for i := 0; i < 4; i++ {
		v := int(binary.LittleEndian.Uint16(out[44+i*2:]))
		if v&0x8000 != 0 {
			v -= 65536
		}
		if v > 32767/2 || v < -32768/2 {
			t.Fatalf("sample %d = %d is above the playback headroom", i, v)
		}
	}
}

func TestSafePlaybackWAVShort(t *testing.T) {
	if got := safePlaybackWAV([]byte("RIFF")); string(got) != "RIFF" {
		t.Fatalf("short input changed: %q", got)
	}
}
