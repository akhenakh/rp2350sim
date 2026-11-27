package rp2350

// RP2350 OTP (one-time-programmable) memory.
//
// OTP is 8 KB of antifuse cells arranged as 4096 rows of 24 bits: 16 data
// bits, a 6-bit modified Hamming code and 2 bit-repair-by-polarity bits.
// Programming sets bits (0 -> 1) and is irreversible; a write that would
// clear a bit is refused. There is no persistence: the array lives in RAM and
// starts blank on every run, seeded only with a chip ID.
//
// Two views exist, matching the datasheet:
//   - the ECC alias returns two corrected 16-bit rows per 32-bit read,
//   - the raw alias returns one 24-bit row per 32-bit read, ECC untouched.
// Both have a "guarded" alias which faults instead of supplying data the
// lock shim refuses; the simulator has no bus-error channel, so a guarded
// failure is logged and reads all-ones like the unguarded alias.
//
// Page permissions (64 pages of 64 rows) come from the OTP lock pages on a
// real device. They are runtime state here: a blank device's default locks
// are applied at reset and SW_LOCK0..63 may advance them until the next
// reset.

import (
	"log/slog"
	"math/bits"
)

const (
	OTPBase           = 0x40120000
	OTPDataBase       = 0x40130000 // ECC alias, first 8 KB populated
	OTPDataRawBase    = 0x40134000 // raw alias, 16 KB populated
	OTPDataGuarded    = 0x40138000 // ECC guarded alias
	OTPDataRawGuarded = 0x4013c000 // raw guarded alias

	OTPRows       = 4096
	OTPRowBytes   = 3
	OTPDataBytes  = 8 * 1024
	OTPPages      = 64
	OTPPageRows   = OTPRows / OTPPages
	otpWindowSize = 0x4000
)

// Lock levels. 0 is read/write, 1 read-only, 2 inaccessible; the order is the
// only allowed progression.
const (
	otpLockRW = iota
	otpLockRO
	otpLockNoAccess
)

// Register offsets inside the OTP control block.
const (
	otpSWLock0      = 0x000
	otpSWLockCount  = 64
	otpSBPIInstr    = 0x100
	otpSBPIWData0   = 0x104
	otpSBPIRData0   = 0x114
	otpSBPIStatus   = 0x124
	otpUSR          = 0x128
	otpDBG          = 0x12c
	otpBIST         = 0x134
	otpCRTKeyW0     = 0x138
	otpCRTKeyW3     = 0x144
	otpCritical     = 0x148
	otpKeyValid     = 0x14c
	otpDebugEn      = 0x150
	otpDebugEnLock  = 0x154
	otpArchSel      = 0x158
	otpArchSelStat  = 0x15c
	otpBootDis      = 0x160
	otpINTR         = 0x164
	otpINTE         = 0x168
	otpINTF         = 0x16c
	otpINTS         = 0x170
	otpSBPIInstrEx  = 1 << 30
	otpSBPIOutDone  = 1 << 4
	otpSBPIRDataVld = 1 << 0
	otpUSRPD        = 1 << 4
)

// otpECCParity is the RP2350 bootrom's modified-Hamming parity table, from
// the datasheet (section 13.6.2). Bit i of each entry selects the code word
// bits that contribute to parity bit 16+i.
var otpECCParity = [6]uint32{
	0b0000001010110101011011,
	0b0000000011011001101101,
	0b0000001100011110001110,
	0b0000000000011111110000,
	0b0000001111100000000000,
	0b0111111111111111111111,
}

func otpEvenParity(v uint32) uint32 { return uint32(bits.OnesCount32(v) & 1) }

// otpCalcECC returns data with the six ECC bits placed at 21:16.
func otpCalcECC(data uint16) uint32 {
	p := uint32(data)
	for i := range 6 {
		p |= otpEvenParity(p&otpECCParity[i]) << (16 + i)
	}
	return p
}

// otpEncodeRow builds the 24-bit row for a 16-bit data value.
func otpEncodeRow(data uint16) uint32 { return otpCalcECC(data) }

