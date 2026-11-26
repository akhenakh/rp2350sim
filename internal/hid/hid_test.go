package hid

import (
	"strings"
	"testing"
)

// descriptor items used by the fixtures. Sizes are kept to one byte unless the
// value needs more, matching the way TinyGo builds its report descriptors.
func usagePage(p byte) []byte   { return []byte{0x05, p} }
func usage(u byte) []byte       { return []byte{0x09, u} }
func collection(t byte) []byte  { return []byte{0xA1, t} }
func endCollection() []byte     { return []byte{0xC0} }
func reportID(id byte) []byte   { return []byte{0x85, id} }
func logicalMin(v byte) []byte  { return []byte{0x15, v} }
func logicalMax(v byte) []byte  { return []byte{0x25, v} }
func reportSize(v byte) []byte  { return []byte{0x75, v} }
func reportCount(v byte) []byte { return []byte{0x95, v} }
func usageMin(v byte) []byte    { return []byte{0x19, v} }
func usageMax(v byte) []byte    { return []byte{0x29, v} }
func input(v byte) []byte       { return []byte{0x81, v} }
func output(v byte) []byte      { return []byte{0x91, v} }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// keyboardMouseDescriptor mirrors the keyboard and mouse collections in
// TinyGo's CDCHID descriptor: report ID 2 is the keyboard, ID 1 the mouse, and
// the keyboard declares an output report for its LEDs.
func keyboardMouseDescriptor() []byte {
	kb := cat(
		usagePage(1), usage(6), collection(1), reportID(2),
		usagePage(7), usageMin(0xE0), usageMax(0xE7),
		logicalMin(0), logicalMax(1), reportSize(1), reportCount(8), input(0x02),
		reportCount(1), reportSize(8), input(0x01),
		reportCount(3), reportSize(1), usagePage(8), usageMin(1), usageMax(3), output(0x02),
		reportCount(5), reportSize(1), output(0x01),
		reportCount(6), reportSize(8), logicalMin(0), logicalMax(0xFF),
		usagePage(7), usageMin(0), usageMax(0xFF), input(0x00),
		endCollection(),
	)
	mouse := cat(
		usagePage(1), usage(2), collection(1),
		usagePage(1), usage(1), collection(0), reportID(1),
		usagePage(9), usageMin(1), usageMax(5),
		logicalMin(0), logicalMax(1), reportSize(1), reportCount(5), input(0x02),
		reportCount(1), reportSize(3), input(0x01),
		usagePage(1), usage(0x30), usage(0x31), usage(0x38),
		logicalMin(0x81), logicalMax(0x7F), reportSize(8), reportCount(3), input(0x06),
		endCollection(), endCollection(),
	)
	return cat(kb, mouse)
}

func joystickDescriptor() []byte {
	return cat(
		usagePage(1), usage(4), collection(1), reportID(1),
		usagePage(9), usageMin(1), usageMax(16),
		logicalMin(0), logicalMax(1), reportSize(1), reportCount(16), input(0x02),
		usagePage(1), usage(0x39), logicalMin(0), logicalMax(7),
		reportCount(1), reportSize(4), input(0x02),
		reportCount(1), reportSize(4), input(0x01),
		usagePage(1), usage(0x30), usage(0x31), usage(0x32), usage(0x33), usage(0x34), usage(0x35),
		logicalMin(0x81), logicalMax(0x7F), reportSize(16), reportCount(6), input(0x02),
		endCollection(),
	)
}

func TestParseKeyboardMouse(t *testing.T) {
	d := Parse(keyboardMouseDescriptor())
	if !d.HasReportIDs {
		t.Fatal("keyboard/mouse descriptor has no report IDs")
	}
	if got := d.Inputs[2]; got.Kind != Keyboard || got.Bits != 64 {
		t.Fatalf("keyboard report = %+v", got)
	}
	if got := d.Inputs[1]; got.Kind != Mouse || got.Bits != 32 {
		t.Fatalf("mouse report = %+v", got)
	}
	if got := d.Outputs[2]; got.Bits != 8 {
		t.Fatalf("keyboard LED report bits = %d, want 8", got.Bits)
	}
	if got := d.PrimaryReportID(); got != 1 {
		t.Fatalf("primary report ID = %d, want 1", got)
	}
	if got := d.ReportIDForKind(Keyboard); got != 2 {
		t.Fatalf("keyboard report ID = %d, want 2", got)
	}
	kinds := d.Kinds()
	if len(kinds) != 2 || kinds[0] != Mouse || kinds[1] != Keyboard {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestDecodeKeyboard(t *testing.T) {
	d := Parse(keyboardMouseDescriptor())
	id, kind, text := d.Decode([]byte{2, 0x02, 0, 0x04, 0, 0, 0, 0, 0})
	if id != 2 || kind != Keyboard {
		t.Fatalf("id=%d kind=%s", id, kind)
	}
	if !strings.Contains(text, "mods=LShift") || !strings.Contains(text, "keys=A") {
		t.Fatalf("keyboard text = %q", text)
	}
}

func TestDecodeMouse(t *testing.T) {
	d := Parse(keyboardMouseDescriptor())
	id, kind, text := d.Decode([]byte{1, 0x01, 5, 0xFB, 1})
	if id != 1 || kind != Mouse {
		t.Fatalf("id=%d kind=%s", id, kind)
	}
	for _, want := range []string{"buttons=left", "dx=5", "dy=-5", "wheel=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mouse text %q missing %q", text, want)
		}
	}
}

func TestDecodeGamepad(t *testing.T) {
	d := Parse(joystickDescriptor())
	data := []byte{
		1,          // report ID
		0x05, 0x00, // buttons 1 and 3
		3,          // hat: down-right
		0x64, 0x00, // x = 100
		0x38, 0xFF, // y = -200
		0x00, 0x00, // z = 0
		0x01, 0x80, // rx = -32767
		0xFF, 0x7F, // ry = 32767
		0x00, 0x00, // rz = 0
	}
	id, kind, text := d.Decode(data)
	if id != 1 || kind != Gamepad {
		t.Fatalf("id=%d kind=%s", id, kind)
	}
	for _, want := range []string{"buttons=1,3", "hat=down-right", "x=100", "y=-200", "rx=-32767", "ry=32767"} {
		if !strings.Contains(text, want) {
			t.Fatalf("gamepad text %q missing %q", text, want)
		}
	}
}

func TestParseTruncatedDescriptor(t *testing.T) {
	for _, desc := range [][]byte{nil, {0x05}, {0xFE, 1}, {0xA1, 0x01}} {
		d := Parse(desc)
		_, kind, text := d.Decode([]byte{1, 2, 3})
		if kind != Other || !strings.Contains(text, "bytes=") {
			t.Fatalf("desc % x: kind=%s text=%q", desc, kind, text)
		}
	}
}
