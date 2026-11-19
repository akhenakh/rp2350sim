package m33

import (
	"math"
	"testing"
)

// TestVFPFused covers the fused multiply-add forms emitted by optimized C
// code (the Waveshare FatFs demo uses vfma.f32 in its time/bench paths).
func TestVFPFused(t *testing.T) {
	cases := []struct {
		name string
		hw1  uint16
		hw2  uint16
		want float32
	}{
		{"vfma", 0xEEA0, 0x0A81, 7},   // 1 + 2*3
		{"vfms", 0xEEA0, 0x0AC1, -5},  // 1 - 2*3
		{"vfnma", 0xEE90, 0x0AC1, -7}, // -(1 + 2*3)
		{"vfnms", 0xEE90, 0x0A81, 5},  // -(1 - 2*3)
	}
	for _, tc := range cases {
		cpu := New(newTestBus())
		cpu.S[0] = math.Float32bits(1)
		cpu.S[1] = math.Float32bits(2)
		cpu.S[2] = math.Float32bits(3)
		cpu.exec32Coproc(tc.hw1, tc.hw2)
		if got := math.Float32frombits(cpu.S[0]); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}
