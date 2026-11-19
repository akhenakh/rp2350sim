package firmware

import (
	"encoding/binary"
	"testing"
)

func uf2Block(flags, addr uint32, payload []byte, family uint32) []byte {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint32(b[0:], uf2MagicStart0)
	binary.LittleEndian.PutUint32(b[4:], uf2MagicStart1)
	binary.LittleEndian.PutUint32(b[8:], flags)
	binary.LittleEndian.PutUint32(b[12:], addr)
	binary.LittleEndian.PutUint32(b[16:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(b[28:], family)
	copy(b[32:], payload)
	binary.LittleEndian.PutUint32(b[508:], uf2MagicEnd)
	return b
}

// TestUF2IgnoreBlock checks the RP2350-E10 absolute block is skipped, not
// written into the last flash sector.
func TestUF2IgnoreBlock(t *testing.T) {
	meta := uf2Block(0x2000|0x8000, 0x10ffff00, make([]byte, 256), uf2FamilyAbsolute)
	for i := range 256 {
		meta[32+i] = 0xEF
	}
	binary.LittleEndian.PutUint32(meta[32+256:], uf2ExtensionIgnore)
	data := uf2Block(0x2000, 0x10000000, []byte{1, 2, 3, 4}, uf2FamilyRP2350)
	img, err := loadUF2(append(meta, data...))
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Segments) != 1 || img.Segments[0].Addr != 0x10000000 {
		t.Fatalf("segments = %+v", img.Segments)
	}
	if string(img.Segments[0].Data) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("data = %v", img.Segments[0].Data)
	}
}

// TestUF2AbsoluteFamily checks an unprefixed absolute metadata block is
// ignored, while a normal data block still loads.
func TestUF2AbsoluteFamily(t *testing.T) {
	meta := uf2Block(0x2000, 0x10ffff00, make([]byte, 256), uf2FamilyAbsolute)
	data := uf2Block(0x2000, 0x10000000, []byte{9, 9}, uf2FamilyRP2350)
	img, err := loadUF2(append(meta, data...))
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Segments) != 1 || img.Segments[0].Addr != 0x10000000 {
		t.Fatalf("segments = %+v", img.Segments)
	}
}

// TestUF2RejectsOtherFamilies rejects RP2040 and RISC-V images rather than
// executing them as Arm.
func TestUF2RejectsOtherFamilies(t *testing.T) {
	for _, family := range []uint32{0xE48BFF56, 0xE48BFF5A, 0xE48BFF5B} {
		data := uf2Block(0x2000, 0x10000000, []byte{1}, family)
		if _, err := loadUF2(data); err == nil {
			t.Fatalf("family %#x was accepted", family)
		}
	}
}

// TestUF2NotMainFlash checks the flag is honoured before any address check.
func TestUF2NotMainFlash(t *testing.T) {
	blk := uf2Block(0x2000|0x0001, 0x20000000, []byte{1, 2}, uf2FamilyRP2350)
	img, err := loadUF2(blk)
	if err == nil {
		t.Fatalf("image with only a not-main-flash block loaded: %+v", img)
	}
}
