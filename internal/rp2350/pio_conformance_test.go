package rp2350

import "testing"

func newPIOConformanceChip() *Chip {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	configurePads(c)
	return c
}

func tickPIO(p *pioBlock, n int) {
	for range n {
		p.tick()
	}
}

func TestPIOClockDividerPhase(t *testing.T) {
	tests := []struct {
		name   string
		div    uint32
		before int
		want   []int
	}{
		{name: "full speed", div: 1 << 16, want: []int{1, 2, 3, 4}},
		{name: "integer two", div: 2 << 16, want: []int{2, 4, 6, 8}},
		{name: "integer zero is 65536", div: 0, before: 65534, want: []int{65536}},
		{name: "disabled divider keeps phase", div: 2 << 16, before: 1, want: []int{2, 4, 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPIOConformanceChip()
			p := c.pio[0]
			s := &p.sm[0]
			s.pc = 0
			s.x = 0
			p.mem[0] = 0xE021 // set x, 1
			p.mem[1] = 0xE022 // set x, 2
			p.mem[2] = 0xE023 // set x, 3
			p.mem[3] = 0xE024 // set x, 4
			c.Write32(pioBase0+0xC8, tt.div)
			tickPIO(p, tt.before)
			c.Write32(pioBase0, 1)
			current := tt.before
			for i, cycle := range tt.want {
				tickPIO(p, cycle-current)
				current = cycle
				if want := uint32(i + 1); s.x != want {
					t.Fatalf("execution %d at cycle %d: x=%d, want %d", i+1, cycle, s.x, want)
				}
			}
		})
	}
}

func TestPIOFractionalDividerRate(t *testing.T) {
	c := newPIOConformanceChip()
	s := &c.pio[0].sm[0]
	c.Write32(pioBase0+0xC8, 2<<16|128<<8)
	enables := 0
	for range 1280 {
		if s.clockTick() {
			enables++
		}
	}
	if enables != 512 {
		t.Fatalf("2.5 divider produced %d enables in 1280 cycles, want 512", enables)
	}
}

func TestPIOClockDividerRestart(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	p.mem[0] = 0xE021
	p.mem[1] = 0xE022
	c.Write32(pioBase0+0xC8, 2<<16)
	tickPIO(p, 1)
	c.Write32(pioBase0+0x2000, 1) // enable without restarting the divider
	p.tick()
	if s.x != 1 {
		t.Fatalf("enable restarted divider phase: x=%d", s.x)
	}
	c.Write32(pioBase0+0x2000, 1<<8) // CLKDIV_RESTART
	c.Write32(pioBase0+0x2000, 1)
	p.tick()
	if s.x != 1 {
		t.Fatalf("instruction ran immediately after CLKDIV_RESTART: x=%d", s.x)
	}
	p.tick()
	if s.x != 2 {
		t.Fatalf("instruction did not run on restarted divider's second cycle: x=%d", s.x)
	}
}

func TestPIOINUsesLowBits(t *testing.T) {
	tests := []struct {
		name  string
		right bool
		count int
		data  uint32
		want  uint32
	}{
		{name: "left byte", count: 8, data: 0x12345678, want: 0x78},
		{name: "right byte", right: true, count: 8, data: 0x12345678, want: 0x78000000},
		{name: "left three", count: 3, data: 0xfffffffA, want: 2},
		{name: "right three", right: true, count: 3, data: 0xfffffffA, want: 0x40000000},
		{name: "left word", count: 32, data: 0x12345678, want: 0x12345678},
		{name: "right word", right: true, count: 32, data: 0x12345678, want: 0x12345678},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPIOConformanceChip()
			s := &c.pio[0].sm[0]
			s.x = tt.data
			s.shiftctrl = 0
			if tt.right {
				s.shiftctrl |= 1 << 18
			}
			s.execute(0x4000 | 1<<5 | uint16(tt.count))
			if s.isr != tt.want {
				t.Fatalf("ISR=%#08x, want %#08x", s.isr, tt.want)
			}
		})
	}
}

