package debug

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"testing"
)

// TestReadDebugLocKeepsLinkedAddresses guards the linked-image path: LLD emits
// .rel.debug_loc records for an executable, but the section already holds the
// final addresses. Re-applying them split the section base (0x10000000) and
// broke every location list.
func TestReadDebugLocKeepsLinkedAddresses(t *testing.T) {
	const path = "../../build/debugvars.elf"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not built", path)
	}
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syms, err := f.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	var want uint32
	for _, s := range syms {
		if s.Name == "main.marker" {
			want = uint32(s.Value) &^ 1
			break
		}
	}
	if want == 0 {
		t.Fatal("main.marker not found")
	}
	data := readDebugLoc(f)
	for i := 0; i+8 <= len(data); i += 4 {
		if binary.LittleEndian.Uint32(data[i:]) != ^uint32(0) {
			continue
		}
		base := binary.LittleEndian.Uint32(data[i+4:])
		if base == want {
			return
		}
		if base == want+0x10000000 {
			t.Fatalf("base %#x was relocated twice", base)
		}
	}
	t.Fatalf("base %#x not found in .debug_loc", want)
}
