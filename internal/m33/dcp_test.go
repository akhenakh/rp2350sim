package m33

import (
	"math"
	"testing"
)

func TestDCPClassifyUsesLoadedFormat(t *testing.T) {
	cases := []struct {
		name   string
		format dcpFormat
		value  float64
		want   uint32
	}{
		{"double_normal", dcpDouble, 1, 0},
		{"double_large_finite", dcpDouble, 1e300, 0},
		{"double_small_finite", dcpDouble, 1e-300, 0},
		{"double_infinity", dcpDouble, math.Inf(1), 2},
		{"double_nan", dcpDouble, math.NaN(), 1},
		{"double_zero", dcpDouble, 0, 4},
		{"float_normal", dcpFloat, 1, 0},
		{"float_infinity", dcpFloat, math.Inf(-1), 2},
		{"float_nan", dcpFloat, math.NaN(), 1},
		{"float_zero", dcpFloat, 0, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cpu := New(newTestBus())
			cpu.dcp.format = tc.format
			cpu.dcp.x = tc.value
			cpu.dcpClassify()
			if cpu.dcp.class != tc.want {
				t.Fatalf("class = %d, want %d", cpu.dcp.class, tc.want)
			}
		})
	}
}

func runDCPConversion(t *testing.T, value float64, rounded, unsigned bool) uint32 {
	t.Helper()
	cpu := New(newTestBus())
	cpu.dcp.x = value
	cpu.dcp.y = 0
	cpu.dcp.op = dcpAdd
	cpu.dcp.format = dcpDouble
	opc2 := uint16(2) // NTDC
	if rounded {
		opc2 = 3 // NRDC
	}
	cpu.dcpCDP(0x80, opc2<<5)
	readOpc2 := uint16(0) // RDIC
	if unsigned {
		readOpc2 = 1 // RDUC
	}
	cpu.dcpMRC(0, readOpc2<<5|3)
	if cpu.Halted {
		t.Fatalf("DCP conversion halted: %s", cpu.HaltReason)
	}
	return cpu.R[0]
}

func TestDCPConversionRoundingAndSaturation(t *testing.T) {
	cases := []struct {
		name     string
		value    float64
		rounded  bool
		unsigned bool
		want     uint32
	}{
		{"truncate_positive", 1.5, false, false, 1},
		{"round_positive", 1.5, true, false, 2},
		{"truncate_negative", -1.5, false, false, 0xFFFFFFFF},
		{"round_negative", -1.5, true, false, 0xFFFFFFFE},
		{"round_unsigned", 1.5, true, true, 2},
		{"signed_positive_overflow", 1e300, false, false, 0x7FFFFFFF},
		{"signed_negative_overflow", -1e300, false, false, 0x80000000},
		{"unsigned_positive_overflow", 1e300, false, true, 0xFFFFFFFF},
		{"unsigned_negative", -1, false, true, 0},
		{"signed_positive_infinity", math.Inf(1), false, false, 0x7FFFFFFF},
		{"signed_negative_infinity", math.Inf(-1), false, false, 0x80000000},
		{"unsigned_positive_infinity", math.Inf(1), false, true, 0xFFFFFFFF},
		{"unsigned_negative_infinity", math.Inf(-1), false, true, 0},
		{"signed_nan", math.NaN(), false, false, 0},
		{"unsigned_nan", math.NaN(), false, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runDCPConversion(t, tc.value, tc.rounded, tc.unsigned); got != tc.want {
				t.Fatalf("conversion = %#x, want %#x", got, tc.want)
			}
		})
	}
}
