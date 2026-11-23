package rp2350

import "encoding/binary"

// TRNG is the RP2350 true random number generator at 0x400F0000. The model is
// deterministic: it derives words from a seeded splitmix64 stream, but exposes
// the register behaviour the SDK polls (EHR valid, six data words, busy=0).
const trngBase = 0x400F0000

const (
	trngIMR      = 0x100
	trngISR      = 0x104
	trngICR      = 0x108
	trngConfig   = 0x10C
	trngValid    = 0x110
	trngEHR0     = 0x114
	trngEHRWords = 6
	trngEnable   = 0x12C
	trngSample   = 0x130
	trngAutoStat = 0x134
	trngDebug    = 0x138
	trngSWReset  = 0x140
	trngDebugEn  = 0x1B4
	trngBusy     = 0x1B8
	trngRstBits  = 0x1BC
	trngVersion  = 0x1C0
	trngBist0    = 0x1E0
)

type trngBlock struct {
	block *regBlock
	state uint64
	ehr   [trngEHRWords]uint32
	isr   uint32
	valid bool
	imr   uint32
	// config/sample/debug are stored for readback.
	config  uint32
	sample  uint32
	debug   uint32
	enabled bool
}

func newTRNG() *trngBlock {
	t := &trngBlock{block: newRegBlock(trngBase, 0x1000), state: 0x9E3779B97F4A7C15}
	t.block.readFn = t.read
	t.block.writeFn = t.write
	t.block.resetFn = func(c *Chip) { t.reset() }
	return t
}

func (t *trngBlock) reset() {
	t.state = 0x9E3779B97F4A7C15
	t.ehr = [trngEHRWords]uint32{}
	t.isr = 0
	t.valid = false
	t.enabled = false
	t.config, t.sample, t.debug = 0, 0xFFFF, 0
	t.imr = 0
}

func (t *trngBlock) splitmix() uint32 {
	t.state += 0x9E3779B97F4A7C15
	z := t.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	return uint32(z)
}

func (t *trngBlock) refill() {
	for i := range t.ehr {
		t.ehr[i] = t.splitmix()
	}
	t.valid = true
	t.isr |= 1 // EHR_VALID
}

func (t *trngBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case trngIMR:
		return t.imr, true
	case trngISR:
		return t.isr, true
	case trngConfig:
		return t.config, true
	case trngValid:
		if t.valid {
			return 1, true
		}
		return 0, true
	case trngEnable:
		if t.enabled {
			return 1, true
		}
		return 0, true
	case trngSample:
		return t.sample, true
	case trngAutoStat:
		return 0, true
	case trngDebug:
		return t.debug, true
	case trngDebugEn:
		return 0, true
	case trngBusy, trngRstBits:
		return 0, true
	case trngVersion:
		// EHR width 192, tests and decorrelators present.
		return 0x7F, true
	case trngBist0, trngBist0 + 4, trngBist0 + 8:
		return 0, true
	}
	if off >= trngEHR0 && off < trngEHR0+trngEHRWords*4 {
		idx := (off - trngEHR0) / 4
		v := t.ehr[idx]
		if t.enabled && int(idx) == trngEHRWords-1 {
			// Reading the last EHR word consumes the 192-bit sample; the
			// hardware starts another one.
			t.refill()
		}
		return v, true
	}
	return 0, false
}

func (t *trngBlock) write(c *Chip, off, v uint32) {
	switch off {
	case trngIMR:
		t.imr = v
	case trngICR:
		t.isr &^= v
	case trngConfig:
		t.config = v
	case trngEnable:
		if v&1 != 0 {
			t.enabled = true
			t.refill()
		} else {
			t.enabled = false
			t.valid = false
		}
	case trngSample:
		t.sample = v
	case trngDebug:
		t.debug = v
	case trngSWReset, trngRstBits:
		t.reset()
	case trngDebugEn:
		// accepted, debug mode has no effect
	}
}

// SHA-256 is the RP2350 hardware hash block at 0x400F8000.
const shaBase = 0x400F8000

type shaBlock struct {
	block *regBlock
	input [64]byte
	used  int
	h     [8]uint32
	valid bool
	bswap bool
}

