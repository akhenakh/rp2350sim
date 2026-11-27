package debug

import (
	"debug/elf"
	"encoding/binary"
)

// readDebugLoc returns the .debug_loc section with its relocations applied.
//
// A linked image already contains the final addresses: the linker resolves
// .debug_loc relocations, and the .rel records kept by --emit-relocs are
// informational. Applying them again adds the section base a second time, so
// linked images are returned unchanged. Relocatable objects still need the
// R_ARM_ABS32/REL32 records applied.
func readDebugLoc(f *elf.File) []byte {
	sec := f.Section(".debug_loc")
	if sec == nil {
		return nil
	}
	data, err := sec.Data()
	if err != nil {
		return nil
	}
	if f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN {
		return data
	}
	relSec := f.Section(".rel.debug_loc")
	if relSec == nil {
		return data
	}
	relData, err := relSec.Data()
	if err != nil {
		return data
	}
	syms, err := f.Symbols()
	if err != nil {
		return data
	}
	for off := 0; off+8 <= len(relData); off += 8 {
		r := elf.Rel32{
			Off:  binary.LittleEndian.Uint32(relData[off:]),
			Info: binary.LittleEndian.Uint32(relData[off+4:]),
		}
		if int(r.Off)+4 > len(data) {
			continue
		}
		symIdx := int(r.Info >> 8)
		typ := elf.R_ARM(r.Info & 0xff)
		// Symbols() omits the null symbol at index 0, so an ELF symbol
		// index addresses syms[index-1].
		if symIdx < 1 || symIdx > len(syms) {
			continue
		}
		sym := syms[symIdx-1]
		add := uint64(binary.LittleEndian.Uint32(data[r.Off:]))
		var val uint64
		switch typ {
		case elf.R_ARM_ABS32:
			val = sym.Value + add
		case elf.R_ARM_REL32:
			val = sym.Value + add
		default:
			continue
		}
		binary.LittleEndian.PutUint32(data[r.Off:], uint32(val))
	}
	return data
}
