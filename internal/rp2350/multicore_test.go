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
