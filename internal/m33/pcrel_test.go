package m33

import "testing"

// TestADRWHalfwordAlignedPC guards the ADR.W form of ADDW/SUBW: with Rn=15 the
// base is Align(PC,4), not the raw pc+4 value used for ordinary data
// processing. An instruction at a 2 mod 4 address otherwise materialises an
// address two bytes too high.
func TestADRWHalfwordAlignedPC(t *testing.T) {
	tests := []struct {
		name string
		hw1  uint16
		hw2  uint16
		want uint32
	}{
		{"addw", 0xF20F, 0x0064, 0x168}, // adr.w r0, #100 at 0x102
		{"subw", 0xF2AF, 0x0064, 0x0A0}, // subw r0, pc, #100 at 0x102
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bus := newTestBusAt(0, 1<<16)
			put16(t, bus, 0x102, tc.hw1, tc.hw2)
			c := New(bus)
			stepAt(t, c, 0x102)
			if c.R[0] != tc.want {
				t.Fatalf("r0 = %#x, want %#x", c.R[0], tc.want)
			}
		})
	}
}

// TestLDRDLiteralHalfwordAlignedPC checks the literal LDRD base. The buggy
// path read from pc+4 (0x106) instead of Align(PC,4) (0x104).
func TestLDRDLiteralHalfwordAlignedPC(t *testing.T) {
	bus := newTestBusAt(0, 1<<16)
	put16(t, bus, 0x102, 0xE9DF, 0x0102) // ldrd r0, r1, [pc, #8]
	// Fill a ten-byte window so the aligned read at 0x10c and the unaligned
	// read at 0x10e produce different values despite overlapping.
	copy(bus.mem[0x10C:], []byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55, 0xAA, 0xBB})
	c := New(bus)
	stepAt(t, c, 0x102)
	if c.R[0] != 0x11223344 || c.R[1] != 0x55667788 {
		t.Fatalf("ldrd = %#x %#x, want 0x11223344 0x55667788", c.R[0], c.R[1])
	}
}