func TestPIOExecRunsOnNextCycle(t *testing.T) {
	tests := []struct {
		name string
		inst uint16
	}{
		{name: "out exec", inst: 0x7FE0}, // out exec, 32 [31]
		{name: "mov exec", inst: 0xB081}, // mov exec, x [31]
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPIOConformanceChip()
			p := c.pio[0]
			s := &p.sm[0]
			p.mem[0] = tt.inst
			p.mem[1] = 0xE023 // set x, 3
			s.osr = 0xE227    // set x, 7 [2]
			s.x = 0xE227
			s.outCount = 0
			c.Write32(pioBase0, 1)

			p.tick() // OUT/MOV queues the executee; its delay is ignored.
			if s.x != 0xE227 || s.pc != 1 || s.delay != 0 {
				t.Fatalf("after outer: x=%#x pc=%d delay=%d", s.x, s.pc, s.delay)
			}
			p.tick() // executee runs, retains PC and applies its own delay.
			if s.x != 7 || s.pc != 1 || s.delay != 2 {
				t.Fatalf("after executee: x=%d pc=%d delay=%d", s.x, s.pc, s.delay)
			}
			p.tick()
			p.tick()
			if s.x != 7 || s.delay != 0 {
				t.Fatalf("executee delay did not elapse: x=%d delay=%d", s.x, s.delay)
			}
			p.tick()
			if s.x != 3 {
				t.Fatalf("program did not resume after executee: x=%d", s.x)
			}
		})
	}
}

func TestPIOExecWaitsForEnabledCycle(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	p.mem[0] = 0xA081 // mov exec, x
	p.mem[1] = 0xE023
	s.x = 0xE027 // set x, 7
	c.Write32(pioBase0+0xC8, 2<<16)
	c.Write32(pioBase0, 1)
	p.tick()
	if s.pc != 0 || s.x != 0xE027 {
		t.Fatalf("SM ran on a disabled divider cycle: pc=%d x=%#x", s.pc, s.x)
	}
	p.tick()
	if s.pc != 1 || s.x != 0xE027 {
		t.Fatalf("outer MOV EXEC timing: pc=%d x=%#x", s.pc, s.x)
	}
	p.tick()
	if s.x != 0xE027 {
		t.Fatalf("executee ignored the divider: x=%#x", s.x)
	}
	p.tick()
	if s.x != 7 || s.pc != 1 {
		t.Fatalf("executee did not run on the next enabled cycle: x=%d pc=%d", s.x, s.pc)
	}
}

func TestPIONestedExec(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	p.mem[0] = 0x7FE0 // out exec, 32 [31]
	p.mem[1] = 0xE023
	s.shiftctrl = 1 << 17 // autopull at 32
	s.outCount = 0
	s.osr = 0x60E0 // out exec, 32
	c.Write32(pioBase0+0x10, 0xE227)
	c.Write32(pioBase0, 1)
	p.tick() // executes first OUT EXEC and autopulls the next instruction
	if s.x != 0 {
		t.Fatalf("nested executee ran in outer cycle: x=%#x", s.x)
	}
	p.tick() // executes the nested OUT EXEC
	if s.x != 0 {
		t.Fatalf("innermost instruction ran too early: x=%#x", s.x)
	}
	p.tick()
	if s.x != 7 {
		t.Fatalf("innermost executee did not run: x=%d", s.x)
	}
}

func TestPIOJMPDecrementWrapsZero(t *testing.T) {
	c := newPIOConformanceChip()
	s := &c.pio[0].sm[0]
	s.pc = 0
	s.execute(0x0045) // jmp x--, 5
	if s.x != 0xffffffff || s.pc != 0 || s.pcSet {
		t.Fatalf("x-- from zero: x=%#x pc=%d pcSet=%v", s.x, s.pc, s.pcSet)
	}
	s.execute(0x0085) // jmp y--, 5
	if s.y != 0xffffffff || s.pc != 0 || s.pcSet {
		t.Fatalf("y-- from zero: y=%#x pc=%d pcSet=%v", s.y, s.pc, s.pcSet)
	}
	s.x = 2
	s.execute(0x0045)
	if s.x != 1 || s.pc != 5 || !s.pcSet {
		t.Fatalf("x-- from two: x=%d pc=%d pcSet=%v", s.x, s.pc, s.pcSet)
	}
}

