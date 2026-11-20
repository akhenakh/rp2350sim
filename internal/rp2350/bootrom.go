package rp2350

import (
	"fmt"
	"log/slog"

	"github.com/akhenakh/rp2350sim/internal/m33"
)

// Synthetic RP2350 bootrom.
//
// The Pico SDK calls rom_func_lookup() during runtime_init (and uses more ROM
// services for flash and reboot). A real RP2350 answers those calls from a
// 32 KB mask ROM; firmware is entitled to treat the table as opaque and only
// reads the two well-known pointers at 0x14 and 0x18.
//
// The emulator builds that table and backs each entry with a Go function. A
// call lands on a synthetic address inside the ROM range; Chip.stepCore sees
// it before decoding and performs the call as a normal function return. This
// keeps the CPU core free of chip-specific knowledge and lets the stubs use
// the real bus and chip state.

const (
	bootromMagicOffset       = 0x10
	bootromFuncTablePtr      = 0x14 // 16-bit pointer to the (opaque) function table
	bootromLookupPtr         = 0x16 // 16-bit pointer to rom_table_lookup, Thumb bit set
	bootromSyntheticBase     = 0x7000
	bootromSyntheticStride   = 4
	bootromDataBase          = 0x7800
	bootromFuncTableData     = 0x7c00
	bootromMagic             = 0x5a5a_5a5a
	sysInfoBootRandom        = 0x0010
	bootromOK                = 0
	bootromErrNotPermitted   = ^uint32(3)  // -4
	bootromErrInvalidArg     = ^uint32(4)  // -5
	bootromErrInvalidAddr    = ^uint32(9)  // -10
	bootromErrInvalidState   = ^uint32(11) // -12
	bootromErrUnsupportedMod = ^uint32(17) // -18
	bootromRebootReason      = "bootrom reboot requested"
)

// romStub is called with the CPU at the synthetic address. It reads arguments
// from R0-R3, writes its result to R0 and returns to LR.
type romStub func(c *Chip, cpu *m33.CPU)

type bootROM struct {
	chip    *Chip
	hooks   map[uint32]romStub
	funcs   map[uint32]uint32 // code -> synthetic address
	data    map[uint32]uint32 // code -> address of the value in ROM
	next    uint32
	strings []byte
}

// installBootROM fills c.ROM with the table the SDK expects and registers the
// stubs.
func installBootROM(c *Chip) *bootROM {
	b := &bootROM{
		chip:  c,
		hooks: map[uint32]romStub{},
		funcs: map[uint32]uint32{},
		data:  map[uint32]uint32{},
		next:  bootromSyntheticBase,
	}
	lookupAddr := b.addHook(b.lookup)

	write16 := func(off int, v uint16) { c.ROM[off] = byte(v); c.ROM[off+1] = byte(v >> 8) }
	write16(bootromFuncTablePtr, uint16(bootromFuncTableData))
	write16(bootromLookupPtr, uint16(lookupAddr|1))
	put32 := func(off int, v uint32) {
		c.ROM[off] = byte(v)
		c.ROM[off+1] = byte(v >> 8)
		c.ROM[off+2] = byte(v >> 16)
		c.ROM[off+3] = byte(v >> 24)
	}
	put32(bootromMagicOffset, bootromMagic)
	// ROM_DATA_FLASH_DEVINFO16_PTR is a pointer literal to the boot RAM copy
	// of the flash/PSRAM device sizes. The SDK dereferences it during PSRAM
	// initialisation and flash programming.
	put32(bootromDevinfoPtr, bootramBase+bootramFlashDevinfo)
	// The SDK does not inspect the table contents, but keep a plausible
	// header so a firmware that dumps it sees something.
	for i := range 16 {
		put32(bootromFuncTableData+i*4, 0)
	}

	// Memory and maths helpers (RP2350 ROM service codes).
	b.addFunc("MC", b.stubMemcpy)
	b.addFunc("MS", b.stubMemset)
	b.addFunc("C4", b.stubMemcpy)
	b.addFunc("S4", b.stubMemset)
	b.addFunc("L3", b.stubCLZ)
	b.addFunc("P3", b.stubPopcount)
	b.addFunc("R3", b.stubReverse)
	b.addFunc("T3", b.stubCTZ)

	// Startup and system services.
	b.addFunc("SR", b.stubOK) // bootrom_state_reset
	b.addFunc("SS", b.stubOK) // set_bootrom_stack
	b.addFunc("GS", b.stubGetSysInfo)
	b.addFunc("RB", b.stubReboot)
	b.addFunc("UB", b.stubUSBBoot)

	// Flash and XIP services. The emulator maps flash directly, so these only
	// need to update the backing array.
	b.addFunc("RE", b.stubFlashErase)
	b.addFunc("RP", b.stubFlashProgram)
	b.addFunc("FC", b.stubOK)      // flush cache
	b.addFunc("CX", b.stubOK)      // enter command XIP
	b.addFunc("EX", b.stubOK)      // exit XIP
	b.addFunc("IF", b.stubOK)      // connect internal flash
	b.addFunc("FO", b.stubFlashOp) // flash op
	b.addFunc("FA", b.stubOK)      // runtime to storage address
	b.addFunc("RA", b.stubOK)      // reset address translation
	b.addFunc("XM", b.stubOK)      // select XIP read mode

	// Partition and secure-boot services are not modelled; report them as
	// unsupported rather than returning garbage.
	b.addFunc("AB", b.stubUnsupported)
	b.addFunc("CI", b.stubUnsupported)
	b.addFunc("EB", b.stubUnsupported)
	b.addFunc("GB", b.stubUnsupported)
	b.addFunc("GP", b.stubUnsupported)
	b.addFunc("GU", b.stubUnsupported)
	b.addFunc("LP", b.stubUnsupported)
	b.addFunc("RC", b.stubUnsupported)
	b.addFunc("SC", b.stubUnsupported)
	b.addFunc("SP", b.stubUnsupported)
	b.addFunc("VB", b.stubUnsupported)

	// OTP access: reads and writes rows through the fuse model.
	b.addFunc("OA", b.stubOTPAccess)

	// Data services.
	b.addData("CR", b.addString("rp2350sim synthetic bootrom"))
	b.addData("GR", b.addString("rp2350sim"))
	b.addData("FD", bootromDevinfoPtr)
	b.addData("PT", 0)
	b.addData("XF", 0)
	copy(c.ROM[bootromDataBase:], b.strings)
	return b
}

