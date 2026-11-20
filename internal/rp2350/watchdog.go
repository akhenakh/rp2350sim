package rp2350

// RP2350 watchdog. RP2350 has a 24-bit down counter in microseconds; a
// trigger (or expiry) resets the chip. The Pico SDK's watchdog_reboot writes
// scratch registers and then either sets TRIGGER (delay 0) or ENABLE + LOAD.
type watchdogBlock struct {
	chip     *Chip
	block    *regBlock
	running  bool
	deadline uint64 // virtual microseconds
	load     uint32
	reason   uint32
}

const (
	wdCtrlEnable  = 1 << 30
	wdCtrlTrigger = 1 << 31
	wdTimeMask    = 0x00ffffff

	wdReasonTimer = 1 << 0
	wdReasonForce = 1 << 1

	wdRegCtrl    = 0x00
	wdRegLoad    = 0x04
	wdRegReason  = 0x08
	wdRegScratch = 0x0c
)

func newWatchdog(c *Chip) *watchdogBlock {
	w := &watchdogBlock{chip: c, block: newRegBlock(0x400D8000, 0x1000)}
	w.block.readFn = w.read
	w.block.writeFn = w.write
	return w
}

func (w *watchdogBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case wdRegCtrl:
		v := w.block.regs[0] & wdTimeMask
		if w.running {
			now := c.NowMicros()
			remaining := uint32(0)
			if w.deadline > now {
				remaining = uint32(min(w.deadline-now, uint64(wdTimeMask)))
			}
			v |= wdCtrlEnable | remaining
		}
		return v, true
	case wdRegReason:
		return w.reason, true
	}
	return 0, false
}

func (w *watchdogBlock) write(c *Chip, off uint32, v uint32) {
	switch off {
	case wdRegCtrl:
		if v&wdCtrlTrigger != 0 {
			w.running = false
			w.block.regs[0] = 0
			w.reason |= wdReasonForce
			c.requestReset("watchdog", 0)
			return
		}
		if v&wdCtrlEnable == 0 {
			w.running = false
			w.block.regs[0] = v & wdTimeMask
			return
		}
		delay := w.load
		if delay == 0 {
			delay = v & wdTimeMask
		}
		w.running = true
		w.deadline = c.NowMicros() + uint64(delay)
		w.block.regs[0] = wdCtrlEnable | (v & wdTimeMask)
	case wdRegLoad:
		w.load = v & wdTimeMask
		w.block.regs[wdRegLoad/4] = w.load
		if w.running {
			w.deadline = c.NowMicros() + uint64(w.load)
		}
	}
}

func (w *watchdogBlock) tick(now uint64) {
	if !w.running || now < w.deadline {
		return
	}
	w.running = false
	w.reason |= wdReasonTimer
	w.chip.requestReset("watchdog", 0)
}