func TestPIOSideSetWinsAndRunsOnStall(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Pins.Func[0] = pioFuncSel(0)
	c.Pins.Func[1] = pioFuncSel(0)
	c.Pins.OE |= 1<<0 | 1<<1
	c.Write32(pioBase0+0xDC, 1<<29|1<<20) // side-set GP0, OUT GP0
	s.osr = 0
	s.outCount = 0
	p.mem[0] = 0x7001 // out pins, 1 side 1
	c.Write32(pioBase0, 1)
	p.tick()
	if !c.Pins.Level(0) {
		t.Fatal("main OUT overwrote overlapping side-set")
	}

	c.Pins.SetExternal(2, false)
	c.Write32(pioBase0+0xDC, 1<<29|1<<10) // side-set GP1
	p.mem[0] = 0x3082                     // wait 1 gpio 2 side 1
	c.Write32(pioBase0+0x2000, 1<<4)      // restart SM0
	p.tick()
	if !c.Pins.Level(1) {
		t.Fatal("side-set did not apply on the first stalled WAIT cycle")
	}
	if s.pc != 0 {
		t.Fatalf("stalled WAIT advanced PC to %d", s.pc)
	}
}

func TestPIOForcedInstructionStallAndReadback(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	p.mem[0] = 0xE023
	p.mem[1] = 0xE024
	c.Write32(pioBase0+0xC8, 100<<16)
	c.Write32(pioBase0+0xD8, 0xE025) // forced set x, 5, while disabled
	if s.x != 5 || s.pc != 0 {
		t.Fatalf("forced instruction: x=%d pc=%d", s.x, s.pc)
	}
	if got := c.Read32(pioBase0 + 0xD8); got != 0xE023 {
		t.Fatalf("INSTR readback=%#x, want current memory instruction %#x", got, 0xE023)
	}

	c.Write32(pioBase0+0xD8, 0x80A0) // pull ifempty block
	if got := c.Read32(pioBase0 + 0xCC); got&(1<<31) == 0 {
		t.Fatal("stalled forced instruction did not set EXEC_STALLED")
	}
	c.Write32(pioBase0+0xD8, 0xA042) // nop clears the stalled forced instruction
	if got := c.Read32(pioBase0 + 0xCC); got&(1<<31) != 0 {
		t.Fatal("NOP did not clear EXEC_STALLED")
	}

	c.Write32(pioBase0+0xD8, 0x80A0)
	c.Write32(pioBase0+0x10, 0x12345678)
	c.Write32(pioBase0, 1)
	for range 100 {
		p.tick()
	}
	if s.osr != 0x12345678 || s.outCount != 0 {
		t.Fatalf("stalled forced PULL did not complete: osr=%#x count=%d", s.osr, s.outCount)
	}
	if got := c.Read32(pioBase0 + 0xCC); got&(1<<31) != 0 {
		t.Fatal("EXEC_STALLED did not clear after completion")
	}
}

func TestPIOInputCountMasksPins(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Write32(pioBase0+0x38, 0xffffffff)
	c.Write32(pioBase0+0xD0, 5) // IN_COUNT=5
	for pin := range 48 {
		c.Pins.SetExternal(pin, false)
	}
	for _, pin := range []int{0, 4, 7} {
		c.Pins.SetExternal(pin, true)
	}
	if got := s.inSource(0); got != 0x11 {
		t.Fatalf("masked IN source=%#x, want 0x11", got)
	}
	s.execMov(0xA020) // mov x, pins
	if s.x != 0x11 {
		t.Fatalf("masked MOV PINS=%#x, want 0x11", s.x)
	}
	s.execute(0x4008) // in pins, 8, shift left
	if s.isr != 0x11 {
		t.Fatalf("masked IN PINS produced ISR=%#x, want 0x11", s.isr)
	}
}

func TestPIOPullBarrierAndEmptyBehavior(t *testing.T) {
	c := newPIOConformanceChip()
	s := &c.pio[0].sm[0]
	s.shiftctrl = 1 << 17 // autopull
	s.osr = 0x11111111
	s.outCount = 0 // OSR full
	s.pushTx(0x22222222)
	if !s.execute(0x80A0) { // pull ifempty block
		t.Fatal("PULL barrier stalled with a full OSR")
	}
	if s.osr != 0x11111111 || s.txN != 1 {
		t.Fatalf("PULL barrier changed full OSR: osr=%#x tx=%d", s.osr, s.txN)
	}
	s.outCount = 32
	if !s.execute(0x80A0) || s.osr != 0x22222222 || s.outCount != 0 || s.txN != 0 {
		t.Fatalf("PULL did not load empty OSR: osr=%#x count=%d tx=%d", s.osr, s.outCount, s.txN)
	}

	s.x = 0x33333333
	s.outCount = 32
	c.pio[0].fdebug = 0
	if !s.execute(0x8080) { // pull noblock
		t.Fatal("nonblocking PULL stalled")
	}
	if s.osr != s.x || s.outCount != 0 {
		t.Fatalf("empty nonblocking PULL: osr=%#x count=%d", s.osr, s.outCount)
	}
	if c.pio[0].fdebug&(1<<24) != 0 {
		t.Fatal("nonblocking empty PULL set TXSTALL")
	}
}

