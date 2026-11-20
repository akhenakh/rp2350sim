package debug

import "testing"

func TestEvalAddressBregSigned(t *testing.T) {
	regs := [16]uint32{0x1000}
	// DW_OP_breg0 with SLEB128 -4 must subtract, not add 124.
	addr, isAddr, ok := evalAddress([]byte{opBreg0, 0x7c}, &regs, nil)
	if !ok || !isAddr || addr != 0x0ffc {
		t.Fatalf("breg0 = %#x isAddr=%v ok=%v, want 0xffc", addr, isAddr, ok)
	}
}

func TestEvalAddressRegisterBounds(t *testing.T) {
	regs := [16]uint32{0x1000}
	for _, expr := range [][]byte{
		{opReg0 + 31},        // DW_OP_reg31
		{opBreg0 + 31, 0x00}, // DW_OP_breg31
		{opBregX, 16, 0x00},  // DW_OP_bregx r16
		{opRegX, 16},         // DW_OP_regx r16
	} {
		if _, _, ok := evalAddress(expr, &regs, nil); ok {
			t.Errorf("expr %x evaluated an out-of-range register", expr)
		}
	}
}

func TestEvalAddressSkip(t *testing.T) {
	// DW_OP_skip +2 jumps over two bytes to DW_OP_lit0.
	addr, isAddr, ok := evalAddress([]byte{opSkip, 0x02, 0x00, 0xff, 0xff, opLit0}, nil, nil)
	if !ok || isAddr || addr != 0 {
		t.Fatalf("skip = %#x isAddr=%v ok=%v, want 0", addr, isAddr, ok)
	}
}
