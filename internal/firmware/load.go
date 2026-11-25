// Package firmware loads RP2350 firmware images (UF2, ELF, raw binary).
package firmware

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	uf2MagicStart0    = 0x0A324655
	uf2MagicStart1    = 0x9E5D5157
	uf2MagicEnd       = 0x0AB16F30
	uf2FamilyRP2350   = 0xE48BFF59
	uf2FamilyAbsolute = 0xE48BFF57

	uf2FlagExtension    = 0x00008000
	uf2ExtensionIgnore  = 0x9957E304 // RP2350-E10 absolute block marker
	uf2ExtensionDataOff = 256

	firmwareFlashBase = 0x10000000
	firmwareFlashSize = 16 * 1024 * 1024
	firmwareSRAMBase  = 0x20000000
	firmwareSRAMSize  = 520 * 1024
	firmwareROMSize   = 32 * 1024
	maxSegmentSize    = firmwareFlashSize
)

// Image is a loaded firmware image.
type Image struct {
	// Segments maps a physical address to its bytes.
	Segments []Segment
	// Entry is the reset vector, when known.
	Entry uint32
	// SP is the initial stack pointer, when known.
	SP uint32
	// Format is "uf2", "elf" or "bin".
	Format string
}

// Segment is one contiguous block of image bytes.
type Segment struct {
	Addr uint32
	Data []byte
}

func validateSegment(addr uint32, size uint64) error {
	if size > maxSegmentSize {
		return fmt.Errorf("firmware segment size %d exceeds %d", size, maxSegmentSize)
	}
	end := uint64(addr) + size
	if end > 1<<32 {
		return fmt.Errorf("firmware segment at %#x wraps the 32-bit address space", addr)
	}
	inWindow := func(base, length uint64) bool {
		return uint64(addr) >= base && end <= base+length
	}
	switch {
	case inWindow(0, firmwareROMSize),
		inWindow(firmwareFlashBase, firmwareFlashSize),
		inWindow(firmwareSRAMBase, firmwareSRAMSize),
		inWindow(0x11000000, firmwareFlashSize),
		inWindow(0x15000000, firmwareFlashSize),
		inWindow(0x19000000, firmwareFlashSize),
		inWindow(0x1d000000, firmwareFlashSize):
		return nil
	default:
		return fmt.Errorf("firmware segment [%#x, %#x) is outside writable memory", addr, end)
	}
}

