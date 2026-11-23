package midi

import "testing"

func TestParserRunningStatus(t *testing.T) {
	p := &Parser{}
	var got []Message
	for _, b := range []byte{0x90, 60, 100, 62, 0, 0xB0, 7, 90} {
		got = append(got, p.Feed(b)...)
	}
	if len(got) != 3 {
		t.Fatalf("messages = %d: %v", len(got), got)
	}
	if got[0].Status != 0x90 || got[0].Data[0] != 60 || got[0].Data[1] != 100 {
		t.Fatalf("note on = %v", got[0])
	}
	if got[1].Status != 0x90 || got[1].Data[0] != 62 || got[1].Data[1] != 0 {
		t.Fatalf("running status = %v", got[1])
	}
	if got[2].Status != 0xB0 || got[2].Data[0] != 7 || got[2].Data[1] != 90 {
		t.Fatalf("cc = %v", got[2])
	}
}

func TestParserSystemAndRealTime(t *testing.T) {
	p := &Parser{}
	var got []Message
	// program change (1 data), real-time clock in the middle, then a note.
	for _, b := range []byte{0xC0, 5, 0xF8, 0x90, 0xF8, 64, 1} {
		got = append(got, p.Feed(b)...)
	}
	if len(got) != 4 {
		t.Fatalf("messages = %d: %v", len(got), got)
	}
	if got[0].Status != 0xC0 || got[0].Data[0] != 5 {
		t.Fatalf("program = %v", got[0])
	}
	if got[1].Status != 0xF8 || got[3].Status != 0x90 || got[3].Data[0] != 64 {
		t.Fatalf("messages = %v", got)
	}
}

func TestUSBRoundTrip(t *testing.T) {
	m := Message{Status: 0x92, Data: [2]byte{60, 100}, Len: 3}
	pkt := USBMessage(m)
	if pkt[0] != 0x9 || pkt[1] != 0x92 {
		t.Fatalf("packet = % x", pkt)
	}
	got := DecodeUSB(pkt[:])
	if len(got) != 1 || got[0] != m {
		t.Fatalf("decoded = %v", got)
	}
	cc := Message{Status: 0xB3, Data: [2]byte{7, 127}, Len: 3}
	pkt = USBMessage(cc)
	if pkt[0] != 0xB {
		t.Fatalf("cc CIN = %#x", pkt[0])
	}
	// Tune Request is a single-byte System Common message, CIN 0x5.
	tune := Message{Status: 0xF6, Len: 1}
	pkt = USBMessage(tune)
	if pkt[0] != 0x5 {
		t.Fatalf("tune request CIN = %#x, want 0x5", pkt[0])
	}
	if got := DecodeUSB(pkt[:]); len(got) != 1 || got[0] != tune {
		t.Fatalf("decoded tune request = %v", got)
	}
}

func TestNoteName(t *testing.T) {
	if NoteName(60) != "C4" || NoteName(69) != "A4" {
		t.Fatalf("notes = %s %s", NoteName(60), NoteName(69))
	}
}