func TestPIOForcedSideSetAndStickyRestart(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Pins.Func[0] = pioFuncSel(0)
	c.Pins.OE |= 1
	c.Write32(pioBase0+0xDC, 1<<29)  // one side-set bit on GP0
	c.Write32(pioBase0+0xD8, 0xE001) // forced set pins, 1 side 0
	if c.Pins.Level(0) {
		t.Fatal("forced instruction's main SET beat its side-set")
	}

	c.Write32(pioBase0+0xCC, 1<<17) // OUT_STICKY
	c.Write32(pioBase0+0xDC, 1<<20) // one OUT pin on GP0
	s.osr = 1
	s.outCount = 0
	c.Write32(pioBase0+0xD8, 0x6001) // forced out pins, 1
	if p.outVal&1 == 0 {
		t.Fatal("sticky forced OUT did not latch pin high")
	}
	c.Write32(pioBase0, 1<<4) // SM_RESTART
	if p.outVal&1 != 0 || c.Pins.Level(0) {
		t.Fatal("SM_RESTART did not clear the sticky pin write")
	}
}

func TestPIOShiftCounterUpdates(t *testing.T) {
	c := newPIOConformanceChip()
	s := &c.pio[0].sm[0]
	s.inCount = 31
	s.execute(0x4020) // in x, 32
	if s.inCount != 32 {
		t.Fatalf("input counter did not saturate: %d", s.inCount)
	}
	s.execute(0x60C0) // out isr, 32
	if s.inCount != 32 {
		t.Fatalf("OUT ISR did not set input counter: %d", s.inCount)
	}
	s.execute(0xA0C1) // mov isr, x
	if s.inCount != 0 {
		t.Fatalf("MOV ISR did not clear input counter: %d", s.inCount)
	}
	s.outCount = 31
	s.osr = 0xffffffff
	s.execute(0x6020) // out x, 32
	if s.outCount != 32 {
		t.Fatalf("output counter did not saturate: %d", s.outCount)
	}
	s.execute(0xA0E3) // mov osr, null
	if s.outCount != 0 {
		t.Fatalf("MOV OSR did not clear output counter: %d", s.outCount)
	}
}

func TestPIOAutopullDoesNotFillAndOutputSameCycle(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	p.mem[0] = 0x6020 // out x, 32
	p.mem[1] = 0x6020
	c.Write32(pioBase0+0xD0, 1<<17)
	c.Write32(pioBase0, 1)
	p.tick() // empty OSR and TX FIFO: stall
	if s.x != 0 {
		t.Fatalf("OUT ran without data: x=%#x", s.x)
	}
	c.Write32(pioBase0+0x10, 0x11223344)
	p.tick() // refill only
	if s.x != 0 || s.osr != 0x11223344 || s.outCount != 0 {
		t.Fatalf("refill cycle also output: x=%#x osr=%#x count=%d", s.x, s.osr, s.outCount)
	}
	c.Write32(pioBase0+0x10, 0x55667788)
	p.tick() // output first word and refill the second simultaneously
	if s.x != 0x11223344 || s.osr != 0x55667788 || s.outCount != 0 || s.txN != 0 {
		t.Fatalf("same-cycle output/refill: x=%#x osr=%#x count=%d tx=%d", s.x, s.osr, s.outCount, s.txN)
	}
	p.tick()
	if s.x != 0x55667788 {
		t.Fatalf("second word=%#x", s.x)
	}
}

