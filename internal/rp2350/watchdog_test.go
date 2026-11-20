package rp2350

import "testing"

func TestWatchdogForcedResetReason(t *testing.T) {
	c := New()
	c.wd.reason = wdReasonForce
	c.requestReset("watchdog", 0)
	c.Step()
	if c.wd.reason != wdReasonForce {
		t.Fatalf("forced reset reason = %#x, want %#x", c.wd.reason, wdReasonForce)
	}
}

func TestWatchdogExpiryResetReason(t *testing.T) {
	c := New()
	c.wd.reason = 0
	c.requestReset("watchdog", 0)
	c.Step()
	if c.wd.reason != wdReasonTimer {
		t.Fatalf("expiry reset reason = %#x, want %#x", c.wd.reason, wdReasonTimer)
	}
}