func (b *bootROM) addHook(fn romStub) uint32 {
	addr := b.next
	b.next += bootromSyntheticStride
	b.hooks[addr] = fn
	return addr
}

func (b *bootROM) addFunc(code string, fn romStub) {
	if len(code) != 2 {
		panic("bootrom: code must be two characters")
	}
	b.funcs[romCode(code)] = b.addHook(fn)
}

func (b *bootROM) addData(code string, addr uint32) {
	b.data[romCode(code)] = addr
}

func (b *bootROM) addString(s string) uint32 {
	addr := uint32(bootromDataBase + len(b.strings))
	b.strings = append(b.strings, s...)
	b.strings = append(b.strings, 0)
	return addr
}

func romCode(code string) uint32 { return uint32(code[0]) | uint32(code[1])<<8 }

// exec runs a synthetic ROM call. It reports whether pc was handled.
func (b *bootROM) exec(c *Chip, cpu *m33.CPU, pc uint32) bool {
	stub, ok := b.hooks[pc]
	if !ok {
		return false
	}
	lr := cpu.R[14]
	stub(c, cpu)
	if !cpu.Halted {
		cpu.R[15] = lr &^ 1
	}
	cpu.Cycles++
	return true
}

// lookup implements rom_table_lookup: (code, mask) -> pointer or NULL.
func (b *bootROM) lookup(c *Chip, cpu *m33.CPU) {
	code := cpu.R[0]
	if addr, ok := b.funcs[code]; ok {
		cpu.R[0] = addr | 1 // Thumb function pointer
		c.Logger.Debug("bootrom lookup", slog.String("code", codeName(code)), slog.String("kind", "func"))
		return
	}
	if addr, ok := b.data[code]; ok {
		cpu.R[0] = addr
		c.Logger.Debug("bootrom lookup", slog.String("code", codeName(code)), slog.String("kind", "data"))
		return
	}
	cpu.R[0] = 0
	c.Logger.Warn("bootrom lookup: unsupported code", slog.String("code", codeName(code)))
}

func codeName(code uint32) string {
	return fmt.Sprintf("%c%c (%#04x)", byte(code), byte(code>>8), code)
}

// ---- stubs ----

func (b *bootROM) stubOK(c *Chip, cpu *m33.CPU) { cpu.R[0] = bootromOK }

func (b *bootROM) stubUnsupported(c *Chip, cpu *m33.CPU) {
	c.Logger.Warn("bootrom: unsupported service called", slog.String("pc", fmtHex(cpu.R[15])))
	cpu.R[0] = bootromErrInvalidState
}