func newSHA() *shaBlock {
	s := &shaBlock{block: newRegBlock(shaBase, 0x100), bswap: true}
	s.block.readFn = s.read
	s.block.writeFn = s.write
	s.block.writeSizeFn = s.writeSize
	s.block.resetFn = func(c *Chip) { s.reset() }
	return s
}

func (s *shaBlock) reset() {
	s.used = 0
	s.h = sha256IV
	s.valid = false
	s.bswap = true
}

func (s *shaBlock) csr() uint32 {
	var v uint32 = 1 << 1 // WDATA_RDY
	v |= 2 << 8           // DMA_SIZE = word
	if s.bswap {
		v |= 1 << 12
	}
	if s.valid {
		v |= 1 << 2 // SUM_VLD
	}
	return v
}

func (s *shaBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch {
	case off == 0:
		return s.csr(), true
	case off >= 8 && off < 8+8*4 && (off-8)%4 == 0:
		return s.h[(off-8)/4], true
	}
	return 0, false
}

func (s *shaBlock) write(c *Chip, off, v uint32) {
	switch off {
	case 0:
		if v&1 != 0 { // START
			s.used = 0
			s.h = sha256IV
			s.valid = false
		}
		s.bswap = v&(1<<12) != 0
		// START and the status bits are self-clearing in the register file.
	case 4: // WDATA
		s.pushWord(v)
	}
}

// writeSize handles byte/halfword DMA writes to WDATA. TinyUSB-style DMA
// streams SHA data one byte at a time; appending the transfer's bytes keeps
// the message in memory order regardless of the CPU store width.
func (s *shaBlock) writeSize(c *Chip, addr uint32, v uint32, size int) bool {
	if addr < shaBase+4 || addr >= shaBase+8 {
		return false
	}
	switch size {
	case 1:
		s.pushBytes([]byte{byte(v)})
	case 2:
		s.pushBytes([]byte{byte(v), byte(v >> 8)})
	default:
		return false
	}
	return true
}

func (s *shaBlock) pushWord(v uint32) {
	var b [4]byte
	if s.bswap {
		binary.LittleEndian.PutUint32(b[:], v)
	} else {
		binary.BigEndian.PutUint32(b[:], v)
	}
	s.pushBytes(b[:])
}

func (s *shaBlock) pushBytes(b []byte) {
	s.valid = false
	for _, v := range b {
		s.input[s.used] = v
		s.used++
		if s.used == 64 {
			sha256Compress(&s.h, s.input[:])
			s.used = 0
			s.valid = true
		}
	}
}

var sha256IV = [8]uint32{
	0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
	0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
}

var sha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

func shRor(x uint32, n uint) uint32 { return x>>n | x<<(32-n) }

// sha256Compress applies one SHA-256 compression round. The message padding
// is supplied by the caller (pico_sha256 writes it), so the result is the
// hardware's intermediate/final state without an implicit pad.
func sha256Compress(h *[8]uint32, block []byte) {
	var w [64]uint32
	for i := range 16 {
		w[i] = binary.BigEndian.Uint32(block[i*4:])
	}
	for i := 16; i < 64; i++ {
		s0 := shRor(w[i-15], 7) ^ shRor(w[i-15], 18) ^ w[i-15]>>3
		s1 := shRor(w[i-2], 17) ^ shRor(w[i-2], 19) ^ w[i-2]>>10
		w[i] = w[i-16] + s0 + w[i-7] + s1
	}
	a, b, c, d, e, f, g, hh := h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7]
	for i := range 64 {
		ss1 := shRor(e, 6) ^ shRor(e, 11) ^ shRor(e, 25)
		ch := (e & f) ^ (^e & g)
		t1 := hh + ss1 + ch + sha256K[i] + w[i]
		ss0 := shRor(a, 2) ^ shRor(a, 13) ^ shRor(a, 22)
		maj := (a & b) ^ (a & c) ^ (b & c)
		t2 := ss0 + maj
		hh, g, f, e, d, c, b, a = g, f, e, d+t1, c, b, a, t1+t2
	}
	h[0] += a
	h[1] += b
	h[2] += c
	h[3] += d
	h[4] += e
	h[5] += f
	h[6] += g
	h[7] += hh
}
