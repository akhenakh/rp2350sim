package rp2350

import (
	"encoding/binary"
	"slices"
	"testing"
)

// TestOTPECCRoundTrip checks that every 16-bit value survives the Hamming
// encode/decode path, including a single flipped data bit and a single
// flipped ECC bit.
func TestOTPECCRoundTrip(t *testing.T) {
	values := []uint16{0, 1, 0xffff, 0xaaaa, 0x5555, 0x4845, 0x1234, 0x8000}
	for v := 0; v < 0x10000; v += 997 {
		values = append(values, uint16(v))
	}
	for _, v := range values {
		raw := otpEncodeRow(v)
		got, ok := otpDecodeRow(raw)
		if !ok || got != v {
			t.Fatalf("round trip of %#04x: got %#04x ok=%v raw=%#06x", v, got, ok, raw)
		}
		for bit := range 16 {
			corrupt := raw ^ 1<<bit
			got, ok = otpDecodeRow(corrupt)
			if !ok || got != v {
				t.Fatalf("single data bit %d of %#04x: got %#04x ok=%v", bit, v, got, ok)
			}
		}
		for bit := 16; bit < 22; bit++ {
			corrupt := raw ^ 1<<bit
			got, ok = otpDecodeRow(corrupt)
			if !ok || got != v {
				t.Fatalf("single ECC bit %d of %#04x: got %#04x ok=%v", bit, v, got, ok)
			}
		}
	}
}

// TestOTPECCDoubleError makes sure two flipped data bits are reported as
// uncorrectable (the RP2350 Hamming code has distance four).
func TestOTPECCDoubleError(t *testing.T) {
	raw := otpEncodeRow(0x1234)
	for a := range 16 {
		for b := a + 1; b < 16; b++ {
			if _, ok := otpDecodeRow(raw ^ 1<<a ^ 1<<b); ok {
				t.Fatalf("two-bit error at %d,%d reported as correctable", a, b)
			}
		}
	}
}

// TestOTPBRP checks that a row stored inverted (both BRP bits set) decodes to
// the same data.
func TestOTPBRP(t *testing.T) {
	raw := otpEncodeRow(0x00ff)
	inverted := raw ^ 0xffffff
	got, ok := otpDecodeRow(inverted)
	if !ok || got != 0x00ff {
		t.Fatalf("BRP row: got %#04x ok=%v", got, ok)
	}
}

// TestOTPWindows checks the ECC and raw read aliases, including the
// USR.DCTRL gate.
func TestOTPWindows(t *testing.T) {
	c := New()
	c.SetOTPRow(0x400, otpEncodeRow(0x4845))

	// ECC alias: two rows per word. Row 0x400 is at byte 0x800.
	if got := c.Read32(OTPDataBase + 0x800); got != 0x00004845 {
		t.Fatalf("ECC alias = %#08x, want 0x00004845", got)
	}
	// Raw alias: one 24-bit row per word.
	want := otpEncodeRow(0x4845)
	if got := c.Read32(OTPDataRawBase + 0x1000); got != want {
		t.Fatalf("raw alias = %#06x, want %#06x", got, want)
	}
	// Narrow reads select bytes of the same word.
	if got := c.Read8(OTPDataBase + 0x801); got != 0x48 {
		t.Fatalf("ECC alias byte 1 = %#02x, want 0x48", got)
	}

	// DCTRL cleared: every data read returns all-ones.
	c.Write32(OTPBase+otpUSR, 0)
	if got := c.Read32(OTPDataBase + 0x800); got != 0xffffffff {
		t.Fatalf("ECC alias with DCTRL=0 = %#08x, want all-ones", got)
	}
	if got := c.Read32(OTPDataRawBase + 0x1000); got != 0xffffffff {
		t.Fatalf("raw alias with DCTRL=0 = %#08x, want all-ones", got)
	}
	// PD independently disables reads even while DCTRL remains set.
	c.Write32(OTPBase+otpUSR, 1|otpUSRPD)
	if got := c.Read32(OTPDataBase + 0x800); got != 0xffffffff {
		t.Fatalf("ECC alias with PD=1 = %#08x, want all-ones", got)
	}
	if got := c.Read32(OTPDataRawBase + 0x1000); got != 0xffffffff {
		t.Fatalf("raw alias with PD=1 = %#08x, want all-ones", got)
	}
	c.Write32(OTPBase+otpUSR, 1)
	if got := c.Read32(OTPDataBase + 0x800); got != 0x00004845 {
		t.Fatalf("ECC alias after re-enabling DCTRL = %#08x", got)
	}
}