// otpDecodeRow corrects a row through the ECC alias. ok is false when the
// row holds an uncorrectable error; data then is the uncorrected value.
func otpDecodeRow(raw uint32) (data uint16, ok bool) {
	if (raw>>22)&3 == 3 {
		// BRP: the row was inverted at programming time.
		raw ^= 0xffffff
	}
	got := uint16(raw)
	stored := uint16(raw>>16) & 0x3f
	want := uint16(otpCalcECC(got) >> 16)
	if stored == want {
		return got, true
	}
	for i := range 16 {
		cand := got ^ 1<<i
		if uint16(otpCalcECC(cand)>>16) == stored {
			return cand, true
		}
	}
	// A single flipped ECC bit leaves the 16 data bits intact.
	if bits.OnesCount16(stored^want) == 1 {
		return got, true
	}
	return got, false
}

// OTPDecodeRow exposes ECC decoding for tools that dump the array.
func OTPDecodeRow(raw uint32) (uint16, bool) { return otpDecodeRow(raw) }

// OTPEncodeRow exposes ECC encoding for tools that seed the array.
func OTPEncodeRow(data uint16) uint32 { return otpEncodeRow(data) }

// OTPRowStatus decodes a row and reports "ok", "corrected", "ecc-error" (the
// data is intact but the stored ECC bits do not validate) or
// "uncorrectable".
func OTPRowStatus(raw uint32) (uint16, string) {
	data, ok := otpDecodeRow(raw)
	normalized := raw & 0xffffff
	if (raw>>22)&3 == 3 {
		normalized ^= 0xffffff // BRP: the row was stored inverted
	}
	switch {
	case !ok:
		return data, "uncorrectable"
	case normalized != otpEncodeRow(data):
		if data != uint16(normalized) {
			return data, "corrected"
		}
		return data, "ecc-error"
	}
	return data, "ok"
}

type otpPageLock struct {
	sec, nsec uint8
}

type otpBlock struct {
	chip *Chip
	// rows holds the 24-bit programmed value of every row.
	rows [OTPRows]uint32
	// locks is the effective page lock state; swAdvanced records pages whose
	// SW_LOCK register was written (shown by dumps, not reloaded).
	locks      [OTPPages]otpPageLock
	swAdvanced [OTPPages]bool
	writes     uint64
	lastWrite  int
	// version changes on every fuse or lock mutation, so hosts can cache a
	// copy of the rows instead of rebuilding it every frame.
	version uint64

	block *regBlock
}

func newOTP(c *Chip) *otpBlock {
	o := &otpBlock{chip: c}
	o.block = newRegBlock(OTPBase, 0x1000)
	o.block.readFn = o.readReg
	o.block.writeFn = o.writeReg
	o.block.resetFn = func(c *Chip) { o.resetLockState() }
	o.block.clearFn = func(c *Chip, off, v uint32) {
		if off == otpINTR { // write-1-to-clear
			o.block.regs[off/4] &^= v
			return
		}
		o.block.write(c, off, o.block.read(c, off)&^v)
	}
	o.block.regs[otpUSR/4] = 1 // DCTRL: the data bridge is enabled at reset
	o.resetLockState()
	// Manufacturing data: a 64-bit chip ID in rows 0-3. A real device has a
	// random ID here; a fixed one keeps runs comparable.
	id := uint64(0x8f3a91c45d27be60)
	for i := range 4 {
		o.rows[i] = otpEncodeRow(uint16(id >> (16 * i)))
	}
	return o
}

// resetLockState applies the factory page locks of a blank device and
// restores USR. Programmed rows are fuses: they survive a chip reset.
func (o *otpBlock) resetLockState() {
	o.locks = [OTPPages]otpPageLock{}
	o.swAdvanced = [OTPPages]bool{}
	o.locks[0] = otpPageLock{sec: otpLockRO, nsec: otpLockRO}
	o.locks[1] = otpPageLock{sec: otpLockRW, nsec: otpLockRO}
	o.locks[2] = otpPageLock{sec: otpLockRW, nsec: otpLockRO}
	o.locks[62] = otpPageLock{sec: otpLockRW, nsec: otpLockRO}
	o.locks[63] = otpPageLock{sec: otpLockRW, nsec: otpLockRO}
	o.block.regs[otpUSR/4] = 1 // DCTRL: the data bridge is enabled at reset
}

