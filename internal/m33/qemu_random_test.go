//go:build qemu

package m33

import (
	"math/rand"
	"testing"
)

// TestQEMURandomLongMultiply feeds random operands through the long-multiply
// and MLA/MLS group. Fixed corpus patterns can miss data-dependent paths such
// as the UMAAL carry; random accumulators and products exercise them against
// QEMU.
func TestQEMURandomLongMultiply(t *testing.T) {
	ops := []struct {
		name string
		hw1  uint16
		hw2  uint16
	}{
		{"umull", 0xFBA4, 0x3701}, // umull r3, r7, r4, r1
		{"umlal", 0xFBE4, 0x3701}, // umlal r3, r7, r4, r1
		{"umaal", 0xFBE4, 0x3761}, // umaal r3, r7, r4, r1
		{"smull", 0xFB84, 0x3701}, // smull r3, r7, r4, r1
		{"smlal", 0xFBC4, 0x3701}, // smlal r3, r7, r4, r1
		{"mla", 0xFB04, 0x7301},   // mla r3, r4, r1, r7
		{"mls", 0xFB04, 0x7311},   // mls r3, r4, r1, r7
	}
	rng := rand.New(rand.NewSource(1))
	cases := make([]diffCase, 0, len(ops)*24)
	for _, op := range ops {
		for range 24 {
			var regs [13]uint32
			for r := range regs {
				regs[r] = rng.Uint32()
			}
			cases = append(cases, diffCase{
				name:  op.name,
				hw:    []uint16{op.hw1, op.hw2},
				regs:  regs,
				cpsr:  rng.Uint32() & 0xF0000000,
				steps: 1,
			})
		}
	}

	results := runOracle(t, cases)
	for i := range cases {
		if _, diff := compareOracleCase(cases[i], results[i]); len(diff) != 0 {
			t.Fatalf("%s regs=%v: %v", cases[i].name, cases[i].regs, diff)
		}
	}
}
