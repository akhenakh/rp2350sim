package rp2350

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The emulator recognises a simple, documented flash partition table at the
// base of flash. The picobin/picotool table is a boot block with hash/signature
// items; this format keeps the same A/B idea without the signed-boot state.
//
//	0x000  magic   0x50543235 ("PT25")
//	0x004  version 1
//	0x008  count   number of entries (<=16)
//	0x00c  active  preferred entry, 0 = choose automatically
//	0x010  reserved[16]
//	0x020  entries[16], 16 bytes each:
//	        0x0 offset  bytes from the flash base
//	        0x4 size    bytes
//	        0x8 flags   bit 0 executable, bit 1 B slot of an A/B pair
//	        0xc version higher wins within a pair
const (
	partitionMagic    = 0x50543235
	partitionHeader   = 0x20
	partitionEntryLen = 16
	partitionMax      = 16
	partitionExe      = 1 << 0
	partitionB        = 1 << 1
)

// Partition is one flash slot.
type Partition struct {
	Offset  uint32
	Size    uint32
	Flags   uint32
	Version uint32
}

// Bootable reports whether the slot contains executable firmware.
func (p Partition) Bootable() bool { return p.Flags&partitionExe != 0 }

// PartitionTable is the parsed table.
type PartitionTable struct {
	Active  int
	Entries []Partition
}

// ParsePartitionTable reads the table from the first flash sector. It returns
// nil when the magic is absent or the table is malformed.
func ParsePartitionTable(flash []byte) *PartitionTable {
	if len(flash) < partitionHeader || binary.LittleEndian.Uint32(flash) != partitionMagic {
		return nil
	}
	if binary.LittleEndian.Uint32(flash[4:]) != 1 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(flash[8:]))
	if n <= 0 || n > partitionMax || len(flash) < partitionHeader+n*partitionEntryLen {
		return nil
	}
	t := &PartitionTable{Active: int(binary.LittleEndian.Uint32(flash[12:])), Entries: make([]Partition, n)}
	for i := range n {
		off := partitionHeader + i*partitionEntryLen
		e := flash[off : off+partitionEntryLen]
		p := Partition{
			Offset:  binary.LittleEndian.Uint32(e),
			Size:    binary.LittleEndian.Uint32(e[4:]),
			Flags:   binary.LittleEndian.Uint32(e[8:]),
			Version: binary.LittleEndian.Uint32(e[12:]),
		}
		// A slot must lie inside the image. Reject the whole table otherwise:
		// selecting an out-of-range slot must never index past the flash array.
		// A zero-size entry is an unused slot; a bootable one needs at least
		// the eight vector bytes.
		if p.Size != 0 {
			end := uint64(p.Offset) + uint64(p.Size)
			if end > uint64(len(flash)) || uint64(p.Offset) >= uint64(len(flash)) {
				return nil
			}
		}
		if p.Bootable() && p.Size < 8 {
			return nil
		}
		t.Entries[i] = p
	}
	return t
}

// BootVector returns the eight vector bytes the bootrom would execute from a
// raw flash image, following a partition table when one is present. It is the
// single validation path for both the runner and the UI.
func BootVector(flash []byte) ([]byte, error) {
	if len(flash) < 8 {
		return nil, errors.New("flash image is too small to hold a vector table")
	}
	off := uint64(0)
	if binary.LittleEndian.Uint32(flash) == partitionMagic {
		t := ParsePartitionTable(flash)
		if t == nil {
			return nil, errors.New("malformed flash partition table")
		}
		idx := t.BootPartition(-1)
		if idx < 0 {
			return nil, errors.New("flash partition table has no bootable slot")
		}
		off = uint64(t.Entries[idx].Offset)
	}
	if off+8 > uint64(len(flash)) {
		return nil, fmt.Errorf("flash vector at %#x is outside the image", off)
	}
	return append([]byte(nil), flash[off:off+8]...), nil
}

// BootPartition picks the slot the bootrom would run: an explicit active or
// preferred slot, otherwise the highest-version executable entry.
func (t *PartitionTable) BootPartition(preferred int) int {
	if preferred >= 0 && preferred < len(t.Entries) && t.Entries[preferred].Bootable() {
		return preferred
	}
	if t.Active > 0 && t.Active <= len(t.Entries) && t.Entries[t.Active-1].Bootable() {
		return t.Active - 1
	}
	best := -1
	for i, e := range t.Entries {
		if !e.Bootable() {
			continue
		}
		if best < 0 || e.Version > t.Entries[best].Version {
			best = i
		}
	}
	return best
}

// Find returns the partition containing a storage offset.
func (t *PartitionTable) Find(off uint32) int {
	for i, e := range t.Entries {
		if off >= e.Offset && off < e.Offset+e.Size {
			return i
		}
	}
	return -1
}