// TestOTPFuseRules checks that programming only sets bits.
func TestOTPFuseRules(t *testing.T) {
	c := New()
	if code := c.otp.writeRow(0x410, 0x00000f); code != bootromOK {
		t.Fatalf("first write: code %d", code)
	}
	if got := c.OTPRow(0x410); got != 0x00000f {
		t.Fatalf("row = %#06x, want 0x00000f", got)
	}
	// Setting more bits is allowed.
	if code := c.otp.writeRow(0x410, 0x0000ff); code != bootromOK {
		t.Fatalf("set more bits: code %d", code)
	}
	if got := c.OTPRow(0x410); got != 0x0000ff {
		t.Fatalf("row = %#06x, want 0x0000ff", got)
	}
	// Clearing a bit is refused.
	if code := c.otp.writeRow(0x410, 0x0000fe); code != bootromErrUnsupportedMod {
		t.Fatalf("clear a bit: code %d, want %d", code, bootromErrUnsupportedMod)
	}
	if c.OTPWrites() != 2 {
		t.Fatalf("writes = %d, want 2", c.OTPWrites())
	}
}

// TestOTPLocks checks the default locks, the SW_LOCK progression and the
// effect on the bootrom service and the data aliases.
func TestOTPLocks(t *testing.T) {
	c := New()
	sec, nsec := c.OTPPageLock(0)
	if sec != otpLockRO || nsec != otpLockRO {
		t.Fatalf("page 0 locks = %d/%d, want read-only/read-only", sec, nsec)
	}
	sec, nsec = c.OTPPageLock(16)
	if sec != otpLockRW || nsec != otpLockRW {
		t.Fatalf("page 16 locks = %d/%d, want read/write", sec, nsec)
	}
	// Page 0 is read-only for secure code: programming must fail.
	if code := c.otp.writeRow(0, 0x000001); code != bootromErrNotPermitted {
		t.Fatalf("write to page 0: code %d, want not permitted", code)
	}

	// Lock page 16 (rows 0x400-0x43f) from software.
	c.Write32(OTPBase+otpSWLock0+16*4, 0xf)
	sec, nsec = c.OTPPageLock(16)
	if sec != otpLockNoAccess || nsec != otpLockNoAccess {
		t.Fatalf("page 16 locks = %d/%d, want inaccessible", sec, nsec)
	}
	if _, code := c.otp.readRow(0x400); code != bootromErrNotPermitted {
		t.Fatalf("locked read: code %d, want not permitted", code)
	}
	if got := c.Read32(OTPDataBase + 0x800); got != 0xffffffff {
		t.Fatalf("locked ECC alias = %#08x, want all-ones", got)
	}
	if got := c.Read32(OTPDataRawBase + 0x1000); got != 0xffffffff {
		t.Fatalf("locked raw alias = %#08x, want all-ones", got)
	}

	// A chip reset clears software locks but keeps programmed rows.
	c.SetOTPRow(0x100, otpEncodeRow(0xbeef))
	c.Reset()
	sec, _ = c.OTPPageLock(16)
	if sec != otpLockRW {
		t.Fatalf("page 16 lock after reset = %d, want read/write", sec)
	}
	if got := c.OTPRow(0x100); got != otpEncodeRow(0xbeef) {
		t.Fatalf("row survives reset: %#06x", got)
	}
}

// TestOTPChipID checks the manufacturing chip ID rows.
func TestOTPChipID(t *testing.T) {
	c := New()
	var id uint64
	for i := range 4 {
		data, ok := otpDecodeRow(c.OTPRow(i))
		if !ok {
			t.Fatalf("chip ID row %d fails ECC", i)
		}
		id |= uint64(data) << (16 * i)
	}
	if id != 0x8f3a91c45d27be60 {
		t.Fatalf("chip ID = %#016x", id)
	}
}