// ValidateSegments checks every segment's address, length and destination.
func (img *Image) ValidateSegments() error {
	for _, seg := range img.Segments {
		if err := validateSegment(seg.Addr, uint64(len(seg.Data))); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a firmware file, detecting the format.
func Load(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse("", data)
}

// Parse decodes a firmware image already held in memory (ELF, UF2 or a raw
// flash image). name is only used in error messages; the browser build uses it
// for uploaded files, where there is no filesystem to read from.
func Parse(name string, data []byte) (*Image, error) {
	img, err := parse(data)
	if err != nil && name != "" {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return img, err
}

func parse(data []byte) (*Image, error) {
	switch {
	case len(data) >= 4 && data[0] == 0x7F && data[1] == 'E' && data[2] == 'L' && data[3] == 'F':
		return loadELF(data)
	case len(data) >= 8 && binary.LittleEndian.Uint32(data[0:]) == uf2MagicStart0 &&
		binary.LittleEndian.Uint32(data[4:]) == uf2MagicStart1:
		return loadUF2(data)
	default:
		if len(data) > firmwareFlashSize {
			return nil, fmt.Errorf("firmware: raw image size %d exceeds flash size %d", len(data), firmwareFlashSize)
		}
		return &Image{Format: "bin", Segments: []Segment{{Addr: firmwareFlashBase, Data: data}}}, nil
	}
}

func loadUF2(data []byte) (*Image, error) {
	if len(data)%512 != 0 {
		return nil, fmt.Errorf("uf2: size %d is not a multiple of 512", len(data))
	}
	img := &Image{Format: "uf2"}
	for off := 0; off < len(data); off += 512 {
		blk := data[off : off+512]
		if binary.LittleEndian.Uint32(blk[0:]) != uf2MagicStart0 ||
			binary.LittleEndian.Uint32(blk[4:]) != uf2MagicStart1 ||
			binary.LittleEndian.Uint32(blk[508:]) != uf2MagicEnd {
			return nil, fmt.Errorf("uf2: bad magic in block at %#x", off)
		}
		flags := binary.LittleEndian.Uint32(blk[8:])
		addr := binary.LittleEndian.Uint32(blk[12:])
		payloadSize := binary.LittleEndian.Uint32(blk[16:])
		if flags&0x0001 != 0 { // not main flash
			continue
		}
		if payloadSize > 476 {
			return nil, fmt.Errorf("uf2: payload size %d too large", payloadSize)
		}
		// picotool's RP2350-E10 workaround prepends a 256-byte metadata block
		// tagged "ignore me". The real bootrom skips it; writing its 0xEF
		// payload would destroy the last flash sector.
		if flags&uf2FlagExtension != 0 && binary.LittleEndian.Uint32(blk[32+uf2ExtensionDataOff:]) == uf2ExtensionIgnore {
			continue
		}
		if flags&0x2000 != 0 {
			family := binary.LittleEndian.Uint32(blk[28:])
			switch family {
			case uf2FamilyRP2350:
			case uf2FamilyAbsolute:
				// Absolute metadata blocks carry no firmware payload.
				continue
			default:
				return nil, fmt.Errorf("uf2: unsupported family id %#08x (expected %#08x)", family, uf2FamilyRP2350)
			}
		}
		payload := blk[32 : 32+payloadSize]
		if err := validateSegment(addr, uint64(len(payload))); err != nil {
			return nil, fmt.Errorf("uf2: %w", err)
		}
		// Merge into the previous segment when contiguous, validating the
		// combined range so a chain of blocks cannot cross its destination.
		if n := len(img.Segments); n > 0 {
			last := &img.Segments[n-1]
			if uint64(last.Addr)+uint64(len(last.Data)) == uint64(addr) {
				if err := validateSegment(last.Addr, uint64(len(last.Data))+uint64(len(payload))); err != nil {
					return nil, fmt.Errorf("uf2: %w", err)
				}
				last.Data = append(last.Data, payload...)
				continue
			}
		}
		img.Segments = append(img.Segments, Segment{Addr: addr, Data: append([]byte(nil), payload...)})
	}
	if len(img.Segments) == 0 {
		return nil, fmt.Errorf("uf2: no data blocks")
	}
	return img, nil
}

func loadELF(data []byte) (*Image, error) {
	f, err := elf.NewFile(newReaderAt(data))
	if err != nil {
		return nil, fmt.Errorf("elf: %w", err)
	}
	img := &Image{Format: "elf"}
	if f.Entry != 0 {
		img.Entry = uint32(f.Entry)
	}
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD || p.Filesz == 0 {
			continue
		}
		if p.Filesz > maxSegmentSize {
			return nil, fmt.Errorf("elf: segment size %d exceeds %d", p.Filesz, maxSegmentSize)
		}
		if p.Paddr > uint64(^uint32(0)) {
			return nil, fmt.Errorf("elf: physical address %#x exceeds 32 bits", p.Paddr)
		}
		if p.Off > uint64(len(data)) || p.Filesz > uint64(len(data))-p.Off {
			return nil, fmt.Errorf("elf: segment at %#x extends past the file", p.Off)
		}
		addr := uint32(p.Paddr)
		if err := validateSegment(addr, p.Filesz); err != nil {
			return nil, fmt.Errorf("elf: %w", err)
		}
		buf := make([]byte, p.Filesz)
		if _, err := p.ReadAt(buf, 0); err != nil {
			return nil, fmt.Errorf("elf: read segment: %w", err)
		}
		img.Segments = append(img.Segments, Segment{Addr: addr, Data: buf})
	}
	return img, nil
}

// readerAt adapts a byte slice to io.ReaderAt without importing bytes.
type sliceReaderAt []byte

func (s sliceReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(s)) {
		return 0, io.EOF
	}
	n := copy(p, s[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func newReaderAt(b []byte) sliceReaderAt { return sliceReaderAt(b) }

// ValidateBoot checks that the image starts with the vector table the
// emulator's flash boot path expects. SRAM-resident and partition-table
// layouts are rejected instead of being executed as if they were a flat
// flash image.
func (img *Image) ValidateBoot(flashBase, flashSize, sramBase, sramSize uint32) error {
	if err := img.ValidateSegments(); err != nil {
		return err
	}
	sp, pc, ok := img.vector(flashBase)
	if !ok {
		return fmt.Errorf("firmware: no vector table at %#x (only flat flash images are supported)", flashBase)
	}
	if sp%4 != 0 || sp <= sramBase || sp > sramBase+sramSize {
		return fmt.Errorf("firmware: initial SP %#x is outside SRAM [%#x,%#x]", sp, sramBase, sramBase+sramSize)
	}
	entry := pc &^ 1
	inFlash := entry >= flashBase && entry < flashBase+flashSize
	inSRAM := entry >= sramBase && entry < sramBase+sramSize
	if !inFlash && !inSRAM {
		return fmt.Errorf("firmware: reset vector %#x is outside flash and SRAM", pc)
	}
	return nil
}

// vector returns the eight bytes at addr when a segment covers them.
func (img *Image) vector(addr uint32) (sp, pc uint32, ok bool) {
	for _, s := range img.Segments {
		start, end := uint64(s.Addr), uint64(s.Addr)+uint64(len(s.Data))
		if uint64(addr) < start || uint64(addr)+8 > end {
			continue
		}
		off := int(uint64(addr) - start)
		sp = binary.LittleEndian.Uint32(s.Data[off:])
		pc = binary.LittleEndian.Uint32(s.Data[off+4:])
		return sp, pc, true
	}
	return 0, 0, false
}

// MaxAddr returns the highest address written by the image.
func (img *Image) MaxAddr() uint32 {
	var max uint32
	for _, s := range img.Segments {
		if end := s.Addr + uint32(len(s.Data)); end > max {
			max = end
		}
	}
	return max
}

// Size returns the total number of bytes in the image.
func (img *Image) Size() int {
	n := 0
	for _, s := range img.Segments {
		n += len(s.Data)
	}
	return n
}
