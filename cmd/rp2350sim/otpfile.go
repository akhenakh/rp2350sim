package main

// OTP array interchange for the headless runner.
//
// The dump is both a report and a seed file: rows carry the raw 24-bit fuse
// contents (ECC included) so a deliberately corrupted row round-trips, while
// data-only rows are accepted for hand-written seeds and get a fresh ECC.
//
//	{
//	  "writes": 3,
//	  "rows": [
//	    {"row": 1024, "raw": "0x134845", "data": "0x4845", "ecc": "ok"}
//	  ],
//	  "software_locked": [{"page": 16, "secure": "inaccessible", "nonsecure": "inaccessible"}]
//	}
//
// Only rows are loaded; locks and counters are runtime state.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

type otpFileRow struct {
	Row  int    `json:"row"`
	Raw  string `json:"raw,omitempty"`
	Data string `json:"data,omitempty"`
	ECC  string `json:"ecc,omitempty"`
}

type otpFileLock struct {
	Page      int    `json:"page"`
	Secure    string `json:"secure"`
	NonSecure string `json:"nonsecure"`
}

type otpFileContents struct {
	Writes uint64        `json:"writes"`
	Rows   []otpFileRow  `json:"rows"`
	Locked []otpFileLock `json:"software_locked,omitempty"`
}

func parseHexValue(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	return strconv.ParseUint(s, 16, 32)
}

// loadOTP seeds the chip's fuses from a dump file.
func loadOTP(c *rp2350.Chip, path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var contents otpFileContents
	if err := json.Unmarshal(b, &contents); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	n := 0
	for _, r := range contents.Rows {
		if r.Row < 0 || r.Row >= rp2350.OTPRows {
			return n, fmt.Errorf("row %d out of range", r.Row)
		}
		var raw uint64
		switch {
		case r.Raw != "":
			raw, err = parseHexValue(r.Raw)
		case r.Data != "":
			var data uint64
			data, err = parseHexValue(r.Data)
			if err == nil && data > 0xffff {
				err = fmt.Errorf("data value exceeds 16 bits")
			}
			if err == nil {
				raw = uint64(rp2350.OTPEncodeRow(uint16(data)))
			}
		default:
			err = fmt.Errorf("row %d has neither raw nor data", r.Row)
		}
		if err != nil {
			return n, fmt.Errorf("row %d: %w", r.Row, err)
		}
		if r.Raw != "" && raw > 0xffffff {
			return n, fmt.Errorf("row %d: raw value exceeds 24 bits", r.Row)
		}
		c.SetOTPRow(r.Row, uint32(raw))
		n++
	}
	return n, nil
}

// dumpOTP writes every programmed row, plus the pages software locked during
// the run.
func dumpOTP(c *rp2350.Chip, path string) error {
	rows := c.OTPRowsCopy()
	out := otpFileContents{Writes: c.OTPWrites(), Rows: []otpFileRow{}}
	for i, raw := range rows {
		if raw == 0 {
			continue
		}
		data, status := rp2350.OTPRowStatus(raw)
		out.Rows = append(out.Rows, otpFileRow{
			Row:  i,
			Raw:  fmt.Sprintf("%#06x", raw),
			Data: fmt.Sprintf("%#04x", data),
			ECC:  status,
		})
	}
	for page := range rp2350.OTPPages {
		if !c.OTPSoftwareLocked(page) {
			continue
		}
		sec, nsec := c.OTPPageLock(page)
		secName, nsecName := rp2350.OTPPageLockNames(sec, nsec)
		out.Locked = append(out.Locked, otpFileLock{Page: page, Secure: secName, NonSecure: nsecName})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
