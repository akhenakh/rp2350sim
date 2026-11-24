package rp2350

import (
	"testing"
)

const sramCode = uint32(0x20000000)

// step16 executes one 16-bit instruction from SRAM on core 0 with core 1
// parked.
func step16(c *Chip, hw uint16) {
	c.CPU1.Parked = true
	c.Write16(sramCode, hw)
	c.CPU0.R[15] = sramCode
	c.Step()
}

func TestResetRestoresPeripherals(t *testing.T) {
	c := New()
	c.usb.attached = true
	c.usb.state = usbHostRunning
	c.dma.intr = 1
	c.dma.active = 1
	c.pio[0].irq = 1
	c.pio[0].mem[0] = 0xE001
	c.uart[0].imsc = uartRXIM
	c.uart[0].raw = uartRXIM
	c.Read32(bootramBase + bootramLock0)
	c.Write32(pwmBase+7*pwmStride, 1)
	c.Reset()

	if c.usb.attached || c.usb.state != usbHostDetached {
		t.Fatal("USB state survived reset")
	}
	if c.dma.intr != 0 || c.dma.active != 0 {
		t.Fatal("DMA state survived reset")
	}
	if c.pio[0].irq != 0 || c.pio[0].mem[0] != 0 {
		t.Fatal("PIO state survived reset")
	}
	if c.uart[0].imsc != 0 || c.uart[0].raw != 0 {
		t.Fatal("UART state survived reset")
	}
	if got := c.Read32(pwmBase + 7*pwmStride + 0x10); got != 0xFFFF {
		t.Fatalf("PWM TOP after reset = %#x", got)
	}
	if got := c.Read32(bootramBase + bootramLockStat); got != bootramUnclaimed {
		t.Fatalf("BOOTRAM locks after reset = %#x", got)
	}
	if got := c.Read32(SCSBase + 0xD14); got != 0x201 {
		t.Fatalf("CCR after reset = %#x", got)
	}
}

func TestIdleSkipAppliesPendingReset(t *testing.T) {
	c := New()
	c.Write32(0x400D8004, 100)          // WATCHDOG LOAD = 100 us
	c.Write32(0x400D8000, wdCtrlEnable) // enable
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	before := c.BootCount()
	c.Run(400 * cyclesPerMicrosecond)
	if c.BootCount() == before {
		t.Fatal("watchdog expired while the core slept but no reset was applied")
	}
}

func TestPWMInversionAndSliceMap(t *testing.T) {
	cases := map[int]int{15: 7, 24: 4, 25: 4, 30: 7, 32: 8, 39: 11, 40: 8, 47: 11}
	for pin, want := range cases {
		if got := pinSlice(pin); got != want {
			t.Fatalf("pinSlice(%d) = %d, want %d", pin, got, want)
		}
	}
	c := New()
	// Slice 7, channel B (pin 15).
	const pin = 15
	c.Pins.Func[pin] = pwmFuncSel
	base := uint32(pwmBase + 7*pwmStride)
	c.Write32(base+0x10, 999) // TOP
	c.Write32(base+0x0C, 250<<16)
	c.Write32(base+0x00, 1) // EN
	if got := c.PinBrightness(pin); got < 0.24 || got > 0.26 {
		t.Fatalf("plain duty = %v", got)
	}
	// DIVMODE level mode turns B into an input, so it is no longer a PWM output.
	c.Write32(base+0x00, 1|1<<4)
	if got := c.PinBrightness(pin); got != 0 {
		t.Fatalf("DIVMODE B-input brightness: %v", got)
	}
	c.Write32(base+0x00, 1|1<<3) // B_INV
	if got := c.PinBrightness(pin); got < 0.74 || got > 0.76 {
		t.Fatalf("B_INV duty = %v, want ~0.75", got)
	}
	// TOP=0 is a one-cycle period, not 0xFFFF. Program the double-buffered
	// registers while the slice is stopped.
	c.Write32(base+0x00, 0)
	c.Write32(base+0x10, 0)
	c.Write32(base+0x0C, 0)
	c.Write32(base+0x00, 1)
	if got := c.PinBrightness(pin); got != 0 {
		t.Fatalf("TOP=0 duty = %v, want 0", got)
	}
}

