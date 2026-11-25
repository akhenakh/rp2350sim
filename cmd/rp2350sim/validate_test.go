package main

import (
	"strings"
	"testing"
)

func TestParseHex32(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    uint32
		wantErr bool
	}{
		{in: "0x1000", want: 0x1000},
		{in: "0XDEADBEEF", want: 0xdeadbeef},
		{in: "face", want: 0xface},
		{in: "", wantErr: true},
		{in: "0x", wantErr: true},
		{in: "xyz", wantErr: true},
		{in: "0x100000000", wantErr: true},
		{in: "-1", wantErr: true},
	} {
		got, err := parseHex32(tc.in, "-test")
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parseHex32(%q) = %#x, %v; want %#x, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParsePokes(t *testing.T) {
	got, err := parsePokes("0x20000000=0x12345678, 40070000 = 41")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].addr != 0x20000000 || got[0].val != 0x12345678 || got[1].addr != 0x40070000 || got[1].val != 0x41 {
		t.Fatalf("pokes = %+v", got)
	}
	for _, bad := range []string{"0x20000000", "=1", "0x20000000=", "0x20000000=xyz", "0x100000000=1"} {
		if _, err := parsePokes(bad); err == nil {
			t.Errorf("invalid poke accepted: %q", bad)
		}
	}
}

func TestLogicCaptureRejectsBadRateAndPinCount(t *testing.T) {
	if _, err := newLogicCapture("", "", "", "", 200e6, 1024, "test"); err == nil {
		t.Fatal("logic rate above the system clock was accepted")
	}
	if _, err := newLogicCapture("0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16", "", "", "", 2e6, 1024, "test"); err == nil {
		t.Fatal("more than 16 logic pins were accepted")
	}
	if _, err := newLogicCapture("", "", "", "", 2e6, 0, "test"); err == nil {
		t.Fatal("zero logic depth was accepted")
	}
}

func TestLogicCaptureRejectsInvalidProtocolPins(t *testing.T) {
	for _, tc := range []struct {
		uart, i2c, spi string
	}{
		{uart: "-1,115200"},
		{uart: "0,48,115200"},
		{uart: "0,1,0"},
		{i2c: "4,-1"},
		{i2c: "4,48"},
		{spi: "18,19,-2"},
		{spi: "18,19,16,48"},
		{spi: "-1,19"},
	} {
		_, err := newLogicCapture("", tc.uart, tc.i2c, tc.spi, 2e6, 1024, "test")
		if err == nil {
			t.Errorf("invalid logic protocol accepted: uart=%q i2c=%q spi=%q", tc.uart, tc.i2c, tc.spi)
		}
	}
	if _, err := newLogicCapture("0,,1", "", "", "", 2e6, 1024, "test"); err == nil || !strings.Contains(err.Error(), "empty GPIO") {
		t.Fatalf("empty logic pin accepted: %v", err)
	}
	if _, err := newLogicCapture("0,1", "0,1,115200", "4,5", "18,19,16,17", 2e6, 1024, "test"); err != nil {
		t.Fatalf("valid logic protocols rejected: %v", err)
	}
}
