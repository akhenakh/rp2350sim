package rp2350

// PSM (power state machine) at 0x40018000. Only the processor 1 force-off
// control is modelled: multicore_reset_core1() asserts PROC1 through the
// +0x2000 SET alias, polls FRCE_OFF until it reads back, then clears it
// through the +0x3000 CLR alias and waits for core 1's bootrom 0 push.
const (
	psmBase         = 0x40018000
	psmFrceOff      = 0x04
	psmFrceOffProc1 = 1 << 24
)

func newPSM(c *Chip) *regBlock {
	b := newRegBlock(psmBase, 0x1000)
	forced := false
	b.writeFn = func(c *Chip, off, v uint32) {
		if off != psmFrceOff {
			return
		}
		on := v&psmFrceOffProc1 != 0
		if on == forced {
			return
		}
		forced = on
		if on {
			c.holdCore1()
			return
		}
		c.releaseCore1()
	}
	b.resetFn = func(c *Chip) { forced = false }
	return b
}

// holdCore1 takes processor 1 through a hard reset. Its private SCS and launch
// handshake state are cleared, and any queued FIFO traffic is dropped.
func (c *Chip) holdCore1() {
	c.resetCore(1)
	c.sio.flushFIFOs(c)
}

// releaseCore1 brings processor 1 out of reset. The bootrom drains its mailbox
// and pushes a 0 to core 0 before waiting for a launch sequence.
func (c *Chip) releaseCore1() {
	c.sio.restartCore1(c)
}