func TestDMAAliasesAndINTSAck(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	const src, dst = uint32(0x20000000), uint32(0x20000100)
	for i := range 4 {
		c.Write8(src+uint32(i), byte(0xA0+i))
	}
	ctrl := uint32(dmaCtrlEN | dmaCtrlINCRRead | dmaCtrlINCRWrit)
	c.Write32(0x50000010, ctrl) // AL1_CTRL (no trigger)
	c.Write32(0x50000008, 4)    // TRANS_COUNT
	c.Write32(0x50000004, dst)  // WRITE_ADDR
	c.Write32(0x5000003C, src)  // AL3_READ_ADDR_TRIG
	c.Write32(0x50000404, 1)    // INTE0 channel 0
	if c.dma.active&1 == 0 {
		t.Fatal("AL3_READ_ADDR_TRIG did not start channel 0")
	}
	for range 4 {
		c.Step()
	}
	for i := range 4 {
		if got := c.Read8(dst + uint32(i)); got != byte(0xA0+i) {
			t.Fatalf("byte %d = %#x", i, got)
		}
	}
	if c.NVIC.pending[0]&(1<<10) == 0 {
		t.Fatal("completion interrupt not pending")
	}
	// The SDK acknowledges through INTS0 (0x40c), not INTR.
	c.Write32(0x5000040C, 1)
	if c.dma.intr&1 != 0 {
		t.Fatal("INTS0 write did not acknowledge the channel")
	}
	if c.NVIC.pending[0]&(1<<10) != 0 {
		t.Fatal("NVIC line still pending after INTS0 acknowledge")
	}
}

func TestPIOIRQWaitAndRouting(t *testing.T) {
	c := New()
	p := c.pio[0]
	p.mem[0] = 0xC020 // irq wait 0
	p.writeCtrl(1)    // enable SM0
	p.tick()
	if p.irq&1 == 0 {
		t.Fatal("irq wait did not raise the flag")
	}
	if p.sm[0].pc != 0 {
		t.Fatalf("irq wait advanced PC to %d before the flag was cleared", p.sm[0].pc)
	}
	p.tick()
	if p.sm[0].pc != 0 {
		t.Fatal("irq wait did not stall while the flag stayed set")
	}
	p.write(c, 0x30, 1) // host clears IRQ0
	p.tick()
	if p.sm[0].pc == 0 {
		t.Fatal("irq wait did not resume after the flag was cleared")
	}
	if p.irq&1 != 0 {
		t.Fatal("irq wait cleared the flag itself")
	}

	// IRQ0_INTE routes the shared flag to NVIC 15.
	c.Write32(pioBase0+0x30, 1) // make sure the flag is clear
	c.Write32(pioBase0+pioIRQ0INTE, 1)
	p.mem[0] = 0xC000     // irq set 0
	p.writeCtrl(1 | 1<<4) // enable and restart SM0 to PC=0
	for range 1 {
		p.tick()
	}
	if p.irq&1 == 0 {
		t.Fatal("irq set did not raise the flag")
	}
	if c.NVIC.pending[0]&(1<<15) == 0 {
		t.Fatal("PIO IRQ0 did not reach the NVIC")
	}
	c.Write32(pioBase0+0x30, 1)
	if c.NVIC.pending[0]&(1<<15) != 0 {
		t.Fatal("clearing the PIO flag left IRQ0 pending")
	}
}

func TestPIOCountZeroMeansNoPins(t *testing.T) {
	c := New()
	p := c.pio[0]
	s := &p.sm[0]
	s.pinctrl = 0 // OUT_COUNT=0, SET_COUNT=0
	s.outCount = 0
	s.osr = 1
	s.execSet(0xE001) // set pins, 1
	if p.outVal != 0 {
		t.Fatalf("SET_COUNT=0 wrote pins: %#x", p.outVal)
	}
	if got := s.outCountPins(); got != 0 {
		t.Fatalf("outCountPins = %d, want 0", got)
	}
	s.execOut(0x6001) // out pins, 1
	if p.outVal != 0 {
		t.Fatalf("OUT_COUNT=0 wrote pins: %#x", p.outVal)
	}
}

