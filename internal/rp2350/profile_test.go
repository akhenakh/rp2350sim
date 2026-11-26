package rp2350

import "testing"

func TestProfileCoverageSamplesAndStack(t *testing.T) {
	c := New()
	c.CPU1.Parked = true
	c.Write16(SRAMBase, 0x6808)   // ldr r0, [r1]
	c.Write16(SRAMBase+2, 0xE7FC) // b SRAMBase
	c.CPU0.R[1] = SRAMBase + 0x100
	c.CPU0.R[13] = SRAMBase + 0x1000
	c.CPU0.R[15] = SRAMBase
	c.StartProfile(ProfileConfig{SampleCycles: 100, Coverage: true})
	c.Run(1000)
	if !c.Profiling() {
		t.Fatal("profile stopped by itself")
	}
	data, ok := c.ProfileData()
	if !ok {
		t.Fatal("no profile data")
	}
	if data.Coverage[SRAMBase] == 0 || data.Coverage[SRAMBase+2] == 0 {
		t.Fatalf("coverage = %v", data.Coverage)
	}
	if len(data.Samples) == 0 || data.CoreSamples[0] == 0 {
		t.Fatalf("samples = %v core0 = %d", data.Samples, data.CoreSamples[0])
	}
	if !data.Stack[0].MSPValid || data.Stack[0].MSPMin > SRAMBase+0x1000 {
		t.Fatalf("stack = %+v", data.Stack[0])
	}
	if data.ElapsedCycles < 1000 || data.StartCycles != 0 {
		t.Fatalf("timing = %+v", data)
	}

	// Stopping keeps the data and freezes the sample count.
	c.StopProfile()
	before := len(c.profile.samples)
	c.Run(1000)
	if c.Profiling() {
		t.Fatal("still profiling after stop")
	}
	if len(c.profile.samples) != before {
		t.Fatal("samples changed after stop")
	}
}

func TestProfileIRQLatency(t *testing.T) {
	c := New()
	c.CPU1.Parked = true
	c.StartProfile(ProfileConfig{SampleCycles: 100})
	c.Cycles = 1000
	c.ppb[0].Pend(5)
	c.Cycles += 500
	c.ppb[0].ExceptionEntered(21)
	data, _ := c.ProfileData()
	if data.IRQCount != 1 || data.IRQSlowest != 500 {
		t.Fatalf("irq count = %d slowest = %d", data.IRQCount, data.IRQSlowest)
	}
	if data.IRQHist[4] != 1 {
		t.Fatalf("hist = %v, want bucket 4", data.IRQHist)
	}

	// A line that deasserts before entry must not be measured.
	c.ppb[0].Pend(6)
	c.ppb[0].ClearPending(6)
	c.Cycles += 200
	c.ppb[0].ExceptionEntered(22)
	data, _ = c.ProfileData()
	if data.IRQCount != 1 {
		t.Fatalf("unpended IRQ was measured: %+v", data)
	}
}
