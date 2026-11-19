package devices

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// sdFixture wires a card to a chip with CS driven low.
func sdFixture(t *testing.T) *SDCard {
	t.Helper()
	c := rp2350.New()
	const cs = 7
	c.Pins.OE |= 1 << cs
	c.Pins.Out &^= 1 << cs
	card, err := NewSDCard(c, cs, "")
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func sdCommand(t *testing.T, card *SDCard, cmd byte, arg uint32) byte {
	t.Helper()
	frame := []byte{0x40 | cmd, byte(arg >> 24), byte(arg >> 16), byte(arg >> 8), byte(arg), 0x01}
	for _, b := range frame {
		card.Transfer(b)
	}
	for range 8 {
		if r := card.Transfer(0xFF); r != 0xFF {
			return r
		}
	}
	t.Fatalf("cmd %d: no response", cmd)
	return 0xFF
}

func sdReadData(t *testing.T, card *SDCard) []byte {
	t.Helper()
	var tok byte
	for range 64 {
		tok = card.Transfer(0xFF)
		if tok != 0xFF {
			break
		}
	}
	if tok != 0xFE {
		t.Fatalf("data token = %#x, want 0xFE", tok)
	}
	data := make([]byte, 512)
	for i := range data {
		data[i] = card.Transfer(0xFF)
	}
	card.Transfer(0xFF)
	card.Transfer(0xFF)
	return data
}

// TestSDCardInitAndRead walks the SPI init sequence FatFs uses and reads the
// boot sector.
func TestSDCardInitAndRead(t *testing.T) {
	card := sdFixture(t)
	if r := sdCommand(t, card, 0, 0); r != 0x01 {
		t.Fatalf("CMD0 = %#x, want 0x01", r)
	}
	if r := sdCommand(t, card, 8, 0x1AA); r != 0x01 {
		t.Fatalf("CMD8 = %#x, want 0x01", r)
	}
	for range 4 {
		if b := card.Transfer(0xFF); b == 0x01 {
			continue
		}
	}
	if r := sdCommand(t, card, 55, 0); r != 0x01 {
		t.Fatalf("CMD55 = %#x", r)
	}
	if r := sdCommand(t, card, 41, 0x4000_0000); r != 0 {
		t.Fatalf("ACMD41 = %#x, want 0", r)
	}
	if r := sdCommand(t, card, 58, 0); r != 0 {
		t.Fatalf("CMD58 = %#x", r)
	}
	ocr := uint32(card.Transfer(0xFF))<<24 | uint32(card.Transfer(0xFF))<<16 |
		uint32(card.Transfer(0xFF))<<8 | uint32(card.Transfer(0xFF))
	if ocr&(1<<30) == 0 {
		t.Fatalf("OCR %#x lacks the high-capacity bit", ocr)
	}
	if r := sdCommand(t, card, 16, 512); r != 0 {
		t.Fatalf("CMD16 = %#x", r)
	}
	if r := sdCommand(t, card, 17, 0); r != 0 {
		t.Fatalf("CMD17 = %#x", r)
	}
	boot := sdReadData(t, card)
	if boot[510] != 0x55 || boot[511] != 0xAA {
		t.Fatalf("boot signature = %02x%02x", boot[510], boot[511])
	}
	if got := string(boot[54:62]); got != "FAT16   " {
		t.Fatalf("filesystem type = %q", got)
	}
}

// TestSDCardFATRoot finds HELLO.TXT through the FAT and checks its contents.
func TestSDCardFATRoot(t *testing.T) {
	img := BuildFAT16Image(4 << 20)
	bps := int(binary.LittleEndian.Uint16(img[11:]))
	spc := int(img[13])
	reserved := int(binary.LittleEndian.Uint16(img[14:]))
	numFATs := int(img[16])
	rootEntries := int(binary.LittleEndian.Uint16(img[17:]))
	fatSecs := int(binary.LittleEndian.Uint16(img[22:]))
	rootOff := (reserved + numFATs*fatSecs) * bps
	dataOff := rootOff + rootEntries*32

	found := false
	for i := range rootEntries {
		e := img[rootOff+i*32:]
		if e[0] == 0 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x08 != 0 {
			continue
		}
		name := strings.TrimRight(string(e[0:8]), " ") + "." + strings.TrimRight(string(e[8:11]), " ")
		if name != "HELLO.TXT" {
			continue
		}
		found = true
		cluster := int(binary.LittleEndian.Uint16(e[26:]))
		size := int(binary.LittleEndian.Uint32(e[28:]))
		start := dataOff + (cluster-2)*spc*bps
		content := string(img[start : start+size])
		if !strings.Contains(content, "RP2350-Touch-LCD-2") {
			t.Fatalf("content = %q", content)
		}
	}
	if !found {
		t.Fatal("HELLO.TXT not found in the root directory")
	}
}

// TestSDCardWrite programs a block and reads it back.
func TestSDCardWrite(t *testing.T) {
	card := sdFixture(t)
	sdCommand(t, card, 0, 0)
	sdCommand(t, card, 55, 0)
	sdCommand(t, card, 41, 0x4000_0000)
	if r := sdCommand(t, card, 24, 40); r != 0 {
		t.Fatalf("CMD24 = %#x", r)
	}
	card.Transfer(0xFE)
	payload := make([]byte, 512)
	copy(payload, "written by rp2350sim")
	for _, b := range payload {
		card.Transfer(b)
	}
	card.Transfer(0xFF)
	card.Transfer(0xFF)
	if r := card.Transfer(0xFF); r != 0x05 {
		t.Fatalf("data response = %#x, want 0x05", r)
	}
	for range 8 { // busy, then the ready byte
		if card.Transfer(0xFF) == 0xFF {
			break
		}
	}
	if r := sdCommand(t, card, 17, 40); r != 0 {
		t.Fatalf("CMD17 = %#x", r)
	}
	got := sdReadData(t, card)
	want := "written by rp2350sim"
	if string(got[:len(want)]) != want {
		t.Fatalf("read back %q", got[:len(want)])
	}
}
