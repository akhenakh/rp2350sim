// Package hid parses USB HID report descriptors and decodes the reports used
// by keyboards, mice and gamepads. The emulated USB host uses it to classify
// the interfaces a firmware enumerates and to present reports to the CLI/UI.
package hid

import (
	"fmt"
	"strings"
)

// Kind is the top-level HID application a report belongs to.
type Kind string

const (
	Keyboard Kind = "keyboard"
	Mouse    Kind = "mouse"
	Gamepad  Kind = "gamepad"
	Media    Kind = "media"
	Other    Kind = "hid"
)

// Field is one HID input or output field. Variable fields describe one usage;
// array fields (bit1 of the main item clear) carry an index in UsageMin..Max.
type Field struct {
	UsagePage  uint16
	Usage      uint16
	UsageMin   uint16
	UsageMax   uint16
	BitOffset  int
	BitSize    int
	Count      int
	LogicalMin int32
	LogicalMax int32
	IsConstant bool
	IsVariable bool
	IsRelative bool
}

// Report is one report ID's layout.
type Report struct {
	ID     byte
	Kind   Kind
	Bits   int
	Fields []Field
}

// Device is a parsed HID report descriptor.
type Device struct {
	// HasReportIDs is true when the descriptor assigns report IDs; the first
	// byte of every transfer is then the report ID.
	HasReportIDs bool
	Inputs       map[byte]Report
	Outputs      map[byte]Report
}

// Parse decodes a HID report descriptor. Unknown or malformed items are
// skipped; a device with no recognisable collection still yields reports with
// Kind Other so raw reports remain visible.
func Parse(desc []byte) Device {
	d := Device{Inputs: map[byte]Report{}, Outputs: map[byte]Report{}}
	var (
		usagePage   uint16
		reportSize  int
		reportCount int
		logicalMin  int32
		logicalMax  int32
		reportID    byte
		kind        Kind
		depth       int
		usages      []uint32
		usageMin    uint16
		usageMax    uint16
		stack       []globalState
	)
	for off := 0; off < len(desc); {
		prefix := desc[off]
		off++
		if prefix == 0xFE { // long item
			if off >= len(desc) {
				break
			}
			n := int(desc[off])
			off += 2 + n
			continue
		}
		size := int(prefix & 3)
		if size == 3 {
			size = 4
		}
		if off+size > len(desc) {
			break
		}
		data := uint32(0)
		for i := range size {
			data |= uint32(desc[off+i]) << (8 * uint(i))
		}
		off += size
		typ := (prefix >> 2) & 3
		tag := (prefix >> 4) & 0xF

		switch typ {
		case 1: // global
			switch tag {
			case 0: // usage page
				usagePage = uint16(data)
			case 1: // logical minimum (signed)
				logicalMin = signedItem(data, size)
			case 2: // logical maximum (signed)
				logicalMax = signedItem(data, size)
			case 7: // report size
				reportSize = int(data)
			case 8: // report ID
				reportID = byte(data)
				if reportID != 0 {
					d.HasReportIDs = true
				}
			case 9: // report count
				reportCount = int(data)
			case 10: // push
				stack = append(stack, globalState{usagePage, reportSize, reportCount, logicalMin, logicalMax, reportID})
			case 11: // pop
				if n := len(stack); n > 0 {
					g := stack[n-1]
					stack = stack[:n-1]
					usagePage, reportSize, reportCount, logicalMin, logicalMax, reportID = g.page, g.size, g.count, g.min, g.max, g.id
				}
			}
		case 2: // local
			switch tag {
			case 0: // usage
				usages = append(usages, usageValue(usagePage, data, size))
			case 1: // usage minimum
				usageMin = uint16(data)
			case 2: // usage maximum
				usageMax = uint16(data)
			}
		case 0: // main
			switch tag {
			case 0xA: // collection
				if depth == 0 {
					kind = collectionKind(usagePage, usages)
				}
				depth++
			case 0xC: // end collection
				if depth > 0 {
					depth--
					if depth == 0 {
						kind = Other
					}
				}
			case 0x8, 0x9, 0xB: // input, output, feature
				main := data
				bitLen := reportSize * reportCount
				reports := d.Inputs
				if tag == 0x9 {
					reports = d.Outputs
				}
				if tag == 0xB {
					// Features are control data; keep the bit count for
					// completeness but do not decode them.
					break
				}
				r := reports[reportID]
				r.ID = reportID
				r.Kind = kind
				addMain(&r, main, bitLen, reportSize, reportCount,
					usagePage, logicalMin, logicalMax, usages, usageMin, usageMax)
				reports[reportID] = r
			}
			usages = usages[:0]
			usageMin, usageMax = 0, 0
		}
	}
	return d
}