func TestSPI16BitAndOverrun(t *testing.T) {
	c := New()
	var got []byte
	c.OnSPI0 = func(tx byte) byte {
		got = append(got, tx)
		if tx == 0x12 {
			return 0xAB
		}
		return 0xCD
	}
	c.Write32(0x40080000, 15) // SSPCR0 DSS=16
	c.Write32(0x40080008, 0x1234)
	if len(got) != 2 || got[0] != 0x12 || got[1] != 0x34 {
		t.Fatalf("16-bit frame bytes = % x", got)
	}
	if v := c.Read32(0x40080008); v != 0xABCD {
		t.Fatalf("16-bit RX = %#x", v)
	}

	c.Write32(0x40080000, 7) // DSS=8
	for i := range 9 {
		c.Write32(0x40080008, uint32(i))
	}
	if v := c.Read32(0x40080018); v&1 == 0 {
		t.Fatal("RX overrun did not set ROR")
	}
	if v := c.Read32(0x40080008); v != 0xCD {
		t.Fatalf("overrun kept the newest byte: first RX = %#x, want the first frame 0xCD", v)
	}
	c.Write32(0x40080020, 1) // SSPICR RORIC
	if v := c.Read32(0x40080018); v&1 != 0 {
		t.Fatal("RORIC did not clear the overrun")
	}
}

func TestPSRAMLatencyDoesNotSkipMicrosecondTick(t *testing.T) {
	c := New()
	c.ConfigurePSRAM(1024*1024, 40, 8)
	c.Cycles = 140
	c.lastMicros = 0
	c.Write32(0x400B0010, 1) // TIMER0 alarm 0 = 1 us
	c.Read32(0x11000000)     // charges 40 cycles, crossing 150
	c.Step()
	if c.tmr[0].intR&1 == 0 {
		t.Fatal("timer alarm at 1 us was skipped by the PSRAM latency jump")
	}
}

func TestQMICSSelectsFlashID(t *testing.T) {
	c := New()
	c.ConfigurePSRAM(8*1024*1024, 0, 8)
	c.Write32(qmiBase, 1|1<<2) // EN | ASSERT_CS0N
	c.Write32(qmiBase+4, 0x9F)
	c.Write32(qmiBase+4, 0xFF)
	c.Write32(qmiBase+4, 0xFF)
	var got [3]byte
	for i := range got {
		got[i] = byte(c.Read32(qmiBase + 8))
	}
	if got != [3]byte{0xEF, 0x40, 0x18} {
		t.Fatalf("CS0 JEDEC ID = % x", got)
	}
}

func TestVTORBankedAndVECTACTIVE(t *testing.T) {
	c := New()
	c.core = 0
	c.Write32(SCSBase+0xD08, 0x10000000)
	c.core = 1
	c.Write32(SCSBase+0xD08, 0x20000000)
	if c.CPU0.VTOR != 0x10000000 || c.CPU1.VTOR != 0x20000000 {
		t.Fatalf("VTOR banked incorrectly: core0=%#x core1=%#x", c.CPU0.VTOR, c.CPU1.VTOR)
	}
	c.CPU1.IPSR = 15
	if got := c.Read32(SCSBase+0xD04) & 0x1FF; got != 15 {
		t.Fatalf("VECTACTIVE on core 1 = %d", got)
	}
}

func TestClearAliasOnW1CRegisters(t *testing.T) {
	c := New()
	c.tmr[0].intR = 1
	c.Write32(0x400B0000+0x3000+0x3C, 1) // TIMER0.INTR through CLR
	if c.tmr[0].intR&1 != 0 {
		t.Fatal("CLR alias did not clear TIMER0.INTR")
	}
	c.dma.intr = 1
	c.Write32(0x50000000+0x3000+0x400, 1) // DMA.INTR through CLR
	if c.dma.intr&1 != 0 {
		t.Fatal("CLR alias did not clear DMA.INTR")
	}
	c.pio[0].irq = 1
	c.Write32(pioBase0+0x3000+0x30, 1)
	if c.pio[0].irq&1 != 0 {
		t.Fatal("CLR alias did not clear PIO.IRQ")
	}
}

