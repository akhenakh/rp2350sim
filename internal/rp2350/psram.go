package rp2350

// PSRAM on the RP2350 QMI interface.
//
// Chip select 0 (window 0, 0x10000000) carries the boot flash. Chip select 1
// (window 1) is an optional PSRAM device at 0x11000000 with the same 16 MiB
// span; the uncached windows mirror it like they mirror flash. Data is
// readable at reset but writable only after XIP_CTRL.WRITABLE_M1 is set, which
// is what the Pico SDK's hardware_psram does.
//
// The QMI register block is modelled enough for the SDK: direct mode accepts
// command bytes without stalling and answers the 0x9F JEDEC ID read with an
// APS6404-compatible 8 MiB response, while the M0/M1 window registers are
// plain storage.

const (
	// PSRAMBase is QMI window 1, immediately after the 16 MiB flash window.
	PSRAMBase        = 0x11000000
	PSRAMWindowSize  = 0x01000000
	PSRAMMaxSize     = 16 * 1024 * 1024
	PSRAMNoCacheBase = 0x15000000
	PSRAMMaintBase   = 0x19000000
	PSRAMNoXlateBase = 0x1d000000

	qmiBase           = 0x400d0000
	xipBase           = 0x400c8000
	xipCtrlWritableM1 = 1 << 11

	// PSRAM ID bytes expected by hardware_psram's detect path. The SDK reads
	// eight bytes and uses rxbuffer[5] (KGD) and [6] (EID); EID 0x26 maps to
	// 8 MiB for an APS6404.
	psramIDKGD = 0x5d
	psramIDEID = 0x26

	bootramFlashDevinfo = 0xc00
	bootromDevinfoPtr   = 0x7700
)

// flashJEDEC is the JEDEC ID returned when direct mode selects CS0 (the
// on-board W25Q128 flash).
var flashJEDEC = []byte{0xEF, 0x40, 0x18}

// qmiBlock holds the QMI direct-mode state in addition to the register file.
type qmiBlock struct {
	block   *regBlock
	enabled bool
	idMode  bool
	idIndex int
	rx      []byte
}

func newQMI() *qmiBlock {
	q := &qmiBlock{block: newRegBlock(qmiBase, 0x1000)}
	q.resetRegisters()

	q.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x00: // DIRECT_CSR: never busy, RXEMPTY tracks the response queue.
			v := q.block.regs[0]
			v &^= 1 << 1 // BUSY
			if len(q.rx) == 0 {
				v |= 1 << 16 // RXEMPTY
			}
			return v, true
		case 0x08: // DIRECT_RX
			if len(q.rx) == 0 {
				return 0, true
			}
			b := q.rx[0]
			q.rx = q.rx[1:]
			return uint32(b), true
		}
		return 0, false
	}
	q.block.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0x00:
			en := v&1 != 0
			if en && !q.enabled {
				q.rx = q.rx[:0]
				q.idMode = false
				q.idIndex = 0
			}
			q.enabled = en
		case 0x04:
			if !q.enabled {
				return
			}
			// 0x9F is the JEDEC ID read. The firmware clocks seven more
			// bytes; positions 5 and 6 are KGD and EID.
			if !q.idMode && byte(v) == 0x9f {
				q.idMode = true
				q.idIndex = 0
			}
			if q.idMode {
				resp := byte(0)
				csr := q.block.regs[0]
				cs1 := csr&(1<<3|1<<7) != 0 // ASSERT_CS1N or AUTO_CS1N
				cs0 := csr&(1<<2|1<<6) != 0 // ASSERT_CS0N or AUTO_CS0N
				switch {
				case cs1:
					switch q.idIndex {
					case 5:
						resp = psramIDKGD
					case 6:
						resp = psramIDEID
					}
				case cs0:
					// Direct mode also probes the boot flash. Return the
					// on-board W25Q128 JEDEC ID rather than the PSRAM one.
					if q.idIndex < len(flashJEDEC) {
						resp = flashJEDEC[q.idIndex]
					}
				}
				q.rx = append(q.rx, resp)
				q.idIndex++
			}
		}
	}
	q.block.resetFn = func(c *Chip) { q.reset() }
	return q
}

func (q *qmiBlock) resetRegisters() {
	q.block.regs[0] = 0x01800000      // DIRECT_CSR reset
	q.block.regs[0x0c/4] = 0x40000004 // M0_TIMING reset
	q.block.regs[0x10/4] = 0x00001000 // M0_RFMT reset
	q.block.regs[0x14/4] = 0x0000a003 // M0_RCMD reset
	q.block.regs[0x18/4] = 0x00001000 // M0_WFMT reset
	q.block.regs[0x1c/4] = 0x0000a002 // M0_WCMD reset
	q.block.regs[0x20/4] = 0x40000004 // M1_TIMING reset
	q.block.regs[0x24/4] = 0x00001000 // M1_RFMT reset
	q.block.regs[0x28/4] = 0x0000a003 // M1_RCMD reset
	q.block.regs[0x2c/4] = 0x00001000 // M1_WFMT reset
	q.block.regs[0x30/4] = 0x0000a002 // M1_WCMD reset
	for i := range 8 {
		q.block.regs[(0x34+uint32(i)*4)/4] = 0x04000000 + uint32(i)*0x400
	}
}

func (q *qmiBlock) reset() {
	q.enabled = false
	q.idMode = false
	q.idIndex = 0
	q.rx = q.rx[:0]
	q.block.regs[0x04/4] = 0
	q.block.regs[0x08/4] = 0
	q.resetRegisters()
}

