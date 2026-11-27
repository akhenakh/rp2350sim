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

func TestVFPExceptionFlagsAndVMSR(t *testing.T) {
	cpu := New(newTestBus())
	cpu.S[1] = math.Float32bits(1)
	cpu.S[2] = 0
	cpu.exec32Coproc(0xEE80, 0x0A81) // vdiv.f32 s0, s1, s2
	if !math.IsInf(float64(math.Float32frombits(cpu.S[0])), 1) {
		t.Fatalf("1/0 = %v, want +Inf", math.Float32frombits(cpu.S[0]))
	}
	if cpu.FPSCR&fpscrDZC == 0 {
		t.Fatal("VDIV 1/0 did not set DZC")
	}

	cpu.S[1] = math.Float32bits(1)
	cpu.S[2] = math.Float32bits(float32(math.Ldexp(1, -24))) // lost when added to 1
	cpu.exec32Coproc(0xEE30, 0x0A81)                         // vadd.f32 s0, s1, s2
	if cpu.S[0] != math.Float32bits(1) || cpu.FPSCR&(fpscrDZC|fpscrIXC) != fpscrDZC|fpscrIXC {
		t.Fatalf("cumulative inexact VADD flags: result=%#x FPSCR=%#x", cpu.S[0], cpu.FPSCR)
	}

	cpu.S[2] = 0x01000001            // 16777217 is not exactly representable as float32
	cpu.exec32Coproc(0xEEB8, 0x0AC1) // vcvt.f32.s32 s0, s2
	if cpu.FPSCR&fpscrIXC == 0 {
		t.Fatal("inexact integer-to-float conversion did not set IXC")
	}

	cpu.R[0] = fpscrN | fpscrDZC | fpscrIXC
	cpu.exec32Coproc(0xEEE1, 0x0A10) // vmsr fpscr, r0
	if got := cpu.FPSCR & (fpscrN | fpscrDZC | fpscrIXC); got != fpscrN|fpscrDZC|fpscrIXC {
		t.Fatalf("VMSR FPSCR = %#x, want cumulative flags retained", got)
	}
	cpu.R[0] = 0
	cpu.exec32Coproc(0xEEE1, 0x0A10) // vmsr fpscr, r0
	if cpu.FPSCR != 0 {
		t.Fatalf("VMSR zero left FPSCR=%#x", cpu.FPSCR)
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
	cpu.R[0] = 0x20000600
	for i := range 4 {
		cpu.Bus.Write32(cpu.R[0]+uint32(i*4), math.Float32bits(float32(i+20)))
	}
	cpu.exec32Coproc(0xECB0, 0x0A04) // vldmia r0!, {s0-s3}
	if cpu.R[0] != 0x20000610 {
		t.Fatalf("writeback vldmia left r0 at %#x, want 0x20000610", cpu.R[0])
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

// TestVFPDoubleMoveUndefined checks that VMOV.F64 Dd, Dm halts even for
// D0-D15: FPv5-SP has no double-precision data processing, and a D16-D31
// encoding used to index past S[31] and panic the host.
func TestVFPDoubleMoveUndefined(t *testing.T) {
	for _, hw2 := range []uint16{0x0B41, 0x5B61} { // vmov.f64 d0,d1 / d5,d17
		cpu := New(newExecBus())
		cpu.exec32Coproc(0xEEB0, hw2)
		if !cpu.Halted {
			t.Fatalf("%04x: undefined VMOV.F64 did not halt", hw2)
		}
		if cpu.HaltReason != "undefined VFP double register" {
			t.Fatalf("%04x: halt reason = %q", hw2, cpu.HaltReason)
		}
	}
}

// TestVFPBlockOddStart guards the VMOV Dm,Rt,Rt2 dispatch mask: the
// non-writeback VSTMIA/VLDMIA forms starting at an odd single register
// (ECC0/ECD0) used to be mistaken for the double core transfer.
func TestVFPBlockOddStart(t *testing.T) {
	cpu := New(newExecBus())
	cpu.R[0] = 0x20000500
	cpu.S[1] = math.Float32bits(11)
	cpu.S[2] = math.Float32bits(22)
	cpu.exec32Coproc(0xECC0, 0x0A02) // vstmia r0, {s1-s2}
	if cpu.Halted {
		t.Fatalf("vstmia odd start halted: %s", cpu.HaltReason)
	}
	if cpu.R[0] != 0x20000500 {
		t.Fatalf("non-writeback vstmia changed r0 to %#x", cpu.R[0])
	}
	if got := cpu.Bus.Read32(0x20000500); got != math.Float32bits(11) {
		t.Fatalf("store s1 = %#x", got)
	}
	if got := cpu.Bus.Read32(0x20000504); got != math.Float32bits(22) {
		t.Fatalf("store s2 = %#x", got)
	}
	cpu.S[1], cpu.S[2] = 0, 0
	cpu.exec32Coproc(0xECD0, 0x0A02) // vldmia r0, {s1-s2}
	if cpu.Halted {
		t.Fatalf("vldmia odd start halted: %s", cpu.HaltReason)
	}
	if math.Float32frombits(cpu.S[1]) != 11 || math.Float32frombits(cpu.S[2]) != 22 {
		t.Fatalf("load odd start = %v,%v", math.Float32frombits(cpu.S[1]), math.Float32frombits(cpu.S[2]))
	}
}

// TestVFPVMOVDoubleStillWorks checks the corrected mask still matches
// EC4x/EC5x.
func TestVFPVMOVDoubleStillWorks(t *testing.T) {
	cpu := New(newTestBus())
	cpu.R[2], cpu.R[3] = 0x11223344, 0x55667788
	cpu.exec32Coproc(0xEC43, 0x2B15) // vmov d5, r2, r3
	if cpu.Halted {
		t.Fatalf("vmov d5 halted: %s", cpu.HaltReason)
	}
	if cpu.S[10] != 0x11223344 || cpu.S[11] != 0x55667788 {
		t.Fatalf("d5 = %#x %#x", cpu.S[10], cpu.S[11])
	}
	cpu.R[2], cpu.R[3] = 0, 0
	cpu.exec32Coproc(0xEC53, 0x2B15) // vmov r2, r3, d5
	if cpu.R[2] != 0x11223344 || cpu.R[3] != 0x55667788 {
		t.Fatalf("vmov read = %#x %#x", cpu.R[2], cpu.R[3])
	}
}
