// Package regmap resolves RP2350 peripheral, register and field names from the
// official CMSIS SVD bundled with the emulator. It is used by the hang detector
// to turn a polled MMIO address into a readable register/bit explanation.
package regmap

import (
	_ "embed"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

//go:embed RP2350.svd
var svdData []byte

type svdDevice struct {
	Peripherals []svdPeripheral `xml:"peripherals>peripheral"`
}

type svdPeripheral struct {
	Name      string        `xml:"name"`
	Base      string        `xml:"baseAddress"`
	Registers []svdRegister `xml:"registers>register"`
}

type svdRegister struct {
	Name    string     `xml:"name"`
	Offset  string     `xml:"addressOffset"`
	Dim     int        `xml:"dim"`
	DimIncr string     `xml:"dimIncrement"`
	Fields  []svdField `xml:"fields>field"`
}

type svdField struct {
	Name      string `xml:"name"`
	BitRange  string `xml:"bitRange"`
	BitOffset string `xml:"bitOffset"`
	BitWidth  string `xml:"bitWidth"`
	Access    string `xml:"access"`
}

// Field is one register bit field.
type Field struct {
	Name  string
	LSB   int
	Width int
}

// Register is one peripheral register.
type Register struct {
	Name   string
	Offset uint32
	Fields []Field
}

// Peripheral is one SVD peripheral.
type Peripheral struct {
	Name      string
	Base      uint32
	Registers []Register
}

// Map is the parsed SVD.
type Map struct {
	peripherals []Peripheral
}

var (
	once sync.Once
	m    *Map
	err  error
)

// Load parses the bundled SVD once.
func Load() (*Map, error) {
	once.Do(func() {
		var dev svdDevice
		if e := xml.Unmarshal(svdData, &dev); e != nil {
			err = e
			return
		}
		m = &Map{}
		for _, p := range dev.Peripherals {
			base, e := parseUint(p.Base)
			if e != nil {
				continue
			}
			per := Peripheral{Name: strings.TrimSpace(p.Name), Base: base}
			for _, r := range p.Registers {
				off, e := parseUint(r.Offset)
				if e != nil {
					continue
				}
				reg := Register{Name: strings.TrimSpace(r.Name), Offset: off}
				for _, f := range r.Fields {
					lsb, width := fieldBits(f)
					if width <= 0 {
						continue
					}
					reg.Fields = append(reg.Fields, Field{Name: strings.TrimSpace(f.Name), LSB: lsb, Width: width})
				}
				per.Registers = append(per.Registers, reg)
				if r.Dim > 1 {
					incr, _ := parseUint(r.DimIncr)
					for i := 1; i < r.Dim && incr > 0; i++ {
						rr := reg
						rr.Offset = off + uint32(i)*incr
						rr.Name = fmt.Sprintf("%s%d", reg.Name, i)
						per.Registers = append(per.Registers, rr)
					}
				}
			}
			m.peripherals = append(m.peripherals, per)
		}
		for i := 1; i < len(m.peripherals); i++ {
			for j := i; j > 0 && m.peripherals[j].Base < m.peripherals[j-1].Base; j-- {
				m.peripherals[j], m.peripherals[j-1] = m.peripherals[j-1], m.peripherals[j]
			}
		}
	})
	return m, err
}

func parseUint(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "UL")
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	v, e := strconv.ParseUint(s, 0, 32)
	return uint32(v), e
}

func fieldBits(f svdField) (lsb, width int) {
	if f.BitRange != "" {
		s := strings.TrimSpace(f.BitRange)
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		hi, lo, ok := strings.Cut(s, ":")
		if ok {
			h, e1 := strconv.Atoi(strings.TrimSpace(hi))
			l, e2 := strconv.Atoi(strings.TrimSpace(lo))
			if e1 == nil && e2 == nil && h >= l {
				return l, h - l + 1
			}
		}
	}
	l, e1 := strconv.Atoi(strings.TrimSpace(f.BitOffset))
	w, e2 := strconv.Atoi(strings.TrimSpace(f.BitWidth))
	if e1 == nil && e2 == nil && w > 0 {
		return l, w
	}
	return 0, 0
}

// PeripheralFor finds the peripheral containing addr.
func (m *Map) PeripheralFor(addr uint32) (*Peripheral, bool) {
	if m == nil {
		return nil, false
	}
	for i := len(m.peripherals) - 1; i >= 0; i-- {
		p := &m.peripherals[i]
		if addr >= p.Base && addr < p.Base+0x10000 {
			return p, true
		}
	}
	return nil, false
}

// RegisterFor finds the register containing addr in a peripheral.
func (p *Peripheral) RegisterFor(addr uint32) (*Register, bool) {
	off := addr - p.Base
	// Strip the atomic aliases (+0x1000/+0x2000/+0x3000).
	off &= ^uint32(0x3000)
	for i := range p.Registers {
		r := &p.Registers[i]
		if off >= r.Offset && off < r.Offset+4 {
			return r, true
		}
	}
	return nil, false
}

// FieldFor finds the field of a register containing bit.
func (r *Register) FieldFor(bit int) (*Field, bool) {
	for i := range r.Fields {
		f := &r.Fields[i]
		if bit >= f.LSB && bit < f.LSB+f.Width {
			return f, true
		}
	}
	return nil, false
}

