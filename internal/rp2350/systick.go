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

// period is the number of cycles between wraps at the current reload value.
func (s *sysTickBlock) period() uint64 {
	return (uint64(s.base) + 1) * s.divisor()
}

// value returns the current counter value.
func (s *sysTickBlock) value(now uint64) uint32 {
	if !s.enabled() {
		return s.base
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
	if !s.enabled() {
		return
	}
	p := s.period()
	if now-s.baseCycle < p {
		return
	}
	wraps := (now - s.baseCycle) / p
	s.baseCycle += wraps * p
	s.base = s.load
	s.ctrl |= sysTickCountFlag
	if s.ctrl&sysTickTickInt != 0 {
		n.pendSystem(excSysTick)
	}
}

// cyclesUntil returns the number of cycles to the next wrap, or MaxUint64 when
// the timer is disabled. Idle skips end on this cycle so a sleeping core wakes
// without overshooting.
func (s *sysTickBlock) cyclesUntil(now uint64, n *NVIC) uint64 {
	if !s.enabled() {
		return ^uint64(0)
	}
	s.sync(now, n)
	if !s.enabled() {
		return ^uint64(0)
	}
	next := s.baseCycle + s.period()
	if next <= now {
		return 1
	}
	return next - now
}

func (s *sysTickBlock) writeCSR(v uint32, now uint64) {
	oldEnable := s.enabled()
	cur := uint32(0)
	if oldEnable {
		cur = s.value(now)
	}
	s.ctrl = (s.ctrl & sysTickCountFlag) | (v & 7)
	if s.enabled() {
		if oldEnable {
			s.base = cur
		} else {
			// Enabling reloads RVR whatever CVR holds.
			s.base = s.load
		}
		s.baseCycle = now
	}
}

func (s *sysTickBlock) writeRVR(v uint32) { s.load = v & 0xFFFFFF }

func (s *sysTickBlock) writeCVR(now uint64) {
	s.ctrl &^= sysTickCountFlag
	if s.enabled() {
		s.base = s.load
		s.baseCycle = now
	} else {
		s.base = 0
	}
}

// tickSysTick is called once per cycle from the chip scheduler.
func (n *NVIC) tickSysTick(now uint64) { n.tick.sync(now, n) }

// cyclesUntilSysTick is used to clamp an idle fast-forward jump.
func (n *NVIC) cyclesUntilSysTick(now uint64) uint64 {
	return n.tick.cyclesUntil(now, n)
}