func TestPIOAutopullDuringWait(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Write32(pioBase0+0x38, 0xffffffff)
	c.Pins.SetExternal(0, false)
	c.Write32(pioBase0+0xD0, 1<<17)
	c.Write32(pioBase0+0x10, 0x12345678)
	s.outCount = 32
	p.mem[0] = 0x2080 // wait 1 gpio 0
	c.Write32(pioBase0, 1)
	p.tick()
	if !s.stalled || s.osr != 0x12345678 || s.outCount != 0 || s.txN != 0 {
		t.Fatalf("autopull during stalled WAIT: stalled=%v osr=%#x count=%d tx=%d", s.stalled, s.osr, s.outCount, s.txN)
	}
}

func TestPIOPermissiveOUTRefillRetained(t *testing.T) {
	c := newPIOConformanceChip()
	c.Write32(pioBase0+0x10, 0x89abcdef)
	c.Write32(pioBase0+0xD8, 0x6020) // forced out x, 32, autopull disabled
	if got := c.pio[0].sm[0].x; got != 0x89abcdef {
		t.Fatalf("TinyGo permissive refill broke: x=%#x", got)
	}
}

func TestPIOPushFullNonblockingClearsISR(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	for i := range 4 {
		s.pushRx(uint32(i))
	}
	s.isr = 0x12345678
	s.inCount = 32
	p.mem[0] = 0x8000 // push noblock
	p.mem[1] = 0xE029
	c.Write32(pioBase0, 1)
	p.tick()
	if s.rxN != 4 || s.isr != 0 || s.inCount != 0 || s.pc != 1 {
		t.Fatalf("nonblocking full PUSH: rx=%d isr=%#x in=%d pc=%d", s.rxN, s.isr, s.inCount, s.pc)
	}
	if p.fdebug&1 == 0 {
		t.Fatal("nonblocking full PUSH did not set RXSTALL")
	}
}

func TestPIOAutopushStallCompletesBeforeNextInstruction(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	s.pushRx(0xaaaaaaaa) // fill the 4-entry RX FIFO
	for range 3 {
		s.pushRx(0xbbbbbbbb)
	}
	c.Write32(pioBase0+0xD0, 1<<16|1<<20) // autopush, threshold 1
	p.mem[0] = 0x4021                     // in x, 1
	p.mem[1] = 0xE029                     // set x, 9
	c.Write32(pioBase0, 1)
	p.tick()
	if s.pc != 0 {
		t.Fatalf("stalled IN advanced PC to %d", s.pc)
	}
	s.popRx()
	p.tick() // completes the stalled IN; must not execute PC 1 yet
	if s.pc != 1 || s.x != 0 {
		t.Fatalf("stall completion cycle: pc=%d x=%d", s.pc, s.x)
	}
	p.tick()
	if s.x != 9 {
		t.Fatalf("next instruction did not run after stall completion: x=%d", s.x)
	}
}

func TestPIOWaitSourcesAndPinMask(t *testing.T) {
	tests := []struct {
		name      string
		inst      uint16
		gpioBase  uint32
		inBase    uint32
		inCount   uint32
		jmpPin    uint32
		highPins  []int
		wantStall bool
	}{
		{name: "gpio waits for high", inst: 0x2084, gpioBase: 16, wantStall: true},
		{name: "gpio absolute high", inst: 0x2084, gpioBase: 16, highPins: []int{4}},
		{name: "gpio base does not remap", inst: 0x2084, gpioBase: 16, highPins: []int{20}, wantStall: true},
		{name: "pin mapped", inst: 0x20A3, gpioBase: 16, inBase: 2, inCount: 4, highPins: []int{21}},
		{name: "pin masked", inst: 0x20A3, gpioBase: 16, inBase: 2, inCount: 3, highPins: []int{21}, wantStall: true},
		{name: "jmppin offset", inst: 0x20E3, gpioBase: 16, jmpPin: 30, highPins: []int{17}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPIOConformanceChip()
			p := c.pio[0]
			s := &p.sm[0]
			c.Write32(pioBase0+0x168, tt.gpioBase)
			c.Write32(pioBase0+0x38, 0xffffffff) // bypass synchronizers
			c.Write32(pioBase0+0xDC, tt.inBase<<15)
			c.Write32(pioBase0+0xD0, tt.inCount)
			c.Write32(pioBase0+0xCC, tt.jmpPin<<24|1<<12) // wrap after PC 0
			for pin := range 48 {
				c.Pins.SetExternal(pin, false)
			}
			for _, pin := range tt.highPins {
				c.Pins.SetExternal(pin, true)
			}
			p.mem[0] = tt.inst
			p.mem[1] = 0xE029
			tickPIO(p, 2) // settle synchronized inputs before enabling the SM
			c.Write32(pioBase0, 1)
			p.tick()
			if got := s.pc == 0; got != tt.wantStall {
				t.Fatalf("stalled=%v, want %v (pc=%d)", got, tt.wantStall, s.pc)
			}
		})
	}
}