// effectiveLock returns the lock state applied to a read or write of the
// page. The emulated cores run Secure, so only the secure lock matters.
func (o *otpBlock) effectiveLock(page int) uint8 {
	if page < 0 || page >= OTPPages {
		return otpLockNoAccess
	}
	return o.locks[page].sec
}

func (o *otpBlock) dataBridgeEnabled() bool {
	usr := o.block.regs[otpUSR/4]
	return usr&1 != 0 && usr&otpUSRPD == 0
}

func (o *otpBlock) failDataRead(addr uint32, guarded bool) uint32 {
	what := "locked"
	switch usr := o.block.regs[otpUSR/4]; {
	case usr&otpUSRPD != 0:
		what = "OTP powered down (USR.PD=1)"
	case usr&1 == 0:
		what = "data bridge disabled (USR.DCTRL=0)"
	}
	if guarded {
		o.chip.Logger.Warn("otp guarded read failed", slog.String("addr", fmtHex(addr)), slog.String("reason", what))
	}
	return 0xffffffff
}

// readWindow serves the four data aliases. ok is false for addresses outside
// them, so the caller can fall through to the register blocks.
func (o *otpBlock) readWindow(addr uint32) (uint32, bool) {
	var off uint32
	guarded := false
	ecc := false
	switch {
	case addr >= OTPDataBase && addr < OTPDataBase+otpWindowSize:
		off, ecc = addr-OTPDataBase, true
	case addr >= OTPDataRawBase && addr < OTPDataRawBase+otpWindowSize:
		off = addr - OTPDataRawBase
	case addr >= OTPDataGuarded && addr < OTPDataGuarded+otpWindowSize:
		off, ecc, guarded = addr-OTPDataGuarded, true, true
	case addr >= OTPDataRawGuarded && addr < OTPDataRawGuarded+otpWindowSize:
		off, guarded = addr-OTPDataRawGuarded, true
	default:
		return 0, false
	}
	if !ecc && off >= OTPRows*4 {
		return 0, false
	}
	if ecc && off >= OTPDataBytes {
		return 0, false
	}
	if !o.dataBridgeEnabled() {
		return o.failDataRead(addr, guarded), true
	}
	if ecc {
		row := int(off / 2)
		lo := o.readECCRow(row, guarded, addr)
		hi := o.readECCRow(row+1, guarded, addr)
		return lo | hi<<16, true
	}
	row := int(off / 4)
	if o.effectiveLock(row/OTPPageRows) == otpLockNoAccess {
		return o.failDataRead(addr, guarded), true
	}
	return o.rows[row], true
}

func (o *otpBlock) readECCRow(row int, guarded bool, addr uint32) uint32 {
	if row < 0 || row >= OTPRows {
		return 0xffff
	}
	if o.effectiveLock(row/OTPPageRows) == otpLockNoAccess {
		return o.failDataRead(addr, guarded) & 0xffff
	}
	data, ok := otpDecodeRow(o.rows[row])
	if !ok {
		if guarded {
			o.chip.Logger.Warn("otp guarded ECC read: uncorrectable row",
				slog.Int("row", row), slog.String("raw", fmtHex(o.rows[row])))
		}
	}
	return uint32(data)
}

