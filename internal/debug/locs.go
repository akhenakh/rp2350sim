package debug

import (
	"debug/elf"
	"encoding/binary"
)

// readDebugLoc returns a relocated copy of .debug_loc. DWARF4 location lists
// in TinyGo images carry R_ARM_ABS32 relocations against .text; Section.Data
// does not apply them.
func readDebugLoc(f *elf.File) []byte {
	sec := f.Section(".debug_loc")
	if sec == nil {
		return nil
	}
	data, err := sec.Data()
	if err != nil {
		return nil
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
		if symIdx >= len(syms) {
			continue
		}
		add := uint64(binary.LittleEndian.Uint32(data[r.Off:]))
		var val uint64
		switch typ {
		case elf.R_ARM_ABS32:
			val = syms[symIdx].Value + add
		case elf.R_ARM_REL32:
			val = syms[symIdx].Value + add
		default:
			continue
		}
		binary.LittleEndian.PutUint32(data[r.Off:], uint32(val))
	}
	return data
}