type globalState struct {
	page        uint16
	size, count int
	min, max    int32
	id          byte
}

func signedItem(v uint32, size int) int32 {
	switch size {
	case 1:
		return int32(int8(v))
	case 2:
		return int32(int16(v))
	case 4:
		return int32(v)
	}
	return 0
}

func usageValue(page uint16, data uint32, size int) uint32 {
	if size == 4 {
		return data // extended usage: page in the high half
	}
	return uint32(page)<<16 | (data & 0xFFFF)
}

func usageParts(v uint32) (uint16, uint16) { return uint16(v >> 16), uint16(v) }

func collectionKind(page uint16, usages []uint32) Kind {
	if len(usages) == 0 {
		return Other
	}
	p, u := usageParts(usages[0])
	if p == 0 {
		p = page
	}
	switch {
	case p == 1 && u == 2:
		return Mouse
	case p == 1 && u == 6:
		return Keyboard
	case p == 1 && (u == 4 || u == 5 || u == 8):
		return Gamepad
	case p == 0x0C:
		return Media
	}
	return Other
}

// addMain appends the fields of one input/output main item and advances the
// report bit count.
func addMain(r *Report, main uint32, bitLen, reportSize, reportCount int,
	page uint16, logicalMin, logicalMax int32, usages []uint32, usageMin, usageMax uint16) {
	bits := r.Bits
	constant := main&1 != 0
	variable := main&2 != 0
	relative := main&4 != 0
	if reportCount <= 0 || reportSize <= 0 {
		return
	}
	if variable {
		for i := range reportCount {
			f := Field{
				UsagePage: page, BitOffset: bits, BitSize: reportSize, Count: 1,
				LogicalMin: logicalMin, LogicalMax: logicalMax,
				IsConstant: constant, IsVariable: true, IsRelative: relative,
			}
			if i < len(usages) {
				f.UsagePage, f.Usage = usageParts(usages[i])
			} else if usageMin != 0 || usageMax != 0 {
				f.Usage = usageMin + uint16(i)
			}
			r.Fields = append(r.Fields, f)
			bits += reportSize
		}
		r.Bits = bits
		return
	}
	// Array item: one field carrying one usage index per element.
	r.Fields = append(r.Fields, Field{
		UsagePage: page, UsageMin: usageMin, UsageMax: usageMax,
		BitOffset: bits, BitSize: reportSize, Count: reportCount,
		LogicalMin: logicalMin, LogicalMax: logicalMax,
		IsConstant: constant,
	})
	r.Bits = bits + bitLen
}

// PrimaryReportID returns the report ID most likely used by the device's main
// function, or 0 when the descriptor has no report IDs.
func (d Device) PrimaryReportID() byte {
	for _, id := range d.ReportIDs() {
		return id
	}
	return 0
}

// ReportIDs returns the input report IDs in ascending order.
func (d Device) ReportIDs() []byte {
	var ids []byte
	for id := range d.Inputs {
		ids = append(ids, id)
	}
	sortBytes(ids)
	return ids
}

// OutputReportID returns the first output report ID, or 0 when none exist.
func (d Device) OutputReportID() byte {
	var ids []byte
	for id := range d.Outputs {
		ids = append(ids, id)
	}
	sortBytes(ids)
	if len(ids) > 0 {
		return ids[0]
	}
	return 0
}

// ReportIDForKind returns the first input report ID belonging to kind.
func (d Device) ReportIDForKind(kind Kind) byte {
	for _, id := range d.ReportIDs() {
		if d.Inputs[id].Kind == kind {
			return id
		}
	}
	return 0
}

