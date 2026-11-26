package regmap

import (
	"strings"
	"testing"
)

func TestDescribePLLLock(t *testing.T) {
	peek := func(addr uint32) (uint32, bool) {
		switch addr {
		case 0x40050004: // PLL_SYS_PWR: PD/VCOPD/POSTDIVPD/DSMPD set
			return 0x2d, true
		}
		return 0, false
	}
	out := Describe(0x40050000, 0, peek)
	for _, want := range []string{"PLL_SYS", "CS", "LOCK", "PWR", "PD", "bit31", "cannot set"} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe = %q, missing %q", out, want)
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, _, _, _, ok := Lookup(0x60000000); ok {
		t.Fatal("unexpected substitution for an unknown peripheral")
	}
}
