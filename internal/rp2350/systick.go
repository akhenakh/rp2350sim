package rp2350

// sysTickBlock models the Armv8-M SysTick timer lazily: instead of ticking a
// counter every cycle it stores the reload value and the cycle at which the
// next wrap happens, so a WFI idle skip can land exactly on the wrap.
type sysTickBlock struct {
	ctrl uint32
	load uint32
	// base is the counter value at baseCycle: the reload value after a wrap,
	// or the value written to CVR.
	base      uint32
	baseCycle uint64
	// reloadAt is set after a CVR write: the next clock edge reloads RVR
	// instead of wrapping.
	reloadAt uint64
	// stopped is set when RVR is zero: the timer raises COUNTFLAG once and
	// then stops, as the architecture specifies (and QEMU implements).
	stopped bool
}

const (
	sysTickEnable    uint32 = 1 << 0
	sysTickTickInt   uint32 = 1 << 1
	sysTickClkSource uint32 = 1 << 2
	sysTickCountFlag uint32 = 1 << 16
)

func (s *sysTickBlock) reset() { *s = sysTickBlock{} }

func (s *sysTickBlock) enabled() bool { return s.ctrl&sysTickEnable != 0 }

// divisor is the number of 150 MHz cycles per SysTick clock: the CPU clock, or
// the 1 MHz reference clock.
func (s *sysTickBlock) divisor() uint64 {
	if s.ctrl&sysTickClkSource != 0 {
		return 1
	}
	return cyclesPerMicrosecond
}

// period is the number of cycles between wraps at the current counter value.
func (s *sysTickBlock) period() uint64 {
	return (uint64(s.base) + 1) * s.divisor()
}

// value returns the current counter value.
func (s *sysTickBlock) value(now uint64) uint32 {
	if !s.enabled() {
		return s.base
	}
	if s.reloadAt != 0 && now < s.reloadAt {
		return 0
	}
	elapsed := (now - s.baseCycle) / s.divisor()
	if elapsed > uint64(s.base) {
		return 0
	}
	return s.base - uint32(elapsed)
}

// sync advances the timer to now: it sets COUNTFLAG and pends the exception for
// every wrap crossed. The fast path is a single comparison.
func (s *sysTickBlock) sync(now uint64, n *NVIC) {
	if !s.enabled() || s.stopped {
		return
	}
	if s.reloadAt != 0 {
		if now < s.reloadAt {
			return
		}
		s.base = s.load
		s.baseCycle = s.reloadAt
		s.reloadAt = 0
		if s.base == 0 {
			s.stopped = true
			return
		}
	}
	for now-s.baseCycle >= s.period() {
		s.baseCycle += s.period()
		s.base = s.load
		s.ctrl |= sysTickCountFlag
		if s.ctrl&sysTickTickInt != 0 {
			n.pendSystem(excSysTick)
		}
		if s.base == 0 {
			s.stopped = true
			return
		}
	}
}

// cyclesUntil returns the number of cycles to the next wrap, or MaxUint64 when
// the timer is disabled. Idle skips end on this cycle so a sleeping core wakes
// without overshooting.
func (s *sysTickBlock) cyclesUntil(now uint64, n *NVIC) uint64 {
	if !s.enabled() || s.stopped {
		return ^uint64(0)
	}
	s.sync(now, n)
	if s.stopped || !s.enabled() {
		return ^uint64(0)
	}
	start := s.baseCycle
	count := s.base
	if s.reloadAt != 0 {
		start = s.reloadAt
		count = s.load
	}
	next := start + (uint64(count)+1)*s.divisor()
	if next <= now {
		return 1
	}
	return next - now
}

func (s *sysTickBlock) writeCSR(v uint32, now uint64, n *NVIC) {
	// Catch a wrap crossed while the instruction stalled (PSRAM latency) before
	// rebasing the counter.
	s.sync(now, n)
	oldEnable := s.enabled()
	cur := uint32(0)
	if oldEnable {
		cur = s.value(now)
	}
	s.ctrl = (s.ctrl & sysTickCountFlag) | (v & 7)
	switch {
	case !s.enabled():
		s.base = cur
		s.baseCycle = now
		s.reloadAt = 0
		s.stopped = false
	case !oldEnable:
		// Enabling reloads RVR whatever CVR holds.
		s.base = s.load
		s.baseCycle = now
		s.reloadAt = 0
		s.stopped = false
	case !s.stopped:
		s.base = cur
		s.baseCycle = now
		s.reloadAt = 0
	}
}

func (s *sysTickBlock) writeRVR(v uint32) { s.load = v & 0xFFFFFF }

func (s *sysTickBlock) writeCVR(now uint64, n *NVIC) {
	s.sync(now, n)
	s.ctrl &^= sysTickCountFlag
	s.base = 0
	s.baseCycle = now
	s.reloadAt = 0
	s.stopped = false
	if !s.enabled() || s.load == 0 {
		// Writing CVR stops the timer when RVR is zero.
		s.stopped = true
		return
	}
	// CVR reads as zero until the next clock edge reloads RVR.
	s.reloadAt = now + s.divisor()
}

// tickSysTick is called once per cycle from the chip scheduler.
func (n *NVIC) tickSysTick(now uint64) { n.tick.sync(now, n) }

// cyclesUntilSysTick is used to clamp an idle fast-forward jump.
func (n *NVIC) cyclesUntilSysTick(now uint64) uint64 {
	return n.tick.cyclesUntil(now, n)
}