func TestExclusiveMonitorCrossCoreWrite(t *testing.T) {
	c := New()
	c.CPU1.Parked = true
	c.Write16(sramCode, 0xE853) // ldrex r0, [r3]
	c.Write16(sramCode+2, 0x0F00)
	c.CPU0.R[3] = sramCode + 0x100
	c.CPU0.R[15] = sramCode
	c.Step()
	if !c.CPU0.MonitorCovers(sramCode + 0x100) {
		t.Fatal("LDREX did not arm the monitor")
	}
	c.Write32(sramCode+0x100, 0) // a write by the other agent (or DMA)
	if c.CPU0.MonitorCovers(sramCode + 0x100) {
		t.Fatal("write did not invalidate the exclusive monitor")
	}
}

func TestExclusiveLossSignalsEventAndWakesWFE(t *testing.T) {
	c := New()
	c.CPU1.Parked = true
	const addr = sramCode + 0x100
	c.Write16(sramCode, 0xE853) // ldrex r0, [r3]
	c.Write16(sramCode+2, 0x0F00)
	c.CPU0.R[3] = addr
	c.CPU0.R[15] = sramCode
	c.Step()
	if !c.CPU0.MonitorCovers(addr) {
		t.Fatal("LDREX did not arm the monitor")
	}

	c.CPU0.Sleeping = true
	c.CPU0.SleepOnEvent = true
	c.Write32(addr, 0)
	if c.CPU0.MonitorCovers(addr) {
		t.Fatal("remote write did not invalidate the monitor")
	}
	if !c.CPU0.EventRegister || c.CPU0.Sleeping {
		t.Fatal("exclusive loss did not signal an event and wake WFE")
	}

	c.CPU0.Sleeping = false
	c.CPU0.R[15] = sramCode
	c.Step() // arm the monitor again with LDREX
	c.CPU0.EventRegister = false
	c.CPU0.Sleeping = true
	c.CPU0.SleepOnEvent = false
	c.Write32(addr, 1)
	if !c.CPU0.EventRegister || !c.CPU0.Sleeping {
		t.Fatal("exclusive loss must set the event register without waking WFI")
	}
}

func TestWFIPendingWithPrimask(t *testing.T) {
	c := New()
	c.NVIC.write(0x100, 1) // ISER0: IRQ 0
	c.NVIC.Pend(0)
	c.CPU0.PRIMASK = 1
	step16(c, 0xBF30) // wfi
	if c.CPU0.Sleeping {
		t.Fatal("WFI slept with an enabled pending IRQ and PRIMASK=1")
	}
}

// TestRunBudgetSurvivesReset guards the local cycle counter in Run: a reset
// rewinds Cycles, and an absolute target would replay the elapsed time (a
// watchdog reboot loop took minutes instead of the requested seconds).
func TestRunBudgetSurvivesReset(t *testing.T) {
	c := New()
	c.CPU0.Parked = true
	c.CPU1.Parked = true
	c.Run(1_000_000)
	if c.Cycles < 1_000_000 {
		t.Fatalf("setup run stopped at %d cycles", c.Cycles)
	}
	c.Write32(0x400D8004, 10) // watchdog LOAD = 10 us
	c.Write32(0x400D8000, wdCtrlEnable)
	c.CPU0.Sleeping = true
	c.Run(5_000)
	if c.BootCount() == 0 {
		t.Fatal("watchdog did not reset")
	}
	if c.Cycles > 10_000 {
		t.Fatalf("Run replayed the pre-reset elapsed time: Cycles=%d", c.Cycles)
	}
}

func TestWatchdogScratchSurvivesReset(t *testing.T) {
	c := New()
	const scratch4 = 0x400D8000 + 0x0c + 4*4
	c.Write32(scratch4, 0x77617463)
	c.Reset()
	if got := c.Read32(scratch4); got != 0x77617463 {
		t.Fatalf("watchdog scratch[4] after reset = %#x", got)
	}
}
