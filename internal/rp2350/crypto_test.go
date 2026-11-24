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
	c.Cycles += shaDigestCycles
	c.sha.tick(c)
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

func writeSHAData(t *testing.T, c *Chip, data []byte, widths []int) {
	t.Helper()
	for off, i := 0, 0; off < len(data); i++ {
		n := min(widths[i%len(widths)], len(data)-off)
		switch n {
		case 1:
			c.Write8(shaBase+4, data[off])
		case 2:
			c.Write16(shaBase+4, uint16(data[off])|uint16(data[off+1])<<8)
		case 4:
			c.Write32(shaBase+4, binary.LittleEndian.Uint32(data[off:off+4]))
		default:
			t.Fatalf("unsupported test write width %d", n)
		}
		off += n
		if off%64 == 0 {
			c.Cycles += shaDigestCycles
			c.sha.tick(c)
		}
	}
}

func checkSHASum(t *testing.T, c *Chip, want [32]byte) {
	t.Helper()
	for i := range 8 {
		if got, wantWord := c.Read32(shaBase+8+uint32(i*4)), binary.BigEndian.Uint32(want[i*4:]); got != wantWord {
			t.Fatalf("SUM%d = %08x, want %08x", i, got, wantWord)
		}
	}
}

func TestSHASubwordAccumulation(t *testing.T) {
	msg := make([]byte, 120)
	for i := range msg {
		msg[i] = byte(i*37 + 11)
	}
	padded := append([]byte(nil), msg...)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0)
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(msg))*8)
	padded = append(padded, size[:]...)
	want := sha256.Sum256(msg)

	for _, tc := range []struct {
		name   string
		widths []int
	}{
		{"bytes", []int{1}},
		{"halfwords", []int{2}},
		{"words", []int{4}},
		{"mixed subwords", []int{1, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			c.Write32(shaBase, 1|1<<12) // START | BSWAP
			writeSHAData(t, c, padded, tc.widths)
			checkSHASum(t, c, want)
		})
	}
}

func TestSHABSwapAppliesAtCommit(t *testing.T) {
	var block [64]byte
	for i := range block {
		block[i] = byte(i*13 + 7)
	}
	c := New()
	c.Write32(shaBase, 1) // START, BSWAP clear
	for i := 0; i < len(block); i += 4 {
		c.Write32(shaBase+4, binary.BigEndian.Uint32(block[i:i+4]))
	}
	c.Cycles += shaDigestCycles
	c.sha.tick(c)
	var want [8]uint32
	for i := range want {
		want[i] = c.Read32(shaBase + 8 + uint32(i*4))
	}

	c = New()
	c.Write32(shaBase, 1) // START, BSWAP clear
	for i := 0; i < len(block); i += 4 {
		c.Write8(shaBase+4, block[i+3])
		c.Write8(shaBase+4, block[i+2])
		c.Write8(shaBase+4, block[i+1])
		c.Write8(shaBase+4, block[i])
	}
	c.Cycles += shaDigestCycles
	c.sha.tick(c)
	for i := range want {
		if got := c.Read32(shaBase + 8 + uint32(i*4)); got != want[i] {
			t.Fatalf("subword BSWAP SUM%d = %08x, want %08x", i, got, want[i])
		}
	}
}

func TestSHAResetStartAndDMASize(t *testing.T) {
	c := New()
	if got := c.Read32(shaBase); got != 0x1206 {
		t.Fatalf("CSR reset = %#x, want 0x1206", got)
	}
	for i := range 8 {
		if got := c.Read32(shaBase + 8 + uint32(i*4)); got != 0 {
			t.Fatalf("reset SUM%d = %#x", i, got)
		}
	}
	c.Write32(shaBase, 1|1<<12|3<<8)
	if got := c.Read32(shaBase) & (3 << 8); got != 3<<8 {
		t.Fatalf("DMA_SIZE readback = %#x", got)
	}
	for i := range sha256IV {
		if got := c.Read32(shaBase + 8 + uint32(i*4)); got != sha256IV[i] {
			t.Fatalf("START SUM%d = %08x, want IV %08x", i, got, sha256IV[i])
		}
	}
	c.Write8(shaBase+4, 1)
	if c.Read32(shaBase)&(1<<2) != 0 {
		t.Fatal("WDATA did not clear SUM_VLD")
	}
	c.Write32(shaBase, 1|1<<12|2<<8)
	if c.Read32(shaBase)&(1<<2) == 0 {
		t.Fatal("START did not force SUM_VLD")
	}
	c.Reset()
	if got := c.Read32(shaBase); got != 0x1206 {
		t.Fatalf("CSR after reset = %#x", got)
	}
}

