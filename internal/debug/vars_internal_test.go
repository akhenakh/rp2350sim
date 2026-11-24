package debug

import (
	"encoding/binary"
	"testing"
)

func TestVariablesAtIncludesEveryGlobal(t *testing.T) {
	info := &Info{
		Functions: []Function{{Name: "current", Addr: 0x1000, File: "/build/current.c"}},
		globals: []Global{
			{Name: "other_cu", Type: "int", File: "/build/other.c", Addr: 0x2000, Size: 4},
			{Name: "symbol_only", Type: "object", Addr: 0x2004, Size: 4},
			{Name: "constant", Const: new(uint64)},
		},
	}
	*info.globals[2].Const = 42
	read := func(addr uint32, size int) ([]byte, bool) {
		out := make([]byte, size)
		switch addr {
		case 0x2000:
			binary.LittleEndian.PutUint32(out, 0x12345678)
		case 0x2004:
			binary.LittleEndian.PutUint32(out, 0xabcdef01)
		default:
			return nil, false
		}
		return out, true
	}
	vars := info.VariablesAt(0x1000, [16]uint32{}, read)
	want := map[string]string{
		"other_cu":    "0x12345678",
		"symbol_only": "0xabcdef01",
		"constant":    "0x2a",
	}
	for name, value := range want {
		found := false
		for _, v := range vars {
			if v.Name == name && v.Scope == "global" && v.Available && v.Value == value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s missing or wrong in %+v", name, vars)
		}
	}
}
