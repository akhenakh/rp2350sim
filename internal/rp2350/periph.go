package rp2350

// regBlock is a flat 32-bit register file for a peripheral.
type regBlock struct {
	base uint32
	size uint32
	regs []uint32
	// byteAddressed marks true memory (USB DPRAM or boot RAM), where sub-word
	// writes change only the selected lanes. Peripheral registers instead
	// replicate a byte/halfword write across the containing 32-bit register.
	byteAddressed bool
	// readFn, when set, may override a read. ok=false falls through to regs.
	readFn func(c *Chip, off uint32) (uint32, bool)
	// writeFn is called after the register file has been updated.
	writeFn func(c *Chip, off uint32, v uint32)
	// writeSizeFn handles a sub-word write before the normal size policy.
	// It receives the absolute address and the raw value; returning true
	// consumes the access. Used by write-only registers whose byte order
	// depends on the transfer size (SHA-256 WDATA).
	writeSizeFn func(c *Chip, addr uint32, v uint32, size int) bool
	// setFn, when set, handles a write through the +0x2000 SET alias. Status
	// and write-one-to-clear bits must not be fed back from the read value.
	setFn func(c *Chip, off uint32, v uint32)
	// clearFn, when set, handles a write through the +0x3000 CLR alias. A
	// W1C register needs the written mask rather than the resulting value, so
	// such blocks bypass the default read-modify-write.
	clearFn func(c *Chip, off uint32, v uint32)
	// resetFn, when set, restores any post-zero reset values and peripheral
	// state after the register file has been cleared.
	resetFn func(c *Chip)
	// noAliases excludes memory-like blocks such as USB DPRAM from the RP2350
	// register atomic aliases.
	noAliases bool
}

func newRegBlock(base, size uint32) *regBlock {
	return &regBlock{base: base, size: size, regs: make([]uint32, (size+3)/4)}
}

// reset clears the register file and lets the peripheral restore its
// architectural reset values and internal state.
func (b *regBlock) reset(c *Chip) {
	clear(b.regs)
	if b.resetFn != nil {
		b.resetFn(c)
	}
}

func (b *regBlock) peek(off uint32) uint32 {
	i := off / 4
	if int(i) < len(b.regs) {
		return b.regs[i]
	}
	return 0
}

func (b *regBlock) read(c *Chip, off uint32) uint32 {
	if b.readFn != nil {
		if v, ok := b.readFn(c, off); ok {
			return v
		}
	}
	return b.peek(off)
}

func (b *regBlock) write(c *Chip, off, v uint32) {
	i := off / 4
	if int(i) < len(b.regs) {
		b.regs[i] = v
	}
	if b.writeFn != nil {
		b.writeFn(c, off, v)
	}
}

// ---- RESETS ----

const resetsMask = 0x1FFFFFFF

func newResets(c *Chip) *regBlock {
	b := newRegBlock(0x40020000, 0x1000)
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off == 8 { // RESET_DONE
			return ^b.regs[0] & resetsMask, true
		}
		return 0, false
	}
	return b
}

// ---- CLOCKS ----

func newClocks(c *Chip) *regBlock {
	b := newRegBlock(0x40010000, 0x1000)
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		// The SELECTED register of each of the 10 clock muxes sits at 8 mod 12
		// and is a one-hot of the glitchless source: bit 0 for the primary
		// input, bit 1 for the auxiliary mux. The SDK polls it for equality
		// with 0x1 (primary) and for bit 1 (aux), so a constant would hang
		// clock_configure.
		if off < 10*12 && off%12 == 8 {
			clock := off / 12
			ctrl := b.regs[(off-8)/4]
			// CLK_REF (4) has a two-bit glitchless mux: ROSC, aux, XOSC,
			// LPOSC. CLK_SYS (5) has one bit: clk_ref or aux. SELECTED is a
			// one-hot of the active source, and the SDK polls the exact bit.
			if clock == 4 {
				return 1 << (ctrl & 0x3), true
			}
			return 1 << (ctrl & 0x1), true
		}
		if off == 0xBC || off == 0xC0 { // ENABLED0/1
			return 0xFFFFFFFF, true
		}
		return 0, false
	}
	b.writeFn = func(c *Chip, off, v uint32) {
		if off != 0xA0 { // FC0_SRC starts a measurement
			return
		}
		// The hardware measures for REF_KHZ * INTERVAL cycles; the emulator
		// completes instantly and reports the selected source's frequency.
		b.regs[0xA4/4] = 1 << 4 // STATUS.DONE
		b.regs[0xA8/4] = clockSourceKHz(c, b, v) << 5
	}
	return b
}

