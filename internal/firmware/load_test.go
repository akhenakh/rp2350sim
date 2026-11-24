package firmware

import (
	"encoding/binary"
	"os"
	"path/filepath"
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

// TestValidateBootVectors accepts a plausible flash image and rejects an
// image without a vector table.
func TestValidateBootVectors(t *testing.T) {
	img := &Image{Segments: []Segment{{Addr: 0x10000000, Data: []byte{
		0x00, 0x20, 0x08, 0x20, // SP = 0x20082000
		0x01, 0x01, 0x00, 0x10, // PC = 0x10000101
	}}}}
	if err := img.ValidateBoot(0x10000000, 16<<20, 0x20000000, 520<<10); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}

	empty := &Image{Segments: []Segment{{Addr: 0x20000000, Data: []byte{1}}}}
	if err := empty.ValidateBoot(0x10000000, 16<<20, 0x20000000, 520<<10); err == nil {
		t.Fatal("image without vectors was accepted")
	}

	bad := &Image{Segments: []Segment{{Addr: 0x10000000, Data: make([]byte, 8)}}}
	if err := bad.ValidateBoot(0x10000000, 16<<20, 0x20000000, 520<<10); err == nil {
		t.Fatal("zero stack pointer was accepted")
	}
}

func TestLoadRejectsOversizedRawImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.bin")
	if err := os.WriteFile(path, make([]byte, firmwareFlashSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("raw image larger than flash was accepted")
	}
}

func elfSegmentImage(paddr, filesz uint32, includeData bool) []byte {
	const headerSize = 52
	const programHeaderSize = 32
	data := make([]byte, headerSize+programHeaderSize)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 1, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)  // ET_EXEC
	binary.LittleEndian.PutUint16(data[18:], 40) // EM_ARM
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint32(data[28:], headerSize)
	binary.LittleEndian.PutUint16(data[40:], headerSize)
	binary.LittleEndian.PutUint16(data[42:], programHeaderSize)
	binary.LittleEndian.PutUint16(data[44:], 1)
	ph := data[headerSize:]
	binary.LittleEndian.PutUint32(ph[0:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(ph[4:], uint32(len(data)))
	binary.LittleEndian.PutUint32(ph[8:], paddr)
	binary.LittleEndian.PutUint32(ph[12:], paddr)
	binary.LittleEndian.PutUint32(ph[16:], filesz)
	binary.LittleEndian.PutUint32(ph[20:], filesz)
	binary.LittleEndian.PutUint32(ph[24:], 5) // PF_R | PF_X
	binary.LittleEndian.PutUint32(ph[28:], 0x1000)
	if includeData {
		data = append(data, make([]byte, filesz)...)
	}
	return data
}

func TestLoadELFRejectsInvalidSegments(t *testing.T) {
	tests := []struct {
		name   string
		paddr  uint32
		filesz uint32
	}{
		{name: "oversized", paddr: firmwareFlashBase, filesz: firmwareFlashSize + 1},
		{name: "outside flash", paddr: firmwareFlashBase + firmwareFlashSize - 1, filesz: 4},
		{name: "address overflow", paddr: ^uint32(0) - 1, filesz: 4},
		{name: "unmapped destination", paddr: 0x30000000, filesz: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadELF(elfSegmentImage(tt.paddr, tt.filesz, tt.filesz <= 1024)); err == nil {
				t.Fatal("invalid ELF segment was accepted")
			}
		})
	}
}

func TestLoadUF2RejectsInvalidSegments(t *testing.T) {
	tests := []struct {
		name string
		addr uint32
		data []byte
	}{
		{name: "past flash", addr: firmwareFlashBase + firmwareFlashSize - 1, data: []byte{1, 2}},
		{name: "address overflow", addr: ^uint32(0), data: []byte{1, 2}},
		{name: "unmapped destination", addr: 0x30000000, data: []byte{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blk := uf2Block(0x2000, tt.addr, tt.data, uf2FamilyRP2350)
			if _, err := loadUF2(blk); err == nil {
				t.Fatal("invalid UF2 segment was accepted")
			}
		})
	}
}

func TestValidateSegmentsRejectsOverflow(t *testing.T) {
	img := &Image{Segments: []Segment{{Addr: ^uint32(0), Data: []byte{1, 2}}}}
	if err := img.ValidateSegments(); err == nil {
		t.Fatal("wrapping segment was accepted")
	}
}
