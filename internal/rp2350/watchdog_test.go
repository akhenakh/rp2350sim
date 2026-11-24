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

func TestWatchdogResumeCountdown(t *testing.T) {
	tests := []struct {
		name        string
		rewriteLoad bool
		wantResumed uint32
		expiryAtUS  uint64
	}{
		{name: "preserve remaining", wantResumed: 5, expiryAtUS: 105},
		{name: "rewritten load", rewriteLoad: true, wantResumed: 20, expiryAtUS: 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			c.Write32(0x400d8004, 10)
			c.Write32(0x400d8000, wdCtrlEnable)
			c.Cycles = 5 * cyclesPerMicrosecond
			if got := c.Read32(0x400d8000) & wdTimeMask; got != 5 {
				t.Fatalf("remaining before disable = %d, want 5", got)
			}
			c.Write32(0x400d8000, 0)
			c.Cycles = 100 * cyclesPerMicrosecond
			if got := c.Read32(0x400d8000) & wdTimeMask; got != 5 {
				t.Fatalf("remaining while disabled = %d, want 5", got)
			}
			if tt.rewriteLoad {
				c.Write32(0x400d8004, 20)
			}
			c.Write32(0x400d8000, wdCtrlEnable)
			if got := c.Read32(0x400d8000) & wdTimeMask; got != tt.wantResumed {
				t.Fatalf("remaining after re-enable = %d, want %d", got, tt.wantResumed)
			}
			c.Cycles = (tt.expiryAtUS - 1) * cyclesPerMicrosecond
			c.wd.tick(c.NowMicros())
			if c.pendingReset {
				t.Fatal("watchdog reset before the resumed countdown expired")
			}
			c.Cycles = tt.expiryAtUS * cyclesPerMicrosecond
			c.wd.tick(c.NowMicros())
			if !c.pendingReset {
				t.Fatal("watchdog did not reset after the resumed countdown")
			}
		})
	}
}
