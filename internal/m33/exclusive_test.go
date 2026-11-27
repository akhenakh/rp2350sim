package m33

import "testing"

// TestLoadAcquireDoesNotArmMonitor: LDA/LDAB/LDAH are plain load-acquire
// instructions. Only LDREX* and LDAEX* own the local exclusive monitor, so an
// interleaved LDA must not make a later STREX fail.
func TestLoadAcquireDoesNotArmMonitor(t *testing.T) {
	bus := newExecBus()
	const (
		ldrexAddr = 0x20000000
		ldaAddr   = 0x20000004
		strexAddr = 0x20000008
		data      = 0x20001000
		other     = 0x20002000
	)
	put16(t, bus, ldrexAddr, 0xE851, 0x0F00) // ldrex r0, [r1]
	put16(t, bus, ldaAddr, 0xE8D3, 0x2FAF)   // lda r2, [r3]
	put16(t, bus, strexAddr, 0xE841, 0x0400) // strex r4, r0, [r1]
	bus.Write32(data, 0x11111111)
	bus.Write32(other, 0x22222222)

	c := New(bus)
	c.R[1] = data
	c.R[3] = other
	stepAt(t, c, ldrexAddr)
	if !c.monitorValid || c.monitor != data {
		t.Fatalf("after LDREX monitor=%#x valid=%v", c.monitor, c.monitorValid)
	}
	stepAt(t, c, ldaAddr)
	if c.R[2] != 0x22222222 {
		t.Fatalf("LDA loaded %#x", c.R[2])
	}
	if !c.monitorValid || c.monitor != data {
		t.Fatalf("LDA moved the monitor to %#x (valid=%v)", c.monitor, c.monitorValid)
	}
	c.R[0] = 0xABCDEF01
	stepAt(t, c, strexAddr)
	if c.R[4] != 0 {
		t.Fatalf("STREX status = %d, want 0 (monitor lost)", c.R[4])
	}
	if got := bus.Read32(data); got != 0xABCDEF01 {
		t.Fatalf("stored %#x", got)
	}
}
