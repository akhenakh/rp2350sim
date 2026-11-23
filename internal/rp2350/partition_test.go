package rp2350

import (
	"encoding/binary"
	"testing"
)

// buildPartitionFlash lays a table with two executable slots and a vector
// table in each slot.
func buildPartitionFlash(t *testing.T, active uint32) *Chip {
	t.Helper()
	c := New()
	put := func(off uint32, v uint32) { binary.LittleEndian.PutUint32(c.Flash[off:], v) }
	put(0, partitionMagic)
	put(4, 1)
	put(8, 2)
	put(12, active)
	// Slot A at 2 MiB, version 2.
	put(partitionHeader, 0x200000)
	put(partitionHeader+4, 0x100000)
	put(partitionHeader+8, partitionExe)
	put(partitionHeader+12, 2)
	// Slot B at 3 MiB, version 3.
	put(partitionHeader+16, 0x300000)
	put(partitionHeader+20, 0x100000)
	put(partitionHeader+24, partitionExe|partitionB)
	put(partitionHeader+28, 3)
	// Vector tables: SP in SRAM, reset vector in the slot's runtime window.
	put(0x200000, 0x20001000)
	put(0x200004, 0x10001201)
	put(0x300000, 0x20002000)
	put(0x300004, 0x10003401)
	return c
}

func TestPartitionBootHighestVersion(t *testing.T) {
	c := buildPartitionFlash(t, 0)
	c.Boot()
	table, idx := c.Partition()
	if table == nil || idx != 1 {
		t.Fatalf("booted partition = %d, table=%v", idx, table)
	}
	if c.partOffset != 0x300000 || c.partSize != 0x100000 {
		t.Fatalf("window = %#x/%#x", c.partOffset, c.partSize)
	}
	if c.BootSP() != 0x20002000 || c.BootEntry() != 0x10003400 {
		t.Fatalf("vector = sp %#x pc %#x", c.BootSP(), c.BootEntry())
	}
	// Runtime flash reads translate into the booted slot, including the
	// debugger's side-effect-free Peek path.
	if got := c.Read32(FlashBase + 4); got != 0x10003401 {
		t.Fatalf("slot vector = %#x", got)
	}
	for i := range 4 {
		b, ok := c.Peek8(FlashBase + uint32(i))
		if !ok || b != c.Flash[0x300000+uint32(i)] {
			t.Fatalf("peek %d = %#x ok=%v, want %#x", i, b, ok, c.Flash[0x300000+uint32(i)])
		}
	}
}

// TestPartitionRejectsOutOfRange checks that a malformed table is ignored
// instead of selecting a slot outside the flash array.
func TestPartitionRejectsOutOfRange(t *testing.T) {
	c := New()
	put := func(off uint32, v uint32) { binary.LittleEndian.PutUint32(c.Flash[off:], v) }
	put(0, partitionMagic)
	put(4, 1)
	put(8, 1)
	put(12, 0)
	put(partitionHeader, FlashSize-0x1000)
	put(partitionHeader+4, 0x100000) // ends past the 16 MiB array
	put(partitionHeader+8, partitionExe)
	if table := ParsePartitionTable(c.Flash); table != nil {
		t.Fatalf("accepted out-of-range table: %+v", table)
	}
	if _, err := BootVector(c.Flash); err == nil {
		t.Fatal("BootVector accepted an out-of-range slot")
	}
	c.Boot()
	if c.partOffset != 0 || c.partSize != 0 {
		t.Fatalf("booted partition %#x/%#x", c.partOffset, c.partSize)
	}
}

func TestPartitionPreferredAndActive(t *testing.T) {
	c := buildPartitionFlash(t, 1) // active = slot A
	c.Boot()
	if _, idx := c.Partition(); idx != 0 {
		t.Fatalf("active slot = %d, want 0", idx)
	}
	c.PreferPartition(1)
	c.Boot()
	if _, idx := c.Partition(); idx != 1 {
		t.Fatalf("preferred slot = %d, want 1", idx)
	}
}

func TestPartitionFind(t *testing.T) {
	c := buildPartitionFlash(t, 0)
	table := ParsePartitionTable(c.Flash)
	if i := table.Find(0x2F0000); i != 0 {
		t.Fatalf("find slot A = %d", i)
	}
	if i := table.Find(0x300000); i != 1 {
		t.Fatalf("find slot B = %d", i)
	}
	if i := table.Find(0x400000); i != -1 {
		t.Fatalf("find outside = %d", i)
	}
}
