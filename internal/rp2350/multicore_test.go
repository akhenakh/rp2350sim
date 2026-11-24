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
