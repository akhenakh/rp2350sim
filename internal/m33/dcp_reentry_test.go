package m33

import (
	"math"
	"testing"
)

func dcpGet64(c *CPU) float64 {
	return math.Float64frombits(uint64(c.R[0]) | uint64(c.R[1])<<32)
}

// TestDCPReentrantSaveRestore mimics the SDK's saving_func: an interrupt
// between ADD1 and NRDD saves X/Y/status, runs its own DCP operation, then
// restores the outer state. The outer result must survive.
func TestDCPReentrantSaveRestore(t *testing.T) {
	cpu := New(newTestBus())
	cpu.dcp.x, cpu.dcp.y = 1.5, 2.5
	cpu.dcp.format = dcpDouble
	cpu.dcp.engaged = true
	cpu.dcpCDP(0xEE00, 0x0001) // ADD0
	cpu.dcpCDP(0xEE10, 0x0001) // ADD1: X becomes 4.0

	save := func(crm uint16) (lo, hi uint32) {
		cpu.R[0], cpu.R[1] = 0, 0
		cpu.dcpMRRC(0xEC51, crm, true) // PXMD / PYMD / REFD
		return cpu.R[0], cpu.R[1]
	}
	xl, xh := save(8)
	yl, yh := save(9)
	fl, fh := save(10)

	// The handler clobbers the coprocessor with its own multiplication.
	cpu.dcp.x, cpu.dcp.y, cpu.dcp.op = 10, 20, dcpMul
	cpu.dcp.format = dcpDouble
	cpu.finish()
	if cpu.dcp.x != 200 {
		t.Fatalf("handler result = %v", cpu.dcp.x)
	}

	// WXMD / WYMD / WEFD.
	cpu.R[0], cpu.R[1] = xl, xh
	cpu.dcpMCRR(0xEC51, 0x0000)
	cpu.R[0], cpu.R[1] = yl, yh
	cpu.dcpMCRR(0xEC51, 0x0001)
	cpu.R[0], cpu.R[1] = fl, fh
	cpu.dcpMCRR(0xEC51, 0x0002)

	cpu.dcpCDP(0xEE80, 0x0020) // NRDD
	cpu.R[0], cpu.R[1] = 0, 0
	cpu.dcpMRRC(0xEC51, 0x0000, false) // RDDA
	if got := dcpGet64(cpu); got != 4 {
		t.Fatalf("outer dadd = %v, want 4", got)
	}
	if cpu.dcp.engaged {
		t.Fatal("final read left engaged set")
	}
}

// TestDCPEngagedFlag checks the PCMP/RCMP split: the peek form reports the
// engaged flag in N and preserves it; the plain form clears it after reading.
func TestDCPEngagedFlag(t *testing.T) {
	cpu := New(newTestBus())
	cpu.dcp.x, cpu.dcp.y = 1, 2
	cpu.dcp.engaged = true

	cpu.dcpMRC(0xEE10, 0xF020, true) // PCMP apsr_nzcv (peek)
	if cpu.APSR&flagN == 0 {
		t.Fatal("PCMP did not report engaged in N")
	}
	if !cpu.dcp.engaged {
		t.Fatal("PCMP cleared the engaged flag")
	}

	cpu.APSR &^= 0xF0000000
	cpu.dcpMRC(0xEE10, 0xF020, false) // RCMP
	if cpu.dcp.engaged {
		t.Fatal("RCMP preserved the engaged flag")
	}
	cpu.APSR &^= 0xF0000000
	cpu.dcpMRC(0xEE10, 0xF020, true)
	if cpu.APSR&flagN != 0 {
		t.Fatal("PCMP reported engaged after the final read")
	}
}