// Clock anchors used by the frequency counter. The emulator runs clk_sys at
// 150 MHz and models the Pico SDK's PLL setup; these are the frequencies the
// SDK's clock_configure calls target.
const (
	clkPLLSysKHz = 150000
	clkPLLUSBKHz = 48000
	clkXOSCKHz   = 12000
	clkROSCKHz   = 12000
)

func pllPowered(c *Chip, base uint32) bool {
	return c.Read32(base+4)&0x5 == 0
}

func clockSourceKHz(c *Chip, b *regBlock, src uint32) uint32 {
	switch src {
	case 1: // PLL_SYS_CLKSRC_PRIMARY
		if pllPowered(c, 0x40050000) {
			return clkPLLSysKHz
		}
	case 2, 16: // PLL_USB_CLKSRC_PRIMARY (and its test output)
		if pllPowered(c, 0x40058000) {
			return clkPLLUSBKHz
		}
	case 3, 4:
		return clkROSCKHz
	case 5, 8: // XOSC, clk_ref
		return clkXOSCKHz
	case 9:
		return clockSysKHz(c, b)
	case 10:
		return clockPeriKHz(c, b)
	case 11, 12: // clk_usb, clk_adc
		return clkPLLUSBKHz
	case 13:
		return 125000 // clk_hstx
	case 14:
		return 32 // lposc
	}
	return 0
}

func clockSysKHz(c *Chip, b *regBlock) uint32 {
	ctrl := b.peek(0x3C)
	var base uint32
	if ctrl&1 == 0 { // SRC = clk_ref
		base = clockSourceKHz(c, b, 8)
	} else {
		switch (ctrl >> 5) & 7 {
		case 0:
			base = clockSourceKHz(c, b, 1)
		case 1:
			base = clockSourceKHz(c, b, 2)
		case 2:
			base = clkROSCKHz
		case 3:
			base = clkXOSCKHz
		}
	}
	return base / clockDivider(b.peek(0x40))
}

func clockPeriKHz(c *Chip, b *regBlock) uint32 {
	ctrl := b.peek(0x48)
	if ctrl&(1<<11) == 0 { // ENABLE
		return 0
	}
	var base uint32
	switch (ctrl >> 5) & 7 {
	case 0:
		base = clockSysKHz(c, b)
	case 1:
		base = clockSourceKHz(c, b, 1)
	case 2:
		base = clockSourceKHz(c, b, 2)
	case 3:
		base = clkROSCKHz
	case 4:
		base = clkXOSCKHz
	}
	return base / clockDivider(b.peek(0x4C))
}

func clockDivider(div uint32) uint32 {
	if n := div >> 8 & 0xFF; n != 0 {
		return n
	}
	return 1
}

// ---- XOSC ----

func newXOSC(c *Chip) *regBlock {
	b := newRegBlock(0x40048000, 0x1000)
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off == 4 { // STATUS
			v := uint32(0)
			if b.regs[0]&0xF000 != 0 { // ENABLE field
				v |= 1 << 31 // STABLE
			}
			v |= b.regs[4] // preserve count
			return v, true
		}
		return 0, false
	}
	return b
}

// ---- PLL ----

func newPLL(c *Chip, base uint32) *regBlock {
	b := newRegBlock(base, 0x1000)
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off == 0 { // CS
			v := b.regs[0]
			// LOCK is a PLL status bit, not a function of the CS configuration
			// fields. It sets only when the PLL is powered (PD and VCOPD
			// cleared); a configured CS with PWR.PD still asserted must read
			// LOCK=0 or bring-up firmware spins forever.
			if b.regs[1]&0x5 == 0 {
				v |= 1 << 31 // LOCK
			} else {
				v &^= 1 << 31
			}
			return v, true
		}
		return 0, false
	}
	return b
}