func (b *bootROM) stubMemcpy(c *Chip, cpu *m33.CPU) {
	dst, src, n := cpu.R[0], cpu.R[1], cpu.R[2]
	for i := range n {
		c.Write8(dst+i, c.Read8(src+i))
	}
	cpu.R[0] = dst
}

func (b *bootROM) stubMemset(c *Chip, cpu *m33.CPU) {
	dst, val, n := cpu.R[0], byte(cpu.R[1]), cpu.R[2]
	for i := range n {
		c.Write8(dst+i, val)
	}
	cpu.R[0] = dst
}

func (b *bootROM) stubCLZ(c *Chip, cpu *m33.CPU) {
	v := cpu.R[0]
	n := uint32(32)
	for v != 0 {
		n--
		v >>= 1
	}
	cpu.R[0] = n
}

func (b *bootROM) stubCTZ(c *Chip, cpu *m33.CPU) {
	v := cpu.R[0]
	if v == 0 {
		cpu.R[0] = 32
		return
	}
	n := uint32(0)
	for v&1 == 0 {
		n++
		v >>= 1
	}
	cpu.R[0] = n
}

func (b *bootROM) stubPopcount(c *Chip, cpu *m33.CPU) {
	v := cpu.R[0]
	n := uint32(0)
	for v != 0 {
		n += v & 1
		v >>= 1
	}
	cpu.R[0] = n
}

func (b *bootROM) stubReverse(c *Chip, cpu *m33.CPU) {
	v := cpu.R[0]
	var r uint32
	for range 32 {
		r = r<<1 | v&1
		v >>= 1
	}
	cpu.R[0] = r
}

func (b *bootROM) stubGetSysInfo(c *Chip, cpu *m33.CPU) {
	out, size, kind := cpu.R[0], cpu.R[1], cpu.R[2]
	if kind == sysInfoBootRandom && size >= 5 {
		// result[0] is a length word; the SDK copies result[1:5].
		c.Write32(out, 4)
		seed := uint32(c.Cycles) ^ 0x9e3779b9
		for i := range uint32(4) {
			seed = seed*1664525 + 1013904223
			c.Write32(out+4+4*i, seed)
		}
		cpu.R[0] = 5
		return
	}
	cpu.R[0] = 0
}

func (b *bootROM) stubReboot(c *Chip, cpu *m33.CPU) {
	flags, delayMS := cpu.R[0], cpu.R[1]
	if flags != 0 {
		c.Logger.Debug("bootrom reboot type not modelled; resetting to flash",
			slog.String("flags", fmtHex(flags)))
	}
	c.Logger.Debug("bootrom reboot", slog.Uint64("delay_ms", uint64(delayMS)))
	c.requestReset("bootrom", uint64(delayMS)*cyclesPerMillisecond)
	cpu.R[0] = bootromOK
}

func (b *bootROM) stubUSBBoot(c *Chip, cpu *m33.CPU) {
	c.Logger.Warn("bootrom USB boot requested (not modelled)")
	cpu.Halt("bootrom USB boot requested")
}

// OTP command flags from the bootrom otp_access API.
const (
	otpCmdRowMask = 0x0000ffff
	otpCmdWrite   = 0x00010000
	otpCmdECC     = 0x00020000
)

// stubOTPAccess implements otp_access(buf, buf_len, row_and_flags): rows are
// 2 bytes wide with ECC, or 4 bytes wide with the 24-bit raw value in the low
// three bytes. Writes obey fuse semantics, and the first locked or clearing
// row stops the transfer.
func (b *bootROM) stubOTPAccess(c *Chip, cpu *m33.CPU) {
	buf, bufLen, flags := cpu.R[0], cpu.R[1], cpu.R[2]
	row := int(flags & otpCmdRowMask)
	write := flags&otpCmdWrite != 0
	ecc := flags&otpCmdECC != 0
	unit := uint32(4)
	if ecc {
		unit = 2
	}
	count := int(bufLen / unit)
	for i := range count {
		r := row + i
		if r >= OTPRows {
			cpu.R[0] = bootromErrInvalidArg
			return
		}
		off := buf + uint32(i)*unit
		if write {
			var raw uint32
			if ecc {
				raw = otpEncodeRow(uint16(c.Read8(off)) | uint16(c.Read8(off+1))<<8)
			} else {
				raw = uint32(c.Read8(off)) | uint32(c.Read8(off+1))<<8 | uint32(c.Read8(off+2))<<16
			}
			if code := c.otp.writeRow(r, raw); code != bootromOK {
				c.Logger.Debug("otp write failed", slog.Int("row", r), slog.Uint64("code", uint64(code)))
				cpu.R[0] = code
				return
			}
			c.Logger.Debug("otp write", slog.Int("row", r), slog.String("raw", fmtHex(raw)))
			continue
		}
		v, code := c.otp.readRow(r)
		if code != bootromOK {
			c.Logger.Debug("otp read failed", slog.Int("row", r), slog.Uint64("code", uint64(code)))
			cpu.R[0] = code
			return
		}
		if ecc {
			data, ok := otpDecodeRow(v)
			if !ok {
				c.Logger.Warn("otp read: uncorrectable ECC error", slog.Int("row", r), slog.String("raw", fmtHex(v)))
			}
			c.Write8(off, byte(data))
			c.Write8(off+1, byte(data>>8))
			continue
		}
		c.Write8(off, byte(v))
		c.Write8(off+1, byte(v>>8))
		c.Write8(off+2, byte(v>>16))
	}
	cpu.R[0] = bootromOK
}

