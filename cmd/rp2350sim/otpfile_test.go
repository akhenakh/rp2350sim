package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// TestOTPFileRoundTrip checks that a dump can seed a fresh chip, including a
// row whose ECC bytes were corrupted on purpose.
func TestOTPFileRoundTrip(t *testing.T) {
	src := rp2350.New()
	src.SetOTPRow(0x400, rp2350.OTPEncodeRow(0x4845))
	src.SetOTPRow(0x410, 0x123456) // raw bytes, ECC not valid
	path := filepath.Join(t.TempDir(), "otp.json")
	if err := dumpOTP(src, path); err != nil {
		t.Fatalf("dump: %v", err)
	}
	dst := rp2350.New()
	n, err := loadOTP(dst, path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n != 2+4 { // two seeded rows plus the four chip ID rows
		t.Fatalf("loaded %d rows, want 6", n)
	}
	if got := dst.OTPRow(0x400); got != rp2350.OTPEncodeRow(0x4845) {
		t.Fatalf("row 0x400 = %#06x", got)
	}
	if got := dst.OTPRow(0x410); got != 0x123456 {
		t.Fatalf("raw row 0x410 = %#06x", got)
	}
}

// TestOTPFileRejectsBadRow checks bounds and empty rows.
func TestOTPFileRejectsWideValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.json")
	for _, contents := range []string{
		`{"rows":[{"row":1,"raw":"0x1000000"}]}`,
		`{"rows":[{"row":1,"data":"0x10000"}]}`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOTP(rp2350.New(), path); err == nil {
			t.Fatalf("wide OTP value accepted: %s", contents)
		}
	}
}

func TestOTPFileRejectsBadRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"rows":[{"row":4096,"data":"0x1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOTP(rp2350.New(), path); err == nil {
		t.Fatal("out-of-range row accepted")
	}
	if err := os.WriteFile(path, []byte(`{"rows":[{"row":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOTP(rp2350.New(), path); err == nil {
		t.Fatal("row without raw or data accepted")
	}
}