func TestSHATimingAndEarlyWrite(t *testing.T) {
	c := New()
	c.Write32(shaBase, 1|1<<12)
	for range 16 {
		c.Write32(shaBase+4, 0)
	}
	if csr := c.Read32(shaBase); csr&(1<<1) != 0 || csr&(1<<2) != 0 {
		t.Fatalf("full block CSR = %#x, want WDATA_RDY/SUM_VLD clear", csr)
	}
	c.Write32(shaBase+4, 0x12345678)
	if c.Read32(shaBase)&(1<<4) == 0 {
		t.Fatal("early WDATA write did not set ERR_WDATA_NOT_RDY")
	}
	c.Cycles += shaDigestCycles - 1
	if csr := c.Read32(shaBase); csr&(1<<1) != 0 {
		t.Fatalf("SHA became ready after 56 cycles: %#x", csr)
	}
	c.Write32(shaBase, c.Read32(shaBase)|1<<4)
	if c.Read32(shaBase)&(1<<4) != 0 {
		t.Fatal("ERR_WDATA_NOT_RDY did not clear")
	}
	c.Cycles++
	if csr := c.Read32(shaBase); csr&(1<<1) == 0 || csr&(1<<2) == 0 {
		t.Fatalf("SHA not ready/valid after 57 cycles: %#x", csr)
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

func TestTRNGResetAndFieldMasks(t *testing.T) {
	c := New()
	if got := c.Read32(trngBase + trngIMR); got != trngIRQMask {
		t.Fatalf("IMR reset = %#x, want %#x", got, trngIRQMask)
	}
	if got := c.Read32(trngBase + trngVersion); got != 0 {
		t.Fatalf("VERSION reset = %#x", got)
	}
	c.Write32(trngBase+trngConfig, 0xFFFFFFFF)
	c.Write32(trngBase+trngDebug, 0xFFFFFFFF)
	c.Write32(trngBase+trngDebugEn, 0xFFFFFFFF)
	if got := c.Read32(trngBase + trngConfig); got != trngConfigMask {
		t.Fatalf("CONFIG readback = %#x", got)
	}
	if got := c.Read32(trngBase + trngDebug); got != trngDebugMask {
		t.Fatalf("DEBUG readback = %#x", got)
	}
	if got := c.Read32(trngBase + trngDebugEn); got != 1 {
		t.Fatalf("DEBUG_EN readback = %#x", got)
	}
}

func TestTRNGRstBitsCounter(t *testing.T) {
	c := New()
	trng := c.trng
	c.Write32(trngBase+trngEnable, 1)
	c.Write32(trngBase+trngEnable, 0)
	c.Write32(trngBase+trngConfig, 3)
	c.Write32(trngBase+trngDebug, 0xE)
	c.Write32(trngBase+trngIMR, 0)
	trng.isr = 0xF
	c.Write32(trngBase+trngRstBits, 0xFFFFFFFF)
	if trng.valid || trng.ehr != [trngEHRWords]uint32{} {
		t.Fatal("RST_BITS_COUNTER did not clear collection state")
	}
	if trng.isr != 0xE {
		t.Fatalf("RST_BITS_COUNTER ISR = %#x, want preserved error bits", trng.isr)
	}
	if trng.config != 3 || trng.debug != 0xE || trng.imr != 0 || trng.enabled {
		t.Fatal("RST_BITS_COUNTER changed unrelated configuration")
	}

	c.Write32(trngBase+trngEnable, 1)
	trng.valid = true
	trng.ehr[0] = 0x12345678
	c.Write32(trngBase+trngRstBits, 1)
	if !trng.valid || trng.ehr[0] != 0x12345678 {
		t.Fatal("RST_BITS_COUNTER took effect while enabled")
	}
}

func TestTRNGEHRConsumedWhileDisabled(t *testing.T) {
	c := New()
	c.Write32(trngBase+trngEnable, 1)
	c.Write32(trngBase+trngEnable, 0)
	for i := range trngEHRWords {
		c.Read32(trngBase + trngEHR0 + uint32(i*4))
	}
	if c.Read32(trngBase+trngValid) != 0 || c.Read32(trngBase+trngISR)&1 != 0 {
		t.Fatal("last disabled EHR read did not clear valid/status")
	}
	for i := range trngEHRWords {
		if got := c.Read32(trngBase + trngEHR0 + uint32(i*4)); got != 0 {
			t.Fatalf("EHR%d after consumption = %#x", i, got)
		}
	}
}

func TestTRNGInterruptAndClearAlias(t *testing.T) {
	c := New()
	trng := c.trng
	c.Write32(trngBase+trngEnable, 1)
	if c.ppb[0].pending[1]&(1<<(trngIRQ-32)) != 0 {
		t.Fatal("TRNG interrupted with reset IMR mask")
	}
	c.Write32(trngBase+trngIMR, 0)
	if c.ppb[0].pending[1]&(1<<(trngIRQ-32)) == 0 || c.ppb[1].pending[1]&(1<<(trngIRQ-32)) == 0 {
		t.Fatal("unmasked EHR_VALID did not pend IRQ 39")
	}
	c.Write32(trngBase+trngICR, 1)
	if c.ppb[0].pending[1]&(1<<(trngIRQ-32)) != 0 || c.ppb[1].pending[1]&(1<<(trngIRQ-32)) != 0 {
		t.Fatal("IRQ 39 stayed pending after EHR_VALID clear")
	}

	trng.isr = 3
	c.Write32(trngBase+0x3000+trngICR, 3)
	if trng.isr != 2 {
		t.Fatalf("CLR alias ISR = %#x, want AUTOCORR_ERR preserved", trng.isr)
	}
	c.Write32(trngBase+trngSWReset, 1)
	if trng.isr != 0 || trng.imr != trngIRQMask {
		t.Fatal("SW reset did not restore TRNG interrupt state")
	}
	if c.ppb[0].pending[1]&(1<<(trngIRQ-32)) != 0 || c.ppb[1].pending[1]&(1<<(trngIRQ-32)) != 0 {
		t.Fatal("SW reset left IRQ 39 pending")
	}
}
