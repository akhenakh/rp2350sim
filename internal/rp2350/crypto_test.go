package rp2350

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

// TestSHA256 checks the block against the standard "abc" vector. The SDK
// writes 32-bit words loaded from the byte stream with BSWAP enabled.
func TestSHA256(t *testing.T) {
	c := New()
	msg := []byte("abc")
	padded := append([]byte(nil), msg...)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0)
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(msg))*8)
	padded = append(padded, size[:]...)

	c.Write32(shaBase, 1|1<<12) // START | BSWAP
	for i := 0; i < len(padded); i += 4 {
		c.Write32(shaBase+4, binary.LittleEndian.Uint32(padded[i:i+4]))
	}
	csr := c.Read32(shaBase)
	if csr&(1<<2) == 0 {
		t.Fatalf("SUM_VLD clear, csr=%#x", csr)
	}
	want := sha256.Sum256(msg)
	for i := range 8 {
		got := c.Read32(shaBase + 8 + uint32(i*4))
		wantWord := binary.BigEndian.Uint32(want[i*4:])
		if got != wantWord {
			t.Fatalf("SUM%d = %08x, want %08x", i, got, wantWord)
		}
	}
}

// TestTRNG checks the polls used by pico_rand: busy is clear, the six EHR
// words are filled and deterministic.
func TestTRNG(t *testing.T) {
	c := New()
	c.Write32(trngBase+trngEnable, 1)
	if c.Read32(trngBase+trngBusy) != 0 {
		t.Fatal("TRNG busy is set")
	}
	if c.Read32(trngBase+trngValid) != 1 {
		t.Fatal("EHR did not become valid")
	}
	if c.Read32(trngBase+trngISR)&1 == 0 {
		t.Fatal("EHR_VALID interrupt not raised")
	}
	var first [trngEHRWords]uint32
	empty := true
	for i := range first {
		first[i] = c.Read32(trngBase + trngEHR0 + uint32(i*4))
		if first[i] != 0 {
			empty = false
		}
	}
	if empty {
		t.Fatal("EHR words are all zero")
	}
	// A fresh chip yields the same deterministic stream.
	c2 := New()
	c2.Write32(trngBase+trngEnable, 1)
	for i := range first {
		if got := c2.Read32(trngBase + trngEHR0 + uint32(i*4)); got != first[i] {
			t.Fatalf("EHR%d not deterministic: %08x vs %08x", i, got, first[i])
		}
	}
	// Reading the last word starts a new 192-bit sample.
	var second [trngEHRWords]uint32
	for i := range second {
		second[i] = c.Read32(trngBase + trngEHR0 + uint32(i*4))
	}
	if second == first {
		t.Fatal("EHR did not advance to a new sample")
	}
	// Clearing the interrupt does not invalidate the data.
	c.Write32(trngBase+trngICR, 1)
	if c.Read32(trngBase+trngISR)&1 != 0 {
		t.Fatal("EHR_VALID did not clear")
	}
	if c.Read32(trngBase+trngValid) != 1 {
		t.Fatal("clearing ISR invalidated the EHR")
	}
}
