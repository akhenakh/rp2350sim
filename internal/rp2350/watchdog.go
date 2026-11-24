package rp2350

// RP2350 watchdog. RP2350 has a 24-bit down counter in microseconds; a
// trigger (or expiry) resets the chip. The Pico SDK's watchdog_reboot writes
// scratch registers and then either sets TRIGGER (delay 0) or ENABLE + LOAD.
type watchdogBlock struct {
	chip           *Chip
	block          *regBlock
	running        bool
	deadline       uint64 // virtual microseconds
	remaining      uint32
	remainingValid bool
	load           uint32
	reason         uint32
	// scratch survives a watchdog/chip reset in hardware; the SDK stores the
	// "enabled reboot" marker in scratch[4].
	scratch [8]uint32
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
	w.block.resetFn = func(c *Chip) {
		w.running = false
		w.deadline = 0
		w.remaining = 0
		w.remainingValid = false
		w.load = 0
		for i := range w.scratch {
			w.block.regs[(wdRegScratch+uint32(i)*4)/4] = w.scratch[i]
		}
	}
	return w
}

func (w *watchdogBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case wdRegCtrl:
		v := w.block.regs[0] &^ wdTimeMask
		if w.running {
			v |= wdCtrlEnable | w.remainingAt(c.NowMicros())
		} else if w.remainingValid {
			v |= w.remaining
		}
		return v, true
	case wdRegReason:
		return w.reason, true
	}
	return 0, false
}

func (w *watchdogBlock) remainingAt(now uint64) uint32 {
	if w.deadline <= now {
		return 0
	}
	return uint32(min(w.deadline-now, uint64(wdTimeMask)))
}

func (w *watchdogBlock) write(c *Chip, off uint32, v uint32) {
	if off >= wdRegScratch && off < wdRegScratch+4*8 {
		i := (off - wdRegScratch) / 4
		w.scratch[i] = v
		w.block.regs[off/4] = v
		return
	}
	switch off {
	case wdRegCtrl:
		if v&wdCtrlTrigger != 0 {
			w.running = false
			w.remainingValid = false
			w.block.regs[0] = 0
			w.reason |= wdReasonForce
			c.requestReset("watchdog", 0)
			return
		}
		if v&wdCtrlEnable == 0 {
			if w.running {
				w.remaining = w.remainingAt(c.NowMicros())
				w.remainingValid = true
			}
			w.running = false
			w.block.regs[0] = v & wdTimeMask
			return
		}
		delay := w.remaining
		if !w.remainingValid {
			delay = w.load
			if delay == 0 {
				delay = v & wdTimeMask
			}
		}
		w.running = true
		w.remainingValid = false
		w.deadline = c.NowMicros() + uint64(delay)
		w.block.regs[0] = wdCtrlEnable | (v & wdTimeMask)
	case wdRegLoad:
		w.load = v & wdTimeMask
		w.block.regs[wdRegLoad/4] = w.load
		if w.running {
			w.deadline = c.NowMicros() + uint64(w.load)
		} else {
			w.remaining = w.load
			w.remainingValid = true
		}
	}
}

func (w *watchdogBlock) tick(now uint64) {
	if !w.running || now < w.deadline {
		return
	}
	w.running = false
	w.remaining = 0
	w.remainingValid = false
	w.reason |= wdReasonTimer
	w.chip.requestReset("watchdog", 0)
}