// Kinds lists the distinct application kinds in ascending report ID order.
func (d Device) Kinds() []Kind {
	seen := map[Kind]bool{}
	var out []Kind
	for _, id := range d.ReportIDs() {
		k := d.Inputs[id].Kind
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func sortBytes(b []byte) {
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j] < b[j-1]; j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
}

// Decode renders one input report. It returns the report ID, kind and a short
// human-readable description. data includes the report ID when the descriptor
// uses one.
func (d Device) Decode(data []byte) (id byte, kind Kind, text string) {
	payload := data
	if d.HasReportIDs {
		if len(data) == 0 {
			return 0, Other, "empty report"
		}
		id = data[0]
		payload = data[1:]
	}
	r, ok := d.Inputs[id]
	if !ok {
		return id, Other, fmt.Sprintf("id=%d bytes=% x", id, payload)
	}
	kind = r.Kind
	switch kind {
	case Keyboard:
		text = decodeKeyboard(r, payload)
	case Mouse:
		text = decodeMouse(r, payload)
	case Gamepad:
		text = decodeGamepad(r, payload)
	case Media:
		text = decodeMedia(r, payload)
	default:
		text = fmt.Sprintf("bytes=% x", payload)
	}
	if text == "" {
		text = fmt.Sprintf("bytes=% x", payload)
	}
	return id, kind, text
}

func readBits(data []byte, bitOff, bitSize int) uint32 {
	if bitSize <= 0 || bitOff < 0 {
		return 0
	}
	var v uint32
	for i := range bitSize {
		bit := bitOff + i
		byteIdx := bit / 8
		if byteIdx >= len(data) {
			break
		}
		if data[byteIdx]&(1<<uint(bit&7)) != 0 {
			v |= 1 << uint(i)
		}
	}
	return v
}

func signedBits(v uint32, bits int) int32 {
	if bits <= 0 || bits > 32 {
		return 0
	}
	shift := 32 - uint(bits)
	return int32(v<<shift) >> shift
}

func fieldValue(data []byte, f Field, index int) (uint32, int, bool) {
	if f.IsConstant {
		return 0, 0, false
	}
	if f.IsVariable {
		off := f.BitOffset
		return readBits(data, off, f.BitSize), f.BitSize, true
	}
	off := f.BitOffset + index*f.BitSize
	return readBits(data, off, f.BitSize), f.BitSize, true
}

var modifierNames = [8]string{"LCtrl", "LShift", "LAlt", "LGUI", "RCtrl", "RShift", "RAlt", "RGUI"}

func decodeKeyboard(r Report, data []byte) string {
	mods := uint32(0)
	keys := []uint16{}
	for _, f := range r.Fields {
		if f.IsConstant {
			continue
		}
		if f.UsagePage == 0x07 && f.IsVariable && f.Usage >= 0xE0 && f.Usage <= 0xE7 {
			v, _, ok := fieldValue(data, f, 0)
			if ok && v != 0 {
				mods |= 1 << uint(f.Usage-0xE0)
			}
			continue
		}
		if f.UsagePage == 0x07 && !f.IsVariable {
			n := f.Count
			for i := range n {
				v, _, ok := fieldValue(data, f, i)
				if !ok || v == 0 {
					continue
				}
				keys = append(keys, uint16(v))
			}
		}
	}
	if len(r.Fields) == 0 && len(data) >= 8 {
		mods = uint32(data[0])
		for _, k := range data[2:8] {
			if k != 0 {
				keys = append(keys, uint16(k))
			}
		}
	}
	var parts []string
	if mods != 0 {
		var m []string
		for i, name := range modifierNames {
			if mods&(1<<uint(i)) != 0 {
				m = append(m, name)
			}
		}
		parts = append(parts, "mods="+strings.Join(m, "+"))
	}
	if len(keys) == 0 {
		parts = append(parts, "keys=-")
	} else {
		var names []string
		for _, k := range keys {
			names = append(names, keyName(k))
		}
		parts = append(parts, "keys="+strings.Join(names, "+"))
	}
	return strings.Join(parts, " ")
}

func decodeMouse(r Report, data []byte) string {
	var buttons []string
	dx, dy, wheel := int32(0), int32(0), int32(0)
	for _, f := range r.Fields {
		if f.IsConstant {
			continue
		}
		if f.UsagePage == 0x09 && f.IsVariable {
			v, _, ok := fieldValue(data, f, 0)
			if ok && v != 0 {
				buttons = append(buttons, mouseButton(f.Usage))
			}
			continue
		}
		if f.UsagePage == 0x01 && f.IsVariable {
			v, bits, ok := fieldValue(data, f, 0)
			if !ok {
				continue
			}
			val := signedBits(v, bits)
			switch f.Usage {
			case 0x30:
				dx = val
			case 0x31:
				dy = val
			case 0x38:
				wheel = val
			}
		}
	}
	if len(buttons) == 0 {
		buttons = append(buttons, "-")
	}
	return fmt.Sprintf("buttons=%s dx=%d dy=%d wheel=%d", strings.Join(buttons, "+"), dx, dy, wheel)
}

func mouseButton(usage uint16) string {
	switch usage {
	case 1:
		return "left"
	case 2:
		return "right"
	case 3:
		return "middle"
	case 4:
		return "back"
	case 5:
		return "forward"
	}
	return fmt.Sprintf("btn%d", usage)
}

func decodeGamepad(r Report, data []byte) string {
	var buttons []uint16
	hat := int32(-1)
	axes := map[uint16]int32{}
	for _, f := range r.Fields {
		if f.IsConstant || !f.IsVariable {
			continue
		}
		switch f.UsagePage {
		case 0x09:
			v, _, ok := fieldValue(data, f, 0)
			if ok && v != 0 {
				buttons = append(buttons, f.Usage)
			}
		case 0x01:
			v, bits, ok := fieldValue(data, f, 0)
			if !ok {
				continue
			}
			if f.Usage == 0x39 {
				hat = int32(v)
			} else {
				axes[f.Usage] = signedBits(v, bits)
			}
		}
	}
	var parts []string
	if len(buttons) > 0 {
		var b []string
		for _, n := range buttons {
			b = append(b, fmt.Sprintf("%d", n))
		}
		parts = append(parts, "buttons="+strings.Join(b, ","))
	}
	if hat >= 0 {
		parts = append(parts, "hat="+hatName(hat))
	}
	for _, a := range []struct {
		usage uint16
		name  string
	}{{0x30, "x"}, {0x31, "y"}, {0x32, "z"}, {0x33, "rx"}, {0x34, "ry"}, {0x35, "rz"}} {
		if v, ok := axes[a.usage]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", a.name, v))
		}
	}
	return strings.Join(parts, " ")
}

