package rp2350

import "testing"

// The SDK's gpio_put() goes through CP0 MCRR #4 (pin, value); make sure a 0
// clears and a 1 sets, and that mask forms update the SIO output word.
func TestGPIOCoprocBits(t *testing.T) {
	c := New()
	cpu := c.CPU0
	cpu.R[5] = 25
	cpu.R[6] = 1
	c.ExecGPIOCoproc(cpu, 0xEC46, 0x5040) // mcrr p0,#4,r5,r6,c0
	if c.Pins.Out&(1<<25) == 0 {
		t.Fatal("bit put 1 did not set GP25")
	}
	cpu.R[6] = 0
	c.ExecGPIOCoproc(cpu, 0xEC46, 0x5040)
	if c.Pins.Out&(1<<25) != 0 {
		t.Fatal("bit put 0 did not clear GP25")
	}

	// MCRR #2 is "set mask": low word in Rt, high in Rt2.
	cpu.R[0] = 1 << 3
	cpu.R[1] = 0
	c.ExecGPIOCoproc(cpu, 0xEC41, 0x0020) // mcrr p0,#2,r0,r1,c0
	if c.Pins.Out&(1<<3) == 0 {
		t.Fatal("set mask did not set GP3")
	}
	// MCRR #3 clears it again.
	c.ExecGPIOCoproc(cpu, 0xEC41, 0x0030)
	if c.Pins.Out&(1<<3) != 0 {
		t.Fatal("clear mask did not clear GP3")
	}
}

func TestGPIOCoprocIndexedWrites(t *testing.T) {
	c := New()
	cpu := c.CPU0
	c.Pins.Out = 0x0000000F0000000F
	cpu.R[0] = 1 << 1 // value/mask
	cpu.R[1] = 1      // select GPIO_HI_OUT

	c.ExecGPIOCoproc(cpu, 0xEC01, 0x0480) // mcrr p0,#8,r0,r1,c0
	if got := c.Pins.Out; got != 0x000000020000000F {
		t.Fatalf("indexed put = %#x, want high half replaced and low half preserved", got)
	}
	cpu.R[0] = 1 << 3
	c.ExecGPIOCoproc(cpu, 0xEC01, 0x0480)
	if got := c.Pins.Out; got != 0x000000080000000F {
		t.Fatalf("indexed high put = %#x, want low half preserved", got)
	}

	cpu.R[0] = 1 << 2
	c.ExecGPIOCoproc(cpu, 0xEC01, 0x0490) // xor
	c.ExecGPIOCoproc(cpu, 0xEC01, 0x04A0) // set
	cpu.R[0] = 1 << 3
	c.ExecGPIOCoproc(cpu, 0xEC01, 0x04B0) // clear
	if got := c.Pins.Out; got != 0x000000040000000F {
		t.Fatalf("indexed xor/set/clear = %#x, want 0x000000040000000f", got)
	}

	c.Pins.OE = 0x0000000500000005
	cpu.R[0] = 1 << 2
	c.ExecGPIOCoproc(cpu, 0xEC01, 0x0484) // mcrr p0,#8,r0,r1,c4
	if got := c.Pins.OE; got != 0x0000000400000005 {
		t.Fatalf("indexed OE put = %#x, want high half replaced and low half preserved", got)
	}
	cpu.R[0] = 1 << 3
	c.ExecGPIOCoproc(cpu, 0xEC01, 0x0484)
	if got := c.Pins.OE; got != 0x0000000800000005 {
		t.Fatalf("indexed high OE put = %#x, want low half preserved", got)
	}
}

func TestGPIOCoprocMCRRHighHalf(t *testing.T) {
	c := New()
	cpu := c.CPU0
	cpu.R[0] = 1 << 3
	cpu.R[1] = 1 << 3
	c.ExecGPIOCoproc(cpu, 0xEC41, 0x0020) // mcrr p0,#2,r0,r1,c0 (hilo)
	if c.Pins.Out&(1<<3) == 0 || c.Pins.Out&(1<<35) == 0 {
		t.Fatalf("hilo set mask = %#x, want GP3 and GP35", c.Pins.Out)
	}

	cpu.R[0] = 0
	cpu.R[1] = 1 << 4
	c.ExecGPIOCoproc(cpu, 0xEC41, 0x0021) // mcrr p0,#2,r0,r1,c1 (high)
	if c.Pins.Out&(1<<36) == 0 {
		t.Fatalf("high set mask = %#x, want GP36", c.Pins.Out)
	}
}