// Bits extracts a field value.
func (f *Field) Bits(word uint32) uint32 {
	if f == nil || f.Width <= 0 || f.LSB >= 32 {
		return 0
	}
	w := f.Width
	if f.LSB+w > 32 {
		w = 32 - f.LSB
	}
	return (word >> uint(f.LSB)) & (1<<uint(w) - 1)
}

// Describe explains a polled MMIO address and its current value. peek is a
// side-effect-free register reader used to inspect the same peripheral's
// power/reset/enable fields. It never reads through a hook that could clear a
// status bit.
func Describe(addr, value uint32, peek func(uint32) (uint32, bool)) string {
	m, err := Load()
	if err != nil {
		return fmt.Sprintf("polling %#08x (SVD unavailable: %v)", addr, err)
	}
	p, ok := m.PeripheralFor(addr)
	if !ok {
		return fmt.Sprintf("polling %#08x = %#08x (unknown peripheral)", addr, value)
	}
	r, ok := p.RegisterFor(addr)
	if !ok {
		return fmt.Sprintf("polling %s+%#x = %#08x (unknown register)", p.Name, addr-p.Base, value)
	}
	// The SVD bit that corresponds to the polled address: word registers are
	// polled as a whole, so use the least significant set field, or the
	// address's bit lane when the firmware waits for one bit to become 1.
	field, bit := polledField(r, addr, value)
	out := fmt.Sprintf("%s_%s", p.Name, r.Name)
	if field != nil {
		out += fmt.Sprintf(".%s (%#08x bit%d=%d)", field.Name, addr&^uint32(3), field.LSB, field.Bits(value)&1)
	} else {
		out += fmt.Sprintf(" (%#08x = %#08x)", addr&^uint32(3), value)
	}
	_ = bit
	// Explain why a status field cannot change: inspect blocking power/reset/
	// enable fields in the same peripheral.
	if field != nil && isStatus(field.Name) && field.Bits(value) == 0 {
		if why := blockingReason(p, peek); why != "" {
			out += "; " + why + ", so " + field.Name + " cannot set"
		}
	}
	return out
}

func polledField(r *Register, addr, value uint32) (*Field, int) {
	// A read of a word register can be waiting on any bit. Prefer a status
	// field that is currently zero (the common LOCK/READY/DONE/BUSY poll),
	// then the bit lane addressed by a byte/halfword read.
	for i := range r.Fields {
		f := &r.Fields[i]
		if isStatus(f.Name) && f.Bits(value) == 0 {
			return f, f.LSB
		}
	}
	for i := range r.Fields {
		f := &r.Fields[i]
		if f.Bits(value) != 0 {
			return f, f.LSB
		}
	}
	bit := int(addr&3) * 8
	return nil, bit
}

func isStatus(name string) bool {
	n := strings.ToUpper(name)
	for _, s := range []string{"LOCK", "READY", "DONE", "BUSY", "ACK", "VALID", "STABLE", "BOOT", "START", "RDY", "LOCK_N"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

func blockingReason(p *Peripheral, peek func(uint32) (uint32, bool)) string {
	if peek == nil {
		return ""
	}
	best := ""
	bestPri := 99
	for i := range p.Registers {
		r := &p.Registers[i]
		word, ok := peek(p.Base + r.Offset)
		if !ok {
			continue
		}
		for j := range r.Fields {
			f := &r.Fields[j]
			if !isBlocking(f.Name) {
				continue
			}
			v := f.Bits(word)
			blocked := v != 0
			if !blockingWhenSet(f.Name) {
				blocked = v == 0
			}
			if !blocked {
				continue
			}
			pri := blockPriority(f.Name)
			if pri >= bestPri {
				continue
			}
			state := "set"
			if !blockingWhenSet(f.Name) {
				state = "clear"
			}
			best = fmt.Sprintf("%s_%s.%s is %s at %#08x bit%d",
				p.Name, r.Name, f.Name, state, p.Base+r.Offset, f.LSB)
			bestPri = pri
		}
	}
	return best
}

func blockPriority(name string) int {
	switch strings.ToUpper(name) {
	case "PD":
		return 0
	case "RESET", "SWRST", "ENABLE", "EN":
		return 1
	case "VCOPD", "POSTDIVPD", "DSMPD":
		return 2
	}
	return 3
}

func isBlocking(name string) bool {
	n := strings.ToUpper(name)
	switch n {
	case "PD", "VCOPD", "POSTDIVPD", "DSMPD", "PWR", "POWER", "RESET", "SWRST", "ENABLE", "EN", "ISO", "DISO":
		return true
	}
	return false
}

func blockingWhenSet(name string) bool {
	switch strings.ToUpper(name) {
	case "ENABLE", "EN":
		return false
	}
	return true
}

// Lookup returns the peripheral, register and field names for an address.
func Lookup(addr uint32) (peripheral, register, field string, bit int, ok bool) {
	m, err := Load()
	if err != nil {
		return "", "", "", 0, false
	}
	p, ok := m.PeripheralFor(addr)
	if !ok {
		return "", "", "", 0, false
	}
	r, ok := p.RegisterFor(addr)
	if !ok {
		return p.Name, "", "", 0, false
	}
	f, b := polledField(r, addr, 0)
	if f == nil {
		return p.Name, r.Name, "", b, true
	}
	return p.Name, r.Name, f.Name, f.LSB, true
}
