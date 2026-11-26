package m33

import "testing"

// TestCorpusCoversLongMultiplyVariants guards the differential corpus against
// missing instruction variants. UMLAL and UMAAL share the 0xFBE0 group and
// differ in hw2[7:4]; before UMAAL was added, the corpus exercised only UMLAL
// and the QEMU comparison could not see the decoder running UMAAL as UMLAL.
func TestCorpusCoversLongMultiplyVariants(t *testing.T) {
	type variant struct {
		name string
		hw1  uint16
		op   uint16
	}
	want := []variant{
		{"umull", 0xFBA0, 0},
		{"umlal", 0xFBE0, 0},
		{"umaal", 0xFBE0, 0x60},
		{"smull", 0xFB80, 0},
		{"smlal", 0xFBC0, 0},
	}
	have := make(map[[2]uint16]bool)
	for _, c := range instructionCorpus() {
		if len(c.hw) < 2 {
			continue
		}
		have[[2]uint16{c.hw[0] & 0xFFF0, c.hw[1] & 0x00F0}] = true
	}
	for _, w := range want {
		if !have[[2]uint16{w.hw1, w.op}] {
			t.Errorf("corpus has no %s case (group %#04x, op %#x)", w.name, w.hw1, w.op)
		}
	}
}
