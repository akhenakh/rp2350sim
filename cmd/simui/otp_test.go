package main

import (
	"path/filepath"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/otpfile"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestOTPSeedSurvivesReset(t *testing.T) {
	seed := otpfile.SeedRow{Row: 0x400, Raw: uint32(rp2350.OTPEncodeRow(0xfa1a))}
	b := &BoardDef{ID: "otp", OTP: &OTPDevice{
		Seed:   []otpfile.SeedRow{seed},
		Source: "device/otp.json",
	}}
	e := newTestEmu()
	e.SetBoard(b)
	if got := e.chip.OTPRow(0x400); got != seed.Raw {
		t.Fatalf("seeded row = %#x, want %#x", got, seed.Raw)
	}
	if e.otpSeeded != 1 || e.otpSeedSource != "device/otp.json" {
		t.Fatalf("seed metadata = %d %q", e.otpSeeded, e.otpSeedSource)
	}

	// A runtime burn must survive Reset just like a fuse.
	e.chip.SetOTPRow(0x410, 0x123456)
	e.Reset()
	if got := e.chip.OTPRow(0x400); got != seed.Raw {
		t.Fatalf("seed row after reset = %#x", got)
	}
	if got := e.chip.OTPRow(0x410); got != 0x123456 {
		t.Fatalf("burned row after reset = %#x", got)
	}
	if e.otpSeeded != 1 {
		t.Fatalf("seed count after reset = %d", e.otpSeeded)
	}

	// Load replaces the chip too; the rows must survive it.
	if err := e.Load(writeFlashELF(t)); err != nil {
		t.Fatal(err)
	}
	if got := e.chip.OTPRow(0x410); got != 0x123456 {
		t.Fatalf("burned row after load = %#x", got)
	}

	// A board switch starts from the new board's seed, not the old rows.
	other := otpfile.SeedRow{Row: 0x500, Raw: 0x654321}
	e.SetBoard(&BoardDef{ID: "other", OTP: &OTPDevice{
		Seed:   []otpfile.SeedRow{other},
		Source: "other.json",
	}})
	if got := e.chip.OTPRow(0x500); got != other.Raw {
		t.Fatalf("new board seed = %#x", got)
	}
	if got := e.chip.OTPRow(0x410); got != 0 {
		t.Fatalf("old board row survived a switch: %#x", got)
	}
}

func TestOTPOverrideReplacesSeed(t *testing.T) {
	b := &BoardDef{ID: "otp", OTP: &OTPDevice{
		Seed:    []otpfile.SeedRow{{Row: 0x400, Raw: 0x111111}},
		Source:  "board.json",
		Persist: filepath.Join(t.TempDir(), "persist.json"),
	}}
	e := newTestEmu()
	e.SetOTPOverride([]otpfile.SeedRow{{Row: 0x400, Raw: 0x222222}}, "other.json")
	e.SetBoard(b)
	if got := e.chip.OTPRow(0x400); got != 0x222222 {
		t.Fatalf("override row = %#x", got)
	}
	if e.otpSeedSource != "other.json" {
		t.Fatalf("source = %q", e.otpSeedSource)
	}
	if e.otpPersist != "" {
		t.Fatal("--otp override kept the board persist target")
	}
}

func TestOTPPersistWritesRows(t *testing.T) {
	persist := filepath.Join(t.TempDir(), "persist.json")
	seed := otpfile.SeedRow{Row: 0x400, Raw: uint32(rp2350.OTPEncodeRow(0xfa1a))}
	b := &BoardDef{ID: "otp", OTP: &OTPDevice{
		Seed:    []otpfile.SeedRow{seed},
		Source:  "device/otp.json",
		Persist: persist,
	}}
	e := newTestEmu()
	e.SetBoard(b)
	if e.otpPersist != persist {
		t.Fatalf("persist path = %q", e.otpPersist)
	}
	e.chip.SetOTPRow(0x410, 0x123456)
	e.SaveOTP()

	rows, err := otpfile.Load(persist)
	if err != nil {
		t.Fatalf("persisted file: %v", err)
	}
	dst := rp2350.New()
	otpfile.Apply(dst, rows)
	if got := dst.OTPRow(0x400); got != seed.Raw {
		t.Fatalf("persisted seed row = %#x", got)
	}
	if got := dst.OTPRow(0x410); got != 0x123456 {
		t.Fatalf("persisted burned row = %#x", got)
	}
}