func (o *otpBlock) readReg(c *Chip, off uint32) (uint32, bool) {
	switch {
	case off < otpSWLock0+otpSWLockCount*4:
		page := int(off / 4)
		l := o.locks[page]
		return lockBits(l.sec) | lockBits(l.nsec)<<2, true
	case off == otpSBPIStatus:
		// INSTR_DONE stays set: SBPI instructions complete immediately.
		return o.block.regs[off/4] | otpSBPIOutDone, true
	case off >= otpSBPIRData0 && off < otpSBPIRData0+16:
		return 0, true
	case off == otpUSR:
		return o.block.regs[off/4], true
	case off == otpDBG:
		// PSM_DONE | BOOT_DONE | ROSC_UP | ROSC_UP_SEEN.
		return 0xe, true
	case off >= otpCRTKeyW0 && off <= otpCRTKeyW3:
		return 0, true
	case off == otpCritical:
		// Secure boot and debug-disable flags would come from the CRIT
		// rows; secure boot is not modelled, so a blank device reads 0.
		return 0, true
	case off == otpKeyValid:
		return 0, true
	case off == otpArchSelStat:
		return 0, true // both cores run Arm
	case off == otpINTS:
		return (o.block.regs[otpINTR/4] | o.block.regs[otpINTF/4]) & o.block.regs[otpINTE/4], true
	}
	return 0, false
}

func lockBits(level uint8) uint32 {
	switch level {
	case otpLockRO:
		return 1
	case otpLockNoAccess:
		return 3
	}
	return 0
}

// lockLevel converts a written thermometer field into a level. Any value
// above the current state advances it; states never regress.
func lockLevel(v uint32) uint8 {
	switch {
	case v&2 != 0:
		return otpLockNoAccess
	case v&1 != 0:
		return otpLockRO
	}
	return otpLockRW
}

func maxLock(a uint8, v uint32) uint8 {
	return max(a, lockLevel(v))
}

func (o *otpBlock) writeReg(c *Chip, off uint32, v uint32) {
	switch {
	case off < otpSWLock0+otpSWLockCount*4:
		page := int(off / 4)
		l := &o.locks[page]
		l.sec = maxLock(l.sec, v)
		l.nsec = maxLock(l.nsec, v>>2)
		o.swAdvanced[page] = true
		o.version++
		c.Logger.Debug("otp software lock", slog.Int("page", page),
			slog.Uint64("secure", uint64(l.sec)), slog.Uint64("nonsecure", uint64(l.nsec)))
	case off == otpSBPIInstr:
		if v&otpSBPIInstrEx != 0 {
			// The Synopsys SBPI command set is not modelled; the bootrom
			// OTP service programs rows directly. Mark the instruction as
			// complete so polling firmware does not spin.
			o.block.regs[off/4] = v &^ otpSBPIInstrEx
			o.block.regs[otpSBPIStatus/4] |= otpSBPIOutDone
			c.Logger.Debug("otp sbpi instruction ignored", slog.String("instr", fmtHex(v)))
		}
	case off == otpUSR:
		o.block.regs[off/4] = v & 0x11 // only PD and DCTRL exist
	case off == otpBootDis:
		o.block.regs[off/4] |= v & 3
	case off == otpINTR:
		// Raw interrupt flags are write-one-to-clear; nothing sets them.
		o.block.regs[off/4] &^= v
	case off == otpINTE || off == otpINTF:
		o.block.regs[off/4] = v
	case off >= otpSBPIRData0 && off < otpSBPIRData0+16:
		// Read-only.
	case off >= otpCRTKeyW0 && off <= otpCRTKeyW3:
		c.Logger.Debug("otp crt key write ignored", slog.String("value", fmtHex(v)))
	}
}

// eccWriteTarget applies bit repair by polarity before fuse validation. A
// single pre-set bit that conflicts with the ECC target inverts the target and
// sets both BRP bits, allowing the resulting row to program successfully.
func (o *otpBlock) eccWriteTarget(row int, target uint32) uint32 {
	target &= 0xffffff
	conflict := o.rows[row] &^ target & 0x3fffff
	if bits.OnesCount32(conflict) == 1 {
		return target ^ 0xffffff
	}
	return target
}

// programRow writes a 24-bit value with fuse semantics: bits may only be set.
// It returns false when the write would clear a programmed bit.
func (o *otpBlock) programRow(row int, raw uint32) bool {
	if row < 0 || row >= OTPRows {
		return false
	}
	raw &= 0xffffff
	if o.rows[row]&^raw != 0 {
		return false
	}
	if raw&^o.rows[row] != 0 {
		o.writes++
		o.lastWrite = row
		o.version++
	}
	o.rows[row] |= raw
	return true
}