func TestPIOFIFOInterruptSources(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Write32(pioBase0+pioIRQ0INTE, 1<<0|1<<4|1<<8)
	if got := c.Read32(pioBase0 + 0x16C); got&(1<<0|1<<4) != 1<<4 {
		t.Fatalf("initial FIFO INTR=%#x, want RX nonempty clear and TX not-full set", got)
	}
	s.pushRx(0x12345678)
	if got := c.Read32(pioBase0 + 0x16C); got&1 != 1 {
		t.Fatalf("RX non-empty INTR not set: %#x", got)
	}
	if c.NVIC.pending[0]&(1<<15) == 0 {
		t.Fatal("RX FIFO status did not reach NVIC")
	}
	s.popRx()
	if got := c.Read32(pioBase0 + 0x16C); got&1 != 0 {
		t.Fatalf("RX non-empty INTR did not clear: %#x", got)
	}
	if c.NVIC.pending[0]&(1<<15) == 0 {
		t.Fatal("TX not-full source should keep PIO IRQ pending")
	}
	for range 4 {
		s.pushTx(0)
	}
	if got := c.Read32(pioBase0 + 0x16C); got&(1<<4) != 0 {
		t.Fatalf("TX not-full source stayed set on a full FIFO: %#x", got)
	}
	s.popTx()
	if got := c.Read32(pioBase0 + 0x16C); got&(1<<4) == 0 {
		t.Fatalf("TX not-full source did not return after a pop: %#x", got)
	}
	c.Write32(pioBase0+pioIRQ0INTE, 0)
	if c.NVIC.pending[0]&(1<<15) != 0 {
		t.Fatal("masking FIFO sources left NVIC pending")
	}

	c.Write32(pioBase0+pioIRQ0INTE, 1<<8)
	p.mem[0] = 0x20C0 // wait 1 irq 0: clear on success
	p.irq = 1
	p.updateIRQ()
	if c.NVIC.pending[0]&(1<<15) == 0 {
		t.Fatal("shared IRQ flag did not reach NVIC")
	}
	c.Write32(pioBase0, 1)
	p.tick()
	if p.irq&1 != 0 || c.NVIC.pending[0]&(1<<15) != 0 {
		t.Fatal("WAIT IRQ clear did not update shared flag and NVIC")
	}
}

func TestPIOFJOINGetPut(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Write32(pioBase0+0xD0, 1<<15|1<<30) // PUT clears FJOIN_TX
	if got := c.Read32(pioBase0 + 0xD0); got&(1<<30|1<<31) != 0 {
		t.Fatalf("PUT did not clear FIFO joins: %#x", got)
	}
	s.isr = 0x13579bdf
	c.Write32(pioBase0+0xD8, 0x801A) // mov rxfifo[2], isr
	if got := c.Read32(pioBase0 + 0x130); got != s.isr {
		t.Fatalf("PUTGET2=%#x, want %#x", got, s.isr)
	}
	if got := c.Read32(pioBase0 + 0x04); got&(1<<0|1<<8) != 1<<0|1<<8 {
		t.Fatalf("random RX storage should read full and empty: %#x", got)
	}

	c.Write32(pioBase0+0xD0, 1<<14) // GET
	c.Write32(pioBase0+0x12C, 0x2468ace0)
	c.Write32(pioBase0+0xD8, 0x8099) // mov osr, rxfifo[1]
	if s.osr != 0x2468ace0 || s.outCount != 0 {
		t.Fatalf("GET: osr=%#x count=%d", s.osr, s.outCount)
	}

	c.Write32(pioBase0+0xD0, 1<<15|1<<14) // PUTGET: processor access disabled
	c.Write32(pioBase0+0x128, 0xdeadbeef)
	if got := c.Read32(pioBase0 + 0x128); got != 0 {
		t.Fatalf("processor accessed PUTGET scratch storage: %#x", got)
	}
	s.isr = 0x10203040
	c.Write32(pioBase0+0xD8, 0x8018) // mov rxfifo[0], isr
	c.Write32(pioBase0+0xD8, 0x8098) // mov osr, rxfifo[0]
	if s.osr != 0x10203040 {
		t.Fatalf("state-machine PUTGET round trip=%#x", s.osr)
	}
}

