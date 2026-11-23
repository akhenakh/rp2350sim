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

// TestVCVTIntBits checks that float-to-integer conversions write the integer
// bit pattern, not an IEEE-754 float re-encoding of the integer.
func TestVCVTIntBits(t *testing.T) {
	cases := []struct {
		name string
		hw1  uint16
		hw2  uint16
		in   float32
		want uint32
	}{
		{"vcvt.s32.f32", 0xEEBD, 0x0AE0, 3.7, 3},
		{"vcvt.s32.f32 negative", 0xEEBD, 0x0AE0, -3.7, 0xFFFFFFFD},
		{"vcvt.u32.f32", 0xEEBC, 0x0AE0, 3.7, 3},
	}
	for _, tc := range cases {
		cpu := New(newTestBus())
		cpu.S[1] = math.Float32bits(tc.in)
		cpu.exec32Coproc(tc.hw1, tc.hw2)
		if cpu.Halted {
			t.Fatalf("%s halted: %s", tc.name, cpu.HaltReason)
		}
		if got := cpu.S[0]; got != tc.want {
			t.Errorf("%s = %#x, want %#x", tc.name, got, tc.want)
		}
	}
}

// TestVFPMultiLoadStore covers non-writeback VLDMIA/VSTMIA. Their W=0,
// L=1/0 encoding overlaps the single-register VLDR/VSTR pattern; P=0 is what
// distinguishes the block forms.
func TestVFPMultiLoadStore(t *testing.T) {
	cpu := New(newExecBus())
	cpu.R[0] = 0x20000400
	for i := range 4 {
		cpu.Bus.Write32(cpu.R[0]+uint32(i*4), math.Float32bits(float32(i+1)))
	}
	cpu.exec32Coproc(0xEC90, 0x0A04) // vldmia r0, {s0-s3}
	if cpu.Halted {
		t.Fatalf("vldmia halted: %s", cpu.HaltReason)
	}
	for i := range 4 {
		if got := math.Float32frombits(cpu.S[i]); got != float32(i+1) {
			t.Fatalf("s%d = %v, want %v", i, got, float32(i+1))
		}
	}
	if cpu.R[0] != 0x20000400 {
		t.Fatalf("non-writeback vldmia changed r0 to %#x", cpu.R[0])
	}

	cpu.R[0] = 0x20000500
	for i := range 4 {
		cpu.S[i] = math.Float32bits(float32(i + 10))
	}
	cpu.exec32Coproc(0xEC80, 0x0A04) // vstmia r0, {s0-s3}
	for i := range 4 {
		got := math.Float32frombits(cpu.Bus.Read32(cpu.R[0] + uint32(i*4)))
		if got != float32(i+10) {
			t.Fatalf("stored s%d = %v, want %v", i, got, float32(i+10))
		}
	}
}

// TestVFPDoubleRegisterBounds checks that an undefined D16-D31 encoding halts
// the guest instead of panicking the host on S[32].
func TestVFPDoubleRegisterBounds(t *testing.T) {
	cpu := New(newExecBus())
	cpu.R[0] = 0x20000400
	cpu.exec32Coproc(0xEDD0, 0x0B00) // vldr d16, [r0]
	if !cpu.Halted {
		t.Fatal("undefined D16 load did not halt")
	}
	if cpu.HaltReason != "undefined VFP double register" {
		t.Fatalf("halt reason = %q", cpu.HaltReason)
	}
}
