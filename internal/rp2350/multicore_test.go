package rp2350

import "testing"

// SEV is a system-wide event: it must wake the other core from WFE (this is
// how TinyGo's multicore spinlocks make progress) but must not wake WFI.
func TestSignalEventWakesWFEOnly(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU0.SleepOnEvent = true
	c.CPU1.Sleeping = true
	c.CPU1.SleepOnEvent = false

	c.SignalEvent()

	if c.CPU0.Sleeping {
		t.Fatal("WFE core did not wake on SEV")
	}
	if !c.CPU0.EventRegister || !c.CPU1.EventRegister {
		t.Fatal("event register not set on both cores")
	}
	if !c.CPU1.Sleeping {
		t.Fatal("WFI core must not wake on SEV")
	}
}

// TinyGo's scheduler spinlocks release with `str` + `sev`, so the SEV
// instruction executed on one core (not just the Chip.SignalEvent helper) must
// wake the peer parked in WFE. A SEV wired to the executing core only
// deadlocks a waiting core in spinLock.Lock.
func TestExecutedSEVWakesPeerWFE(t *testing.T) {
	c := New()
	c.CPU0.Sleeping = true
	c.CPU0.SleepOnEvent = true
	c.CPU1.Parked = false
	c.Write16(sramCode, 0xBF40) // sev
	c.CPU1.R[15] = sramCode

	c.Step()

	if c.CPU0.Sleeping || c.CPU0.SleepOnEvent {
		t.Fatal("executed SEV did not wake the WFE core")
	}
	if !c.CPU0.EventRegister || !c.CPU1.EventRegister {
		t.Fatal("executed SEV did not set the event register on both cores")
	}

	c = New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = false
	c.Write16(sramCode, 0xBF40)
	c.CPU1.R[15] = sramCode

	c.Step()

	if !c.CPU0.Sleeping {
		t.Fatal("executed SEV woke a WFI core")
	}
}

// pushCore0 writes a word to the SIO FIFO from core 0 and returns the echo
// core 1 produces while parked in the bootrom handshake.
func pushCore0(c *Chip, v uint32) uint32 {
	c.core = 0
	c.Write32(SIOBase+0x54, v)
	echo := c.Read32(SIOBase + 0x58)
	return echo
}

// TestCore1LaunchRejectsJunkSequence checks that only the bootrom handshake
// releases core 1: six arbitrary words must not be taken for a launch.
func TestCore1LaunchRejectsJunkSequence(t *testing.T) {
	c := New()
	for _, w := range []uint32{5, 4, 3, 2, 1, 0} {
		if echo := pushCore0(c, w); echo != w {
			t.Fatalf("push %#x echoed %#x", w, echo)
		}
	}
	if !c.sio.core1Parked || c.Core1Launches() != 0 {
		t.Fatalf("junk sequence launched core 1 (launches=%d parked=%v)",
			c.Core1Launches(), c.sio.core1Parked)
	}
}

// TestCore1LaunchResyncsPrefix covers the bootrom receive loop: junk is
// discarded, a 1 starts the launch sequence, and a zero in VTOR/SP/entry
// restarts it. A complete sequence then launches exactly once.
func TestCore1LaunchResyncsPrefix(t *testing.T) {
	const vt, sp, entry = 0x1000, 0x20001000, 0x10000101
	c := New()

	// Junk before the 1 is ignored.
	for _, w := range []uint32{7, 9} {
		pushCore0(c, w)
	}
	pushCore0(c, 1)
	pushCore0(c, vt)
	if echo := pushCore0(c, 0); echo != 0 { // SP zero restarts the sequence
		t.Fatalf("zero restart echoed %#x", echo)
	}
	if !c.sio.core1Parked || c.Core1Launches() != 0 {
		t.Fatal("truncated sequence launched core 1")
	}

	// A fresh sequence after the restart launches.
	pushCore0(c, 0)
	pushCore0(c, 0)
	pushCore0(c, 1)
	pushCore0(c, vt)
	pushCore0(c, sp)
	pushCore0(c, entry)

	if c.sio.core1Parked || c.Core1Launches() != 1 {
		t.Fatalf("valid launch did not release core 1 (launches=%d parked=%v)",
			c.Core1Launches(), c.sio.core1Parked)
	}
	if c.CPU1.VTOR != vt || c.CPU1.MSP != sp || c.CPU1.R[13] != sp || c.CPU1.R[15] != entry&^1 {
		t.Fatalf("launch state VTOR=%#x MSP=%#x SP=%#x PC=%#x",
			c.CPU1.VTOR, c.CPU1.MSP, c.CPU1.R[13], c.CPU1.R[15])
	}
	if c.CPU1.R[14] != bootromParkLoop|1 {
		t.Fatalf("core 1 LR = %#x, want the bootrom park loop", c.CPU1.R[14])
	}
}

// TestPSMResetCore1 checks multicore_reset_core1(): FRCE_OFF.PROC1 resets
// processor 1 and flushes FIFO/launch state, and clearing it makes the
// bootrom drain and push the 0 acknowledgement core 0 waits for.
func TestPSMResetCore1(t *testing.T) {
	c := New()
	// Launch core 1, then leave stale traffic in both mailboxes.
	for _, w := range []uint32{0, 0, 1, 0x1000, 0x20001000, 0x10000101} {
		pushCore0(c, w)
	}
	if c.sio.core1Parked {
		t.Fatal("core 1 did not launch")
	}
	c.core = 1
	c.Write32(SIOBase+0x54, 0xdead) // core 1 -> core 0
	c.core = 0
	c.Write32(SIOBase+0x54, 0xbeef) // core 0 -> core 1

	c.Write32(psmBase+0x2000+psmFrceOff, psmFrceOffProc1) // SET alias
	if c.Read32(psmBase+psmFrceOff)&psmFrceOffProc1 == 0 {
		t.Fatal("FRCE_OFF did not read back PROC1")
	}
	if !c.sio.core1Parked || c.CPU1.R[15] != 0 {
		t.Fatal("PROC1 force-off did not reset core 1")
	}
	if len(c.sio.fifo[0]) != 0 || len(c.sio.fifo[1]) != 0 {
		t.Fatal("core reset left FIFO traffic queued")
	}

	c.Write32(psmBase+0x3000+psmFrceOff, psmFrceOffProc1) // CLR alias
	if c.Read32(psmBase+psmFrceOff)&psmFrceOffProc1 != 0 {
		t.Fatal("FRCE_OFF stayed asserted after the clear alias")
	}
	if !c.sio.core1Parked {
		t.Fatal("core 1 left reset without a launch sequence")
	}
	if len(c.sio.fifo[0]) != 1 || c.sio.fifo[0][0] != 0 {
		t.Fatalf("bootrom drain acknowledgement = %v, want one zero", c.sio.fifo[0])
	}
	if c.ppb[0].pending[0]&(1<<sioIRQFIFO) == 0 {
		t.Fatal("drain acknowledgement did not raise the core 0 FIFO IRQ")
	}
	c.core = 0
	if got := c.Read32(SIOBase + 0x58); got != 0 {
		t.Fatalf("drain acknowledgement = %#x, want 0", got)
	}
	// The handshake starts from the prefix again.
	for _, w := range []uint32{0, 0, 1, 0x1000, 0x20001000, 0x10000101} {
		pushCore0(c, w)
	}
	if c.sio.core1Parked || c.Core1Launches() != 2 {
		t.Fatalf("relaunch after PSM reset: launches=%d parked=%v",
			c.Core1Launches(), c.sio.core1Parked)
	}
}