// The RP2350 flash ROM services take flash offsets, not bus addresses:
//
//	flash_range_erase(offset, count, block_size, erase_cmd)
//	flash_range_program(offset, src, count)
//
// Sizes are validated against the 16 MiB array; the block size and erase
// command are advisory because every erase here produces 0xFF.
func (b *bootROM) stubFlashErase(c *Chip, cpu *m33.CPU) {
	offset, count := cpu.R[0], cpu.R[1]
	if !c.eraseFlashRange(offset, count) {
		c.Logger.Warn("bootrom flash erase out of range",
			slog.String("offset", fmtHex(offset)), slog.Uint64("bytes", uint64(count)))
		cpu.R[0] = bootromErrInvalidAddr
		return
	}
	c.Logger.Debug("flash erase", slog.String("offset", fmtHex(offset)), slog.Uint64("bytes", uint64(count)))
	cpu.R[0] = bootromOK
}

func (b *bootROM) stubFlashProgram(c *Chip, cpu *m33.CPU) {
	offset, src, count := cpu.R[0], cpu.R[1], cpu.R[2]
	if count == 0 {
		cpu.R[0] = bootromOK
		return
	}
	if count > uint32(len(c.Flash)) || offset > uint32(len(c.Flash))-count {
		c.Logger.Warn("bootrom flash program out of range",
			slog.String("offset", fmtHex(offset)), slog.Uint64("bytes", uint64(count)))
		cpu.R[0] = bootromErrInvalidAddr
		return
	}
	data := make([]byte, count)
	for i := range data {
		data[i] = c.Read8(src + uint32(i))
	}
	if !c.programFlashRange(offset, data) {
		cpu.R[0] = bootromErrInvalidAddr
		return
	}
	c.Logger.Debug("flash program", slog.String("offset", fmtHex(offset)), slog.Uint64("bytes", uint64(count)))
	cpu.R[0] = bootromOK
}

// stubFlashOp implements the bootrom flash_op(flags, addr, size, buf) service
// used by picotool-style code: flags.bit0 selects runtime vs storage
// addresses and bits 16:18 select erase, program or read.
func (b *bootROM) stubFlashOp(c *Chip, cpu *m33.CPU) {
	flags, addr, size, buf := cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3]
	op := (flags >> 16) & 0x7
	if flags&1 != 0 { // runtime address
		if addr < FlashBase || addr > FlashBase+uint32(len(c.Flash)) {
			cpu.R[0] = bootromErrInvalidAddr
			return
		}
		addr -= FlashBase
	}
	if addr > uint32(len(c.Flash)) || size > uint32(len(c.Flash))-addr {
		cpu.R[0] = bootromErrInvalidAddr
		return
	}
	switch op {
	case 0: // erase
		if !c.eraseFlashRange(addr, size) {
			cpu.R[0] = bootromErrInvalidAddr
			return
		}
	case 1: // program
		data := make([]byte, size)
		for i := range data {
			data[i] = c.Read8(buf + uint32(i))
		}
		if !c.programFlashRange(addr, data) {
			cpu.R[0] = bootromErrInvalidAddr
			return
		}
	case 2: // read
		for i := range size {
			c.Write8(buf+i, c.Flash[addr+i])
		}
	default:
		cpu.R[0] = bootromErrInvalidArg
		return
	}
	cpu.R[0] = bootromOK
}