// Row writes a row through the bootrom OTP service, honouring the page lock.
// It reports the bootrom error code (0 on success).
func (o *otpBlock) writeRow(row int, raw uint32) uint32 {
	if lock := o.effectiveLock(row / OTPPageRows); lock != otpLockRW {
		return bootromErrNotPermitted
	}
	if !o.programRow(row, raw) {
		return bootromErrUnsupportedMod
	}
	return bootromOK
}

func (o *otpBlock) readRow(row int) (uint32, uint32) {
	if row < 0 || row >= OTPRows {
		return 0, bootromErrInvalidArg
	}
	if o.effectiveLock(row/OTPPageRows) == otpLockNoAccess {
		return 0, bootromErrNotPermitted
	}
	return o.rows[row], bootromOK
}

// SetRow pre-programs a row from a host file, bypassing fuse and lock rules
// (it represents data burned before the device reached the firmware).
func (o *otpBlock) setRow(row int, raw uint32) {
	if row < 0 || row >= OTPRows {
		return
	}
	raw &= 0xffffff
	if o.rows[row] == raw {
		return
	}
	o.rows[row] = raw
	// Host seeds are fuses too: bump the version so UI snapshots refresh and
	// a persist target sees the change.
	o.version++
}

// OTPRow returns the raw 24-bit contents of a row.
func (c *Chip) OTPRow(row int) uint32 {
	if c.otp == nil || row < 0 || row >= OTPRows {
		return 0
	}
	return c.otp.rows[row]
}

// SetOTPRow pre-programs a row from a host file.
func (c *Chip) SetOTPRow(row int, raw uint32) {
	if c.otp != nil {
		c.otp.setRow(row, raw)
	}
}

// OTPRowsCopy returns a copy of every raw row, for dumps and the UI.
func (c *Chip) OTPRowsCopy() []uint32 {
	out := make([]uint32, OTPRows)
	if c.otp != nil {
		copy(out, c.otp.rows[:])
	}
	return out
}

// OTPVersion changes on every OTP fuse or lock mutation.
func (c *Chip) OTPVersion() uint64 {
	if c.otp == nil {
		return 0
	}
	return c.otp.version
}

// OTPWrites reports how many rows the firmware programmed this run.
func (c *Chip) OTPWrites() uint64 {
	if c.otp == nil {
		return 0
	}
	return c.otp.writes
}

// OTPLastWriteRow is the row of the most recent programming operation, or
// -1 when firmware has not written OTP this run.
func (c *Chip) OTPLastWriteRow() int {
	if c.otp == nil || c.otp.writes == 0 {
		return -1
	}
	return c.otp.lastWrite
}

// OTPProgrammedRows counts rows whose fuses are no longer blank.
func (c *Chip) OTPProgrammedRows() int {
	if c.otp == nil {
		return 0
	}
	n := 0
	for _, v := range c.otp.rows {
		if v != 0 {
			n++
		}
	}
	return n
}

// OTPPageLock returns the effective secure and non-secure lock levels of a
// page (0 read/write, 1 read-only, 2 inaccessible).
func (c *Chip) OTPPageLock(page int) (sec, nsec uint8) {
	if c.otp == nil || page < 0 || page >= OTPPages {
		return otpLockNoAccess, otpLockNoAccess
	}
	return c.otp.locks[page].sec, c.otp.locks[page].nsec
}

// OTPPageLockNames returns the lock levels as datasheet words.
func OTPPageLockNames(sec, nsec uint8) (string, string) {
	name := func(l uint8) string {
		switch l {
		case otpLockRW:
			return "read/write"
		case otpLockRO:
			return "read-only"
		}
		return "inaccessible"
	}
	return name(sec), name(nsec)
}

// OTPSoftwareLocked reports whether firmware advanced the page's lock.
func (c *Chip) OTPSoftwareLocked(page int) bool {
	return c.otp != nil && page >= 0 && page < OTPPages && c.otp.swAdvanced[page]
}
