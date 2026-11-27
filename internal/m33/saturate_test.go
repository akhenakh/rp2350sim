package m33

import "testing"

func run32Regs(t *testing.T, hw1, hw2 uint16, setup func(*CPU)) *CPU {
	t.Helper()
	bus := newTestBus()
	put16(t, bus, 0, hw1, hw2)
	put16(t, bus, 4, 0xBF00)
	c := New(bus)
	c.R[15] = 0
	setup(c)
	c.Step()
	return c
}

// TestSSATUSAT covers the signed and unsigned saturating forms, both shift
// directions and Q-flag accumulation.
func TestSSATUSAT(t *testing.T) {
	tests := []struct {
		name string
		hw1  uint16
		hw2  uint16
		in   uint32
		want uint32
		q    bool
	}{
		{"ssat positive clamp", 0xF301, 0x0004, 100, 15, true},
		{"ssat negative clamp", 0xF301, 0x0004, 0xFFFFFF9C, 0xFFFFFFF0, true},
		{"ssat in range", 0xF301, 0x0004, 7, 7, false},
		{"ssat lsl", 0xF301, 0x00C4, 2, 15, true},                   // 2<<3 = 16 -> 15
		{"ssat asr", 0xF321, 0x00C4, 0xFFFFFFC0, 0xFFFFFFF8, false}, // -64>>3
		{"ssat full range", 0xF301, 0x001F, 0x80000000, 0x80000000, false},
		{"usat negative", 0xF381, 0x0014, 0xFFFFFFFB, 0, true},
		{"usat in range", 0xF381, 0x0014, 1234, 1234, false},
		{"usat positive clamp", 0xF381, 0x0014, 0x100000, 0xFFFFF, true},
		{"usat asr clamp", 0xF3A1, 0x0088, 0x400, 0xFF, true}, // 1024>>2 -> 255
		{"usat zero", 0xF381, 0x0000, 5, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := run32Regs(t, tc.hw1, tc.hw2, func(c *CPU) { c.R[1] = tc.in })
			if c.Halted {
				t.Fatalf("halted: %s", c.HaltReason)
			}
			if c.R[0] != tc.want {
				t.Fatalf("r0 = %#x, want %#x", c.R[0], tc.want)
			}
			if got := c.APSR&flagQ != 0; got != tc.q {
				t.Fatalf("Q = %v, want %v", got, tc.q)
			}
		})
	}
}

// TestSELHalts: SEL needs APSR.GE, which the DSP group does not model. It must
// fail loudly rather than silently selecting Rm.
func TestSELHalts(t *testing.T) {
	c := run32(t, 0xFAA1, 0xF082) // sel r0, r1, r2
	if !c.Halted {
		t.Fatal("SEL did not halt")
	}
}