func TestOTPECCWriteAppliesBRPBeforeFuseCheck(t *testing.T) {
	c := New()
	const row = 0x410
	const data = 0x1234
	target := otpEncodeRow(data)
	var preset uint32
	for bit := range 22 {
		if target&(1<<bit) == 0 {
			preset = 1 << bit
			break
		}
	}
	if preset == 0 {
		t.Fatal("ECC target has no clear bit for BRP test")
	}
	c.SetOTPRow(row, preset)
	c.Write16(SRAMBase+0x100, data)
	cpu := c.CPU0
	cpu.R[0] = SRAMBase + 0x100
	cpu.R[1] = 2
	cpu.R[2] = row | otpCmdWrite | otpCmdECC
	c.rom.stubOTPAccess(c, cpu)
	if cpu.R[0] != bootromOK {
		t.Fatalf("BRP ECC write = %d, want OK", cpu.R[0])
	}
	if got, want := c.OTPRow(row), target^0xffffff; got != want {
		t.Fatalf("BRP row = %#06x, want %#06x", got, want)
	}
	if got, ok := otpDecodeRow(c.OTPRow(row)); !ok || got != data {
		t.Fatalf("decoded BRP row = %#04x ok=%v", got, ok)
	}
}

func TestOTPAccessBufferAlignment(t *testing.T) {
	tests := []struct {
		name    string
		ecc     bool
		write   bool
		addrOff uint32
		wantErr uint32
	}{
		{name: "aligned ECC read", ecc: true, addrOff: 0x100},
		{name: "misaligned ECC read", ecc: true, addrOff: 0x101, wantErr: bootromErrBadAlignment},
		{name: "aligned raw read", addrOff: 0x100},
		{name: "misaligned raw read", addrOff: 0x102, wantErr: bootromErrBadAlignment},
		{name: "aligned ECC write", ecc: true, write: true, addrOff: 0x100},
		{name: "misaligned ECC write", ecc: true, write: true, addrOff: 0x101, wantErr: bootromErrBadAlignment},
		{name: "aligned raw write", write: true, addrOff: 0x100},
		{name: "misaligned raw write", write: true, addrOff: 0x102, wantErr: bootromErrBadAlignment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			const row = 0x410
			unit := uint32(4)
			flags := uint32(row)
			if tt.ecc {
				unit = 2
				flags |= otpCmdECC
			}
			if tt.write {
				flags |= otpCmdWrite
			} else {
				c.SetOTPRow(row, otpEncodeRow(0x1234))
			}
			addr := SRAMBase + tt.addrOff
			for i := range unit {
				c.SRAM[tt.addrOff+uint32(i)] = byte(0x34 + i*0x11)
			}
			before := append([]byte(nil), c.SRAM[tt.addrOff:tt.addrOff+unit]...)
			cpu := c.CPU0
			cpu.R[0], cpu.R[1], cpu.R[2] = addr, unit, flags
			c.rom.stubOTPAccess(c, cpu)
			if cpu.R[0] != tt.wantErr {
				t.Fatalf("otp_access = %d, want %d", cpu.R[0], tt.wantErr)
			}
			if tt.wantErr != 0 {
				if !slices.Equal(c.SRAM[tt.addrOff:tt.addrOff+unit], before) {
					t.Fatal("misaligned access modified its buffer")
				}
				if tt.write && c.OTPRow(row) != 0 {
					t.Fatal("misaligned write modified OTP")
				}
				return
			}
			if tt.write {
				want := uint32(0x00564534)
				if tt.ecc {
					want = otpEncodeRow(0x4534)
				}
				if got := c.OTPRow(row); got != want {
					t.Fatalf("programmed row = %#06x, want %#06x", got, want)
				}
				return
			}
			if tt.ecc {
				if got := binary.LittleEndian.Uint16(c.SRAM[tt.addrOff:]); got != 0x1234 {
					t.Fatalf("ECC read = %#04x", got)
				}
			} else if got := binary.LittleEndian.Uint32(c.SRAM[tt.addrOff:]) & 0xffffff; got != otpEncodeRow(0x1234) {
				t.Fatalf("raw read = %#06x", got)
			}
		})
	}
}
