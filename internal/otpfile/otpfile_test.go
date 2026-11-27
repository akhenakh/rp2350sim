package otpfile

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
	if err := Save(src, path); err != nil {
		t.Fatalf("dump: %v", err)
	}
	rows, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 2+4 { // two seeded rows plus the four chip ID rows
		t.Fatalf("loaded %d rows, want 6", len(rows))
	}
	dst := rp2350.New()
	Apply(dst, rows)
	if got := dst.OTPRow(0x400); got != rp2350.OTPEncodeRow(0x4845) {
		t.Fatalf("row 0x400 = %#06x", got)
	}
	if got := dst.OTPRow(0x410); got != 0x123456 {
		t.Fatalf("raw row 0x410 = %#06x", got)
	}
}

// TestOTPFileRejectsWideValues checks the 16-bit data and 24-bit raw limits.
func TestOTPFileRejectsWideValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.json")
	for _, contents := range []string{
		`{"rows":[{"row":1,"raw":"0x1000000"}]}`,
		`{"rows":[{"row":1,"data":"0x10000"}]}`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("wide OTP value accepted: %s", contents)
		}
	}
}

func TestOTPFileRejectsBadRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"rows":[{"row":4096,"data":"0x1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("out-of-range row accepted")
	}
	if err := os.WriteFile(path, []byte(`{"rows":[{"row":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("row without raw or data accepted")
	}
}

// TestMergeOverridesAndSorts checks that inline rows win over the file and
// that application order is stable.
func TestMergeOverridesAndSorts(t *testing.T) {
	file := []SeedRow{{Row: 3, Raw: 0x11}, {Row: 1, Raw: 0x22}}
	inline := []SeedRow{{Row: 3, Raw: 0x33}}
	rows := Merge(file, inline)
	if len(rows) != 2 || rows[0].Row != 1 || rows[1].Row != 3 || rows[1].Raw != 0x33 {
		t.Fatalf("merged rows = %+v", rows)
	}
}

// TestNewRowValues checks the board-YAML numeric form and its bounds.
func TestNewRowValues(t *testing.T) {
	data := uint32(0xfa1a)
	row, err := NewRowValues(0x400, &data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if row.Row != 0x400 || row.Raw != uint32(rp2350.OTPEncodeRow(0xfa1a)) {
		t.Fatalf("data row = %+v", row)
	}
	raw := uint32(0x1000000)
	if _, err := NewRowValues(1, nil, &raw); err == nil {
		t.Fatal("24-bit raw overflow accepted")
	}
	if _, err := NewRowValues(1, nil, nil); err == nil {
		t.Fatal("empty row accepted")
	}
}
