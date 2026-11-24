package devices

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// BusPresetInfo describes a built-in device preset for the I2C/SPI buses.
// Presets let a board or the CLI attach a common sensor, display or flash
// without running an external Bus Pirate script.
type BusPresetInfo struct {
	Name        string
	Kind        BusKind
	Addresses   []uint8 // I2C addresses the model implements; first is default
	Description string
}

// busPresets is the registry. Keep names lower-case and stable: they appear in
// boards.yaml and in the CLI flags.
var busPresets = []BusPresetInfo{
	{
		Name: "bme280", Kind: BusI2C, Addresses: []uint8{0x76, 0x77},
		Description: "Bosch BME280 temperature, pressure and humidity sensor",
	},
	{
		Name: "ssd1306", Kind: BusI2C, Addresses: []uint8{0x3C, 0x3D},
		Description: "SSD1306 128x64 monochrome OLED controller",
	},
	{
		Name: "mpu6050", Kind: BusI2C, Addresses: []uint8{0x68, 0x69},
		Description: "InvenSense MPU-6050 accelerometer and gyroscope",
	},
	{
		Name: "w25q16", Kind: BusSPI,
		Description: "Winbond W25Q16 2 MiB SPI NOR flash (JEDEC EF 40 15)",
	},
}

// BusPresetList returns the registry sorted by name.
func BusPresetList() []BusPresetInfo {
	out := append([]BusPresetInfo(nil), busPresets...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FindBusPreset looks a preset up by name, case-insensitively.
func FindBusPreset(name string) (BusPresetInfo, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range busPresets {
		if p.Name == name {
			return p, true
		}
	}
	return BusPresetInfo{}, false
}

// NewI2CPreset builds an I2C target for a preset. addr 0 selects the model's
// default address. The returned address is the one to attach.
func NewI2CPreset(name string, addr int) (rp2350.I2CDevice, uint8, error) {
	info, ok := FindBusPreset(name)
	if !ok {
		return nil, 0, fmt.Errorf("unknown I2C preset %q", name)
	}
	if info.Kind != BusI2C {
		return nil, 0, fmt.Errorf("preset %q is not an I2C device", name)
	}
	if addr == 0 {
		addr = int(info.Addresses[0])
	}
	if addr < 0x08 || addr > 0x77 {
		return nil, 0, fmt.Errorf("preset %q address %#x is not 7-bit", name, addr)
	}
	if !slices.Contains(info.Addresses, uint8(addr)) {
		return nil, 0, fmt.Errorf("preset %q does not implement address %#x", name, addr)
	}
	switch info.Name {
	case "bme280":
		return newBME280(), uint8(addr), nil
	case "ssd1306":
		return newSSD1306(), uint8(addr), nil
	case "mpu6050":
		return newMPU6050(), uint8(addr), nil
	}
	return nil, 0, fmt.Errorf("preset %q has no I2C model", name)
}

// SPIPreset is a built-in SPI target. Select is called when the chip select is
// asserted (low) so the command state machine starts at a command byte.
type SPIPreset struct {
	Name     string
	Select   func()
	Transfer func(byte) byte
}

// NewSPIPreset builds an SPI target for a preset.
func NewSPIPreset(name string) (*SPIPreset, error) {
	info, ok := FindBusPreset(name)
	if !ok {
		return nil, fmt.Errorf("unknown SPI preset %q", name)
	}
	if info.Kind != BusSPI {
		return nil, fmt.Errorf("preset %q is not an SPI device", name)
	}
	switch info.Name {
	case "w25q16":
		dev := newW25Q16()
		return &SPIPreset{Name: info.Name, Select: dev.Select, Transfer: dev.Transfer}, nil
	}
	return nil, fmt.Errorf("preset %q has no SPI model", name)
}

// regTarget implements the register-pointer protocol shared by the sensor
// chips: the first write after a start latches the register, later writes store
// data and reads return the current register, auto-incrementing.
type regTarget struct {
	regs    []byte
	ptr     uint8
	first   bool
	wrap    int
	onWrite func(reg, value byte)
}

func (r *regTarget) BeginTransaction() { r.first = true }

func (r *regTarget) EndTransaction() {}

func (r *regTarget) next() {
	r.ptr = uint8((int(r.ptr) + 1) % r.wrap)
}

func (r *regTarget) Write(b byte) { _ = r.WriteAck(b) }

func (r *regTarget) WriteAck(b byte) bool {
	if r.first {
		r.ptr = b
		r.first = false
		return true
	}
	r.regs[r.ptr] = b
	if r.onWrite != nil {
		r.onWrite(r.ptr, b)
	}
	r.next()
	return true
}

func (r *regTarget) Read() byte {
	v, _ := r.ReadAck()
	return v
}

func (r *regTarget) ReadAck() (byte, bool) {
	v := r.regs[r.ptr]
	r.next()
	return v, true
}

// --- BME280 ---------------------------------------------------------------

// The calibration constants and raw sample are the values from the BME280
// datasheet's compensation example (about 25 C, 1006 hPa and 46 % RH). They
// stay constant: the preset is a deterministic target, not a weather model.
type bme280 struct {
	regTarget
}

func newBME280() *bme280 {
	d := &bme280{}
	d.regs = make([]byte, 256)
	d.wrap = 256
	d.onWrite = func(reg, v byte) {
		if reg == 0xE0 && v == 0xB6 {
			d.load()
		}
	}
	d.load()
	return d
}

func (d *bme280) load() {
	clear(d.regs)
	// Temperature calibration, 0x88..0x8D.
	putLE16(d.regs, 0x88, 27504)
	putLE16(d.regs, 0x8A, 26435)
	putLEI16(d.regs, 0x8C, -1000)
	// Pressure calibration, 0x8E..0x9F.
	putLE16(d.regs, 0x8E, 36477)
	putLEI16(d.regs, 0x90, -10685)
	putLE16(d.regs, 0x92, 3024)
	putLE16(d.regs, 0x94, 2855)
	putLE16(d.regs, 0x96, 140)
	putLEI16(d.regs, 0x98, -7)
	putLE16(d.regs, 0x9A, 15500)
	putLEI16(d.regs, 0x9C, -14600)
	putLE16(d.regs, 0x9E, 6000)
	// Humidity calibration.
	d.regs[0xA1] = 75 // H1
	putLE16(d.regs, 0xE1, 350)
	d.regs[0xE3] = 0 // H3
	d.regs[0xE4] = 320 >> 4
	d.regs[0xE5] = byte(320&0x0F) << 4
	d.regs[0xE6] = 0
	d.regs[0xE7] = 30

	d.regs[0xD0] = 0x60 // chip id
	d.regs[0xD1] = 0x00
	d.regs[0xE0] = 0x00 // reset reads zero
	d.regs[0xF2] = 0x00
	d.regs[0xF3] = 0x00
	d.regs[0xF4] = 0x00
	d.regs[0xF5] = 0x00

	// Raw sample: 20-bit pressure and temperature, 16-bit humidity.
	press, temp, hum := uint32(415148), uint32(519888), uint16(26337)
	d.regs[0xF7] = byte(press >> 12)
	d.regs[0xF8] = byte(press >> 4)
	d.regs[0xF9] = byte(press&0x0F) << 4
	d.regs[0xFA] = byte(temp >> 12)
	d.regs[0xFB] = byte(temp >> 4)
	d.regs[0xFC] = byte(temp&0x0F) << 4
	d.regs[0xFD] = byte(hum >> 8)
	d.regs[0xFE] = byte(hum)
}

// --- MPU-6050 -------------------------------------------------------------

type mpu6050 struct {
	regTarget
}

func newMPU6050() *mpu6050 {
	d := &mpu6050{}
	d.regs = make([]byte, 128)
	d.wrap = 128
	d.onWrite = func(reg, v byte) {
		if reg == 0x6B && v&0x80 != 0 {
			d.load()
		}
	}
	d.load()
	return d
}

func (d *mpu6050) load() {
	clear(d.regs)
	d.regs[0x75] = 0x68 // WHO_AM_I
	d.regs[0x6B] = 0x40 // PWR_MGMT_1: sleep after reset
	// Accelerometer: 1 g on Z (16384 LSB/g), temperature 0, gyro 0.
	d.regs[0x3B], d.regs[0x3C] = 0x00, 0x00
	d.regs[0x3D], d.regs[0x3E] = 0x00, 0x00
	d.regs[0x3F], d.regs[0x40] = 0x40, 0x00
	d.regs[0x41], d.regs[0x42] = 0x00, 0x00
}

// --- SSD1306 --------------------------------------------------------------

// ssd1306 models the command and data streams of a 128x64 OLED controller. It
// keeps the GDDRAM so a future display view can render it, but the preset's
// job is to ACK traffic and behave like a real panel on the bus.
type ssd1306 struct {
	regs [1024]byte

	first   bool
	mode    byte // 0 = commands, 1 = GDDRAM data
	pending int  // argument bytes still expected by the last command
	arg     [2]byte
	argN    int
	cmd     byte

	col, page             int
	colStart, colEnd      int
	pageStart, pageEnd    int
	pageMode              bool
	displayOn             bool
	contrast              byte
	startLine             byte
	segmentRemap, comScan bool
	invert                bool
}

func newSSD1306() *ssd1306 {
	d := &ssd1306{}
	d.reset()
	return d
}

func (d *ssd1306) reset() {
	d.colStart, d.colEnd = 0, 127
	d.pageStart, d.pageEnd = 0, 7
	d.col, d.page = 0, 0
	d.pageMode = false
	d.contrast = 0x7F
	d.displayOn = false
	d.segmentRemap = false
	d.comScan = false
	d.invert = false
	d.first = true
	d.mode = 0
	d.pending = 0
	clear(d.regs[:])
}

func (d *ssd1306) BeginTransaction() {
	d.first = true
	d.pending = 0
}

func (d *ssd1306) EndTransaction() {}

func (d *ssd1306) Write(b byte) { _ = d.WriteAck(b) }

func (d *ssd1306) WriteAck(b byte) bool {
	if d.first {
		// Control byte: 0x00 commands, 0x40 data. The 0x80/0xC0 forms use
		// the same low bits (Co/D#), so mask them off.
		if b&0x40 != 0 {
			d.mode = 1
		} else {
			d.mode = 0
		}
		d.first = false
		return true
	}
	if d.mode == 1 {
		d.writeData(b)
	} else {
		d.command(b)
	}
	return true
}

func (d *ssd1306) writeData(b byte) {
	if d.page < 0 || d.page > 7 || d.col < 0 || d.col > 127 {
		return
	}
	d.regs[d.page*128+d.col] = b
	d.col++
	if d.col > d.colEnd {
		d.col = d.colStart
		if d.pageMode {
			d.page++
			if d.page > d.pageEnd {
				d.page = d.pageStart
			}
		} else {
			d.page++
			if d.page > d.pageEnd {
				d.page = d.pageStart
			}
		}
	}
}

func (d *ssd1306) command(b byte) {
	if d.pending > 0 {
		d.arg[d.argN] = b
		d.argN++
		if d.argN < d.pending {
			return
		}
		d.applyCommand()
		d.pending, d.argN = 0, 0
		return
	}
	switch {
	case b == 0x20: // memory addressing mode
		d.cmd, d.pending = b, 1
	case b == 0x21: // column address
		d.cmd, d.pending = b, 2
	case b == 0x22: // page address
		d.cmd, d.pending = b, 2
	case b == 0x81: // contrast
		d.cmd, d.pending = b, 1
	case b == 0xA8: // multiplex
		d.cmd, d.pending = b, 1
	case b == 0xD3: // display offset
		d.cmd, d.pending = b, 1
	case b == 0xDA: // COM pins
		d.cmd, d.pending = b, 1
	case b == 0xDB: // VCOMH
		d.cmd, d.pending = b, 1
	case b == 0x8D: // charge pump
		d.cmd, d.pending = b, 1
	case b <= 0x0F:
		d.col = (d.col & 0xF0) | int(b)
	case b >= 0x10 && b <= 0x1F:
		d.col = (d.col & 0x0F) | int(b&0x0F)<<4
	case b >= 0x40 && b <= 0x7F:
		d.startLine = b & 0x3F
	case b >= 0xB0 && b <= 0xB7:
		d.page = int(b & 0x07)
	case b == 0xA0:
		d.segmentRemap = false
	case b == 0xA1:
		d.segmentRemap = true
	case b == 0xC0:
		d.comScan = false
	case b == 0xC8:
		d.comScan = true
	case b == 0xA6:
		d.invert = false
	case b == 0xA7:
		d.invert = true
	case b == 0xAE:
		d.displayOn = false
	case b == 0xAF:
		d.displayOn = true
	}
}

func (d *ssd1306) applyCommand() {
	switch d.cmd {
	case 0x20:
		d.pageMode = d.arg[0] == 2
	case 0x21:
		d.colStart, d.colEnd = int(d.arg[0]), int(d.arg[1])
		if d.colStart > 127 {
			d.colStart = 127
		}
		if d.colEnd > 127 {
			d.colEnd = 127
		}
		if d.colEnd < d.colStart {
			d.colEnd = d.colStart
		}
		d.col = d.colStart
	case 0x22:
		d.pageStart, d.pageEnd = int(d.arg[0]), int(d.arg[1])
		if d.pageStart > 7 {
			d.pageStart = 7
		}
		if d.pageEnd > 7 {
			d.pageEnd = 7
		}
		if d.pageEnd < d.pageStart {
			d.pageEnd = d.pageStart
		}
		d.page = d.pageStart
	case 0x81:
		d.contrast = d.arg[0]
	}
}

func (d *ssd1306) Read() byte { return 0 }

func (d *ssd1306) ReadAck() (byte, bool) { return 0, true }

// DisplayOn and GRAM expose the panel state to tests and a future OLED view.
func (d *ssd1306) DisplayOn() bool { return d.displayOn }

func (d *ssd1306) GRAM() []byte { return d.regs[:] }

// --- W25Q16 ---------------------------------------------------------------

const (
	w25PhaseCmd = iota
	w25PhaseAddr
	w25PhaseRead
	w25PhaseWrite
	w25PhaseStatus
	w25PhaseID
	w25PhaseDummy
	w25PhaseIgnore
)

type w25q16 struct {
	flash []byte

	phase     int
	cmd       byte
	addr      int
	addrN     int
	status    byte
	id        []byte
	idN       int
	dummy     int
	writeMode bool
	eraseSize int
}

func newW25Q16() *w25q16 {
	f := &w25q16{flash: make([]byte, 2<<20)}
	for i := range f.flash {
		f.flash[i] = 0xFF
	}
	f.Select()
	return f
}

// Select resets the command state machine when CS is asserted. The emulator
// calls it on every low transition of the preset's chip select.
func (f *w25q16) Select() {
	f.phase = w25PhaseCmd
	f.cmd = 0
	f.addr = 0
	f.addrN = 0
	f.dummy = 0
	f.writeMode = false
	f.eraseSize = 0
	f.idN = 0
	f.id = nil
}

func (f *w25q16) Transfer(tx byte) byte {
	switch f.phase {
	case w25PhaseCmd:
		return f.command(tx)
	case w25PhaseAddr:
		f.addr = f.addr<<8 | int(tx)
		f.addrN++
		if f.addrN < 3 {
			return 0xFF
		}
		switch {
		case f.eraseSize > 0:
			f.erase(f.addr, f.eraseSize)
			f.eraseSize = 0
			f.status &^= 0x02
			f.phase = w25PhaseIgnore
		case f.id != nil:
			f.phase = w25PhaseID
		case f.writeMode:
			f.status &^= 0x02
			f.phase = w25PhaseWrite
		case f.dummy > 0:
			f.phase = w25PhaseDummy
		default:
			f.phase = w25PhaseRead
		}
		return 0xFF
	case w25PhaseDummy:
		f.phase = w25PhaseRead
		return 0xFF
	case w25PhaseRead:
		v := f.flash[f.addr%len(f.flash)]
		f.addr++
		return v
	case w25PhaseWrite:
		if f.addr >= 0 && f.addr < len(f.flash) {
			f.flash[f.addr] = tx
		}
		f.addr++
		return 0xFF
	case w25PhaseStatus:
		return f.status
	case w25PhaseID:
		v := byte(0xFF)
		if f.idN < len(f.id) {
			v = f.id[f.idN]
		}
		f.idN++
		return v
	}
	return 0xFF
}

func (f *w25q16) command(tx byte) byte {
	f.cmd = tx
	switch tx {
	case 0x9F: // JEDEC ID
		f.id = []byte{0xEF, 0x40, 0x15}
		f.phase = w25PhaseID
		f.idN = 1
		return f.id[0]
	case 0x90: // manufacturer + device ID, preceded by three address bytes
		f.id = []byte{0xEF, 0x15}
		f.phase = w25PhaseAddr
		f.addrN = 0
	case 0x05, 0x35, 0x15: // status registers
		f.phase = w25PhaseStatus
		return f.status
	case 0x03, 0x0B, 0x6B, 0xBB, 0xEB: // reads
		f.phase = w25PhaseAddr
		f.addrN = 0
		if tx == 0x0B || tx == 0x6B || tx == 0xEB {
			f.dummy = 1
		}
	case 0x02, 0x32: // page/quad programs
		f.phase = w25PhaseAddr
		f.addrN = 0
		f.writeMode = true
	case 0x06: // write enable
		f.status |= 0x02
		f.phase = w25PhaseIgnore
	case 0x04: // write disable
		f.status &^= 0x02
		f.phase = w25PhaseIgnore
	case 0x20:
		f.phase = w25PhaseAddr
		f.addrN = 0
		f.eraseSize = 4 << 10
	case 0x52:
		f.phase = w25PhaseAddr
		f.addrN = 0
		f.eraseSize = 32 << 10
	case 0xD8:
		f.phase = w25PhaseAddr
		f.addrN = 0
		f.eraseSize = 64 << 10
	case 0xC7, 0x60: // chip erase
		f.eraseAll()
		f.phase = w25PhaseIgnore
	default:
		f.phase = w25PhaseIgnore
	}
	return 0xFF
}

func (f *w25q16) erase(addr, size int) {
	if addr < 0 || addr >= len(f.flash) {
		return
	}
	end := min(addr+size, len(f.flash))
	for i := addr; i < end; i++ {
		f.flash[i] = 0xFF
	}
}

func (f *w25q16) eraseAll() {
	for i := range f.flash {
		f.flash[i] = 0xFF
	}
}

// putLE16 stores a little-endian 16-bit value in a register array.
func putLE16(regs []byte, off int, v uint16) {
	regs[off] = byte(v)
	regs[off+1] = byte(v >> 8)
}

// putLEI16 stores a signed calibration constant in two's complement.
func putLEI16(regs []byte, off int, v int16) {
	putLE16(regs, off, uint16(v))
}