// newXIPCtrl models the cache control register that gates writes to window 1.
func newXIPCtrl(c *Chip) *regBlock {
	b := newRegBlock(xipBase, 0x1000)
	b.regs[0] = 0x83
	b.writeFn = func(c *Chip, off, v uint32) {
		if off == 0 {
			c.psramWritable = v&xipCtrlWritableM1 != 0
		}
	}
	b.resetFn = func(c *Chip) {
		b.regs[0] = 0x83
		c.psramWritable = false
	}
	return b
}

// ConfigurePSRAM attaches a PSRAM device on QMI chip select 1. sizeBytes must
// be a power of two up to 16 MiB; 0 detaches it. latencyCycles is the extra
// virtual time charged for each PSRAM access, modelling the external QSPI
// part being slower than on-chip SRAM.
func (c *Chip) ConfigurePSRAM(sizeBytes, latencyCycles uint32, csPin int) {
	if sizeBytes > PSRAMMaxSize || sizeBytes&(sizeBytes-1) != 0 {
		sizeBytes = 0
	}
	c.psramSize = sizeBytes
	c.psramLatency = latencyCycles
	c.psramCS = csPin
	if sizeBytes == 0 {
		c.PSRAM = nil
	} else {
		c.PSRAM = make([]byte, sizeBytes)
	}
	c.syncPSRAMDevinfo()
}

// PSRAMSize returns the configured PSRAM size in bytes, or 0.
func (c *Chip) PSRAMSize() uint32 { return c.psramSize }

// PSRAMLatency returns the per-access stall in virtual cycles.
func (c *Chip) PSRAMLatency() uint32 { return c.psramLatency }

// psramDevinfoSize converts a byte size to the FLASH_DEVINFO size code.
func psramDevinfoSize(size uint32) uint16 {
	if size == 0 {
		return 0
	}
	code := uint16(0)
	for s := uint32(8 * 1024); s < size; s <<= 1 {
		code++
	}
	return code + 1
}

// syncPSRAMDevinfo publishes the attached device to boot RAM, where the SDK's
// flash_devinfo accessors read it. The bootrom does this from OTP at reset on
// real hardware.
func (c *Chip) syncPSRAMDevinfo() {
	// CS0 is always the 16 MiB boot flash; CS1 is absent unless configured.
	devinfo := uint16(12) << 8
	if c.psramSize > 0 {
		cs := uint16(c.psramCS)
		if c.psramCS < 0 || c.psramCS > 63 {
			cs = 0
		}
		devinfo |= psramDevinfoSize(c.psramSize)<<12 | cs
	}
	c.writeMMIO16(bootramBase+bootramFlashDevinfo, devinfo)
}

// resetPSRAM restores the QMI/XIP reset state and republishes the devinfo.
func (c *Chip) resetPSRAM() {
	if c.xipCtrl != nil {
		c.xipCtrl.regs[0] = 0x83
	}
	c.psramWritable = false
	if c.qmi != nil {
		c.qmi.reset()
	}
	c.syncPSRAMDevinfo()
}

// xipMaintenance reports the cache maintenance window. Writes there are
// operations, not memory: the SDK cleans/invalidates the cache by writing to
// 0x18000000+offset, including a set/way sweep at 0x1bffc000.
func xipMaintenance(addr uint32) bool {
	return addr >= XIPMaintenanceBase && addr < XIPNoTranslateBase
}

// psramAlias maps the cached and uncached QMI window-1 views. The maintenance
// view at PSRAMMaintBase is deliberately absent: it carries cache operations,
// not data, so aliasing it would let a ranged maintenance sweep overwrite the
// PSRAM array.
func psramAlias(addr uint32) (uint32, bool) {
	switch {
	case addr >= PSRAMBase && addr < PSRAMBase+PSRAMWindowSize:
		return addr - PSRAMBase, true
	case addr >= PSRAMNoCacheBase && addr < PSRAMNoCacheBase+PSRAMWindowSize:
		return addr - PSRAMNoCacheBase, true
	case addr >= PSRAMNoXlateBase && addr < PSRAMNoXlateBase+PSRAMWindowSize:
		return addr - PSRAMNoXlateBase, true
	}
	return 0, false
}

// psramAccess charges the external latency. The clock is the same virtual
// 150 MHz counter the peripherals use, so a slow PSRAM also pushes timers and
// DMA forward exactly as a real external memory stall would. The per-cycle
// engines (PWM, DMA, PIO, SHA, HSTX) advance with it; cycle-counted registers
// read Cycles directly and catch up on the next tickPeripherals.
func (c *Chip) psramAccess() {
	c.advanceEngines(uint64(c.psramLatency))
}

// advanceEngines moves the per-cycle engines forward by cycles. It exists for
// virtual-time charges outside Step, chiefly a PSRAM stall in the middle of an
// instruction; those engines would otherwise miss the elapsed cycles while
// timers advanced.
func (c *Chip) advanceEngines(cycles uint64) {
	if cycles == 0 {
		return
	}
	if c.psramStall {
		// A DMA engine ticked below is itself reading PSRAM. The outer loop
		// already owns the clock and will keep advancing the engines.
		c.Cycles += cycles
		return
	}
	c.psramStall = true
	for range cycles {
		c.Cycles++
		if c.pwm != nil && c.pwm.busy() {
			c.pwm.tick()
		}
		if c.dma != nil && c.dma.busy() {
			c.dma.tick()
		}
		if c.sha != nil {
			c.sha.tick(c)
		}
		if c.hstx != nil {
			c.hstx.tick()
		}
		pioInputs := c.pioInputLevels()
		for _, p := range c.pio {
			if p != nil {
				p.tickWithInputs(pioInputs)
			}
		}
		for _, h := range c.CycleHooks {
			h()
		}
		if c.Cycles >= c.periodicNext {
			c.runPeriodicHooks()
		}
	}
	c.psramStall = false
}