func TestPIOStatusSelect(t *testing.T) {
	tests := []struct {
		name string
		sel  uint32
		n    uint32
		tx   int
		rx   int
		irq  uint32
		want uint32
	}{
		{name: "tx below", sel: 0, n: 3, tx: 2, want: ^uint32(0)},
		{name: "tx equal", sel: 0, n: 3, tx: 3},
		{name: "rx below", sel: 1, n: 2, rx: 1, want: ^uint32(0)},
		{name: "rx equal", sel: 1, n: 2, rx: 2},
		{name: "irq set", sel: 2, n: 5, irq: 1 << 5, want: ^uint32(0)},
		{name: "irq clear", sel: 2, n: 5},
		{name: "prev pio irq", sel: 2, n: 9, irq: 1 << 1, want: ^uint32(0)},
		{name: "next pio irq", sel: 2, n: 17, irq: 1 << 1, want: ^uint32(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPIOConformanceChip()
			s := &c.pio[0].sm[0]
			s.txN, s.rxN = tt.tx, tt.rx
			s.execctrl = tt.sel<<5 | tt.n
			if tt.sel == 2 {
				p := c.pio[0]
				if tt.n >= 8 && tt.n < 16 {
					p = c.pio[2]
				} else if tt.n >= 16 {
					p = c.pio[1]
				}
				p.irq = tt.irq
			}
			if got := s.movStatus(); got != tt.want {
				t.Fatalf("STATUS=%#x, want %#x", got, tt.want)
			}
		})
	}
}

func TestPIOInlineOutEnable(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	c.Pins.Func[0] = pioFuncSel(0)
	c.Pins.OE |= 1 << 0
	c.Write32(pioBase0+0xDC, 1<<20) // OUT GP0, one pin
	c.Write32(pioBase0+0xD0, 1<<19) // shift OUT right
	c.Write32(pioBase0+0xCC, 1<<18) // INLINE_OUT_EN, select OUT data bit 0
	s.osr = 2                       // data bit 0 is high, selected enable bit 5 is low
	s.outCount = 0
	s.execute(0x6001) // out pins, 1
	if c.Pins.Level(0) {
		t.Fatal("INLINE_OUT_EN=0 still wrote pins")
	}
	s.osr = 1<<5 | 1
	s.outCount = 0
	s.execute(0x6001)
	if !c.Pins.Level(0) {
		t.Fatal("INLINE_OUT_EN=1 did not write pins")
	}

	c.Write32(pioBase0+0xCC, 1<<18|1<<17) // sticky, inline enable bit 0
	s.osr = 1
	s.outCount = 0
	s.execute(0x6001)
	if !c.Pins.Level(0) {
		t.Fatal("sticky OUT did not assert pin")
	}
	s.osr = 0
	s.outCount = 0
	s.execute(0x6001)
	if c.Pins.Level(0) {
		t.Fatal("sticky OUT with inline enable low did not deassert pin")
	}
}