// ---- TICKS ----

func newTicks(c *Chip) *regBlock {
	return newRegBlock(0x40108000, 0x100)
}

// ---- SYSINFO ----

// sysInfoChipID is the RP2350 A3 chip id. The bootrom's SYS_INFO_CHIP_INFO
// selector reports the same value.
const sysInfoChipID = 0x3000_4927

func newSysInfo(c *Chip) *regBlock {
	b := newRegBlock(0x40000000, 0x100)
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x00: // CHIP_ID: revision A3, part RP4, manufacturer 0x926
			return sysInfoChipID, true
		case 0x04: // PACKAGE_SEL: 1 = QFN60 (RP2350A), 0 = QFN80 (RP2350B)
			return c.packageSel, true
		case 0x08: // PLATFORM: ASIC, not FPGA/SIM
			return 0, true
		}
		return 0, false
	}
	return b
}

// ---- TBMAN ----

// newTBMAN reports the ASIC platform. The SDK's running_on_fpga() consults
// this block; a zero read is not a valid platform value.
func newTBMAN(c *Chip) *regBlock {
	b := newRegBlock(0x40160000, 0x100)
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off == 0 {
			return 1, true // PLATFORM_ASIC
		}
		return 0, false
	}
	return b
}

// newROSC models the ring oscillator's random bit source.
func newROSC(c *Chip) *regBlock {
	b := newRegBlock(0x400E8000, 0x1000)
	var state uint32 = 0x12345678
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		if off == 0x1C { // STATUS
			return 1 << 31, true // ROSC_RUNNING
		}
		if off == 0x20 { // RANDOMBIT
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			return state & 1, true
		}
		return 0, false
	}
	b.resetFn = func(c *Chip) { state = 0x12345678 }
	return b
}

// ---- BOOTRAM / bootrom boot locks ----
//
// The bootrom keeps eight spinlocks and two write-once registers in a block
// next to its RAM. The Pico SDK's runtime claims a lock while it initialises
// the bootrom state, so a plain zero register file would hang startup.
const (
	bootramBase       = 0x400e0000
	bootramWriteOnce0 = 0x800
	bootramWriteOnce1 = 0x804
	bootramLockStat   = 0x808
	bootramLock0      = 0x80c
	bootramLockCount  = 8
	bootramUnclaimed  = 0xff
)

func newBootRAM(c *Chip) *regBlock {
	b := newRegBlock(bootramBase, 0x1000)
	b.byteAddressed = true
	// The bootrom stages a boot2 stub at the base of boot RAM. The SDK copies
	// it to SRAM and calls it from flash_start_xip(); a bare bx lr is enough
	// because XIP is always usable in this model.
	b.regs[0] = 0x0000_4770
	unclaimed := uint8(bootramUnclaimed)
	var writeOnce [2]uint32
	b.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch {
		case off == bootramWriteOnce0 || off == bootramWriteOnce1:
			return writeOnce[(off-bootramWriteOnce0)/4], true
		case off == bootramLockStat:
			return uint32(unclaimed), true
		case off >= bootramLock0 && off < bootramLock0+bootramLockCount*4:
			n := (off - bootramLock0) / 4
			if unclaimed&(1<<n) != 0 {
				unclaimed &^= 1 << n
				return 1 << n, true
			}
			return 0, true
		}
		return 0, false
	}
	b.writeFn = func(c *Chip, off, v uint32) {
		switch {
		case off == bootramWriteOnce0 || off == bootramWriteOnce1:
			writeOnce[(off-bootramWriteOnce0)/4] |= v
		case off == bootramLockStat:
			unclaimed = uint8(v)
		case off >= bootramLock0 && off < bootramLock0+bootramLockCount*4:
			n := (off - bootramLock0) / 4
			unclaimed |= 1 << n
		}
	}
	b.resetFn = func(c *Chip) {
		b.regs[0] = 0x0000_4770 // boot2 stub
		unclaimed = bootramUnclaimed
		writeOnce = [2]uint32{}
	}
	return b
}