func hatName(v int32) string {
	switch v {
	case 0:
		return "up"
	case 1:
		return "up-right"
	case 2:
		return "right"
	case 3:
		return "down-right"
	case 4:
		return "down"
	case 5:
		return "down-left"
	case 6:
		return "left"
	case 7:
		return "up-left"
	}
	return "center"
}

func decodeMedia(r Report, data []byte) string {
	for _, f := range r.Fields {
		if f.IsConstant || !f.IsVariable {
			continue
		}
		v, _, ok := fieldValue(data, f, 0)
		if ok {
			return fmt.Sprintf("usage=0x%04x", v)
		}
	}
	return fmt.Sprintf("bytes=% x", data)
}

// keyName maps HID keyboard usage IDs to short names.
func keyName(k uint16) string {
	if k >= 0x04 && k <= 0x1D {
		return string(rune('A' + k - 0x04))
	}
	names := map[uint16]string{
		0x28: "Enter", 0x29: "Esc", 0x2A: "Backspace", 0x2B: "Tab", 0x2C: "Space",
		0x2D: "-", 0x2E: "=", 0x2F: "[", 0x30: "]", 0x31: "\\", 0x32: "#", 0x33: ";",
		0x34: "'", 0x35: "`", 0x36: ",", 0x37: ".", 0x38: "/", 0x39: "CapsLock",
		0x46: "PrintScreen", 0x47: "ScrollLock", 0x48: "Pause", 0x49: "Insert",
		0x4A: "Home", 0x4B: "PageUp", 0x4C: "Delete", 0x4D: "End", 0x4E: "PageDown",
		0x4F: "Right", 0x50: "Left", 0x51: "Down", 0x52: "Up", 0x53: "NumLock",
		0x54: "KP/", 0x55: "KP*", 0x56: "KP-", 0x57: "KP+", 0x58: "KPEnter",
		0x62: "KP0", 0x63: "KP.", 0x64: "\\", 0x65: "App", 0x66: "Power", 0x67: "KP=",
	}
	if n, ok := names[k]; ok {
		return n
	}
	if k >= 0x1E && k <= 0x27 {
		if k == 0x27 {
			return "0"
		}
		return string(rune('1' + k - 0x1E))
	}
	if k >= 0x3A && k <= 0x45 {
		return fmt.Sprintf("F%d", k-0x3A+1)
	}
	if k >= 0x59 && k <= 0x61 {
		return fmt.Sprintf("KP%d", k-0x59+1)
	}
	if k >= 0xE0 && k <= 0xE7 {
		return modifierNames[k-0xE0]
	}
	return fmt.Sprintf("0x%02x", k)
}