func TestPIORestartAndDebugReadback(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	s := &p.sm[0]
	p.mem[0] = 0xE001
	p.mem[1] = 0xE002
	s.pc = 1
	s.x, s.y, s.osr = 0x11, 0x22, 0x33
	s.isr = 0x44
	s.inCount, s.outCount = 7, 9
	s.delay = 3
	c.Write32(pioBase0+0xCC, 1<<30|1<<29|1<<17) // side enable/dir, sticky
	c.Write32(pioBase0+0xDC, 1<<29|1)
	c.Write32(pioBase0+0xD8, 0x8001) // forced out pins, 1 side 1
	c.Pins.Func[0] = pioFuncSel(0)
	c.Write32(pioBase0, 1<<4) // SM_RESTART
	if s.pc != 0 || s.isr != 0 || s.inCount != 0 || s.outCount != 32 || s.delay != 0 {
		t.Fatalf("restart state: pc=%d isr=%#x in=%d out=%d delay=%d", s.pc, s.isr, s.inCount, s.outCount, s.delay)
	}
	if s.x != 0x11 || s.y != 0x22 || s.osr != 0x33 {
		t.Fatalf("restart clobbered preserved registers: x=%#x y=%#x osr=%#x", s.x, s.y, s.osr)
	}
	if got := c.Read32(pioBase0 + 0x44); got != 0x10200404 {
		t.Fatalf("DBG_CFGINFO=%#x", got)
	}
	if got := c.Read32(pioBase0 + 0x3C); got != uint32(p.outVal) {
		t.Fatalf("DBG_PADOUT=%#x, want %#x", got, uint32(p.outVal))
	}
	if got := c.Read32(pioBase0 + 0x40); got != uint32(p.outOE) {
		t.Fatalf("DBG_PADOE=%#x, want %#x", got, uint32(p.outOE))
	}
	if got := c.Read32(pioBase0 + 0xD8); got != uint32(p.mem[s.pc]) {
		t.Fatalf("INSTR readback=%#x, want %#x", got, p.mem[s.pc])
	}
	c.Write32(pioBase0+0xD4, 9) // ADDR is read-only
	if s.pc != 0 {
		t.Fatalf("ADDR write changed PC to %d", s.pc)
	}
}

func TestPIONextPrevControls(t *testing.T) {
	c := newPIOConformanceChip()
	p0, p1, p2 := c.pio[0], c.pio[1], c.pio[2]
	for _, p := range []*pioBlock{p0, p1, p2} {
		c.Write32(p.block.base+0xC8, 2<<16)
	}
	// Restart both neighbours' dividers and enable SM0/SM1 in each.
	v := uint32(1<<26 | 1<<24 | 1<<20 | 1<<21 | 1<<16 | 1<<17)
	c.Write32(pioBase0, v)
	for _, p := range []*pioBlock{p1, p2} {
		if p.sm[0].acc != 0 || p.sm[1].acc != 0 {
			t.Fatalf("neighbour divider was not restarted: acc=%d/%d", p.sm[0].acc, p.sm[1].acc)
		}
		if !p.sm[0].enabled || !p.sm[1].enabled {
			t.Fatalf("neighbour enables: sm0=%v sm1=%v", p.sm[0].enabled, p.sm[1].enabled)
		}
	}
	// A separate disable operation stops only SM1 in the next block.
	c.Write32(pioBase0, 1<<25|1<<21)
	if !p1.sm[0].enabled || p1.sm[1].enabled || !p2.sm[1].enabled {
		t.Fatalf("NEXTPREV disable: next=%v/%v prev=%v/%v", p1.sm[0].enabled, p1.sm[1].enabled, p2.sm[0].enabled, p2.sm[1].enabled)
	}
	if got := c.Read32(pioBase0); got&0x0fff0000 != 0 {
		t.Fatalf("NEXTPREV strobes latched: %#x", got)
	}
}

func TestPIOInputSynchronizers(t *testing.T) {
	c := newPIOConformanceChip()
	p := c.pio[0]
	c.Pins.SetExternal(0, true)
	p.tick()
	if got := p.sm[0].readPins(0); got&1 != 0 {
		t.Fatalf("input appeared after one synchronizer cycle: %#x", got)
	}
	p.tick()
	if got := p.sm[0].readPins(0); got&1 != 1 {
		t.Fatalf("input did not appear after two synchronizer cycles: %#x", got)
	}
	c.Pins.SetExternal(0, false)
	p.tick()
	if got := p.sm[0].readPins(0); got&1 != 1 {
		t.Fatalf("deassertion appeared early: %#x", got)
	}
	p.tick()
	if got := p.sm[0].readPins(0); got&1 != 0 {
		t.Fatalf("deassertion did not propagate: %#x", got)
	}

	c.Write32(pioBase0+0x38, 1)
	c.Pins.SetExternal(0, true)
	if got := p.sm[0].readPins(0); got&1 != 1 {
		t.Fatalf("bypassed input was delayed: %#x", got)
	}

	c.Write32(pioBase0+0x168, 16)
	c.Write32(pioBase0+0x38, 1) // bypass PIO-relative pin 0 = GP16
	c.Pins.SetExternal(0, false)
	c.Pins.SetExternal(16, true)
	if got := p.sm[0].readPins(0); got&1 != 1 {
		t.Fatalf("GPIOBASE-relative bypass did not select GP16: %#x", got)
	}
}
