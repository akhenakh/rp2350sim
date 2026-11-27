// Package otpfile reads and writes the OTP JSON interchange shared by the CLI
// and the UI.
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
package otpfile

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// SeedRow is one pre-programmed row: the 24-bit raw value including ECC.
type SeedRow struct {
	Row int
	Raw uint32
}

type fileRow struct {
	Row  int    `json:"row"`
	Raw  string `json:"raw,omitempty"`
	Data string `json:"data,omitempty"`
	ECC  string `json:"ecc,omitempty"`
}

type fileLock struct {
	Page      int    `json:"page"`
	Secure    string `json:"secure"`
	NonSecure string `json:"nonsecure"`
}

// Contents is the JSON document written by a dump or a persisted session.
type Contents struct {
	Writes uint64     `json:"writes"`
	Rows   []fileRow  `json:"rows"`
	Locked []fileLock `json:"software_locked,omitempty"`
}

func parseHex(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	return strconv.ParseUint(s, 16, 32)
}

// NewRow validates one string-form row from a JSON seed. data is 16 bits and
// gets a fresh ECC; raw is 24 bits and is written verbatim.
func NewRow(row int, data, raw string) (SeedRow, error) {
	if row < 0 || row >= rp2350.OTPRows {
		return SeedRow{}, fmt.Errorf("row %d out of range", row)
	}
	switch {
	case raw != "":
		v, err := parseHex(raw)
		if err != nil {
			return SeedRow{}, fmt.Errorf("row %d: %w", row, err)
		}
		if v > 0xffffff {
			return SeedRow{}, fmt.Errorf("row %d: raw value exceeds 24 bits", row)
		}
		return SeedRow{Row: row, Raw: uint32(v)}, nil
	case data != "":
		v, err := parseHex(data)
		if err != nil {
			return SeedRow{}, fmt.Errorf("row %d: %w", row, err)
		}
		if v > 0xffff {
			return SeedRow{}, fmt.Errorf("row %d: data value exceeds 16 bits", row)
		}
		return SeedRow{Row: row, Raw: uint32(rp2350.OTPEncodeRow(uint16(v)))}, nil
	default:
		return SeedRow{}, fmt.Errorf("row %d has neither raw nor data", row)
	}
}

// NewRowValues is the board-YAML form, where data and raw are numeric.
func NewRowValues(row int, data, raw *uint32) (SeedRow, error) {
	if row < 0 || row >= rp2350.OTPRows {
		return SeedRow{}, fmt.Errorf("row %d out of range", row)
	}
	switch {
	case raw != nil:
		if *raw > 0xffffff {
			return SeedRow{}, fmt.Errorf("row %d: raw value exceeds 24 bits", row)
		}
		return SeedRow{Row: row, Raw: *raw}, nil
	case data != nil:
		if *data > 0xffff {
			return SeedRow{}, fmt.Errorf("row %d: data value exceeds 16 bits", row)
		}
		return SeedRow{Row: row, Raw: uint32(rp2350.OTPEncodeRow(uint16(*data)))}, nil
	default:
		return SeedRow{}, fmt.Errorf("row %d has neither raw nor data", row)
	}
}

// Parse decodes a seed document.
func Parse(b []byte, name string) ([]SeedRow, error) {
	var contents Contents
	if err := json.Unmarshal(b, &contents); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	rows := make([]SeedRow, 0, len(contents.Rows))
	for _, r := range contents.Rows {
		row, err := NewRow(r.Row, r.Data, r.Raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Load reads and validates a seed document.
func Load(path string) ([]SeedRow, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b, path)
}

// Merge combines seed sources. Later rows override earlier ones, and the
// result is sorted by row so application order is deterministic.
func Merge(sources ...[]SeedRow) []SeedRow {
	m := map[int]uint32{}
	for _, rows := range sources {
		for _, r := range rows {
			m[r.Row] = r.Raw
		}
	}
	out := make([]SeedRow, 0, len(m))
	for row, raw := range m {
		out = append(out, SeedRow{Row: row, Raw: raw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Row < out[j].Row })
	return out
}

// Apply pre-programs the chip's fuses. It returns the number of rows applied.
func Apply(c *rp2350.Chip, rows []SeedRow) int {
	n := 0
	for _, r := range rows {
		c.SetOTPRow(r.Row, r.Raw)
		n++
	}
	return n
}

// ContentsOf snapshots every programmed row plus the pages software locked
// this run.
func ContentsOf(c *rp2350.Chip) Contents {
	rows := c.OTPRowsCopy()
	out := Contents{Writes: c.OTPWrites(), Rows: []fileRow{}}
	for i, raw := range rows {
		if raw == 0 {
			continue
		}
		data, status := rp2350.OTPRowStatus(raw)
		out.Rows = append(out.Rows, fileRow{
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
		out.Locked = append(out.Locked, fileLock{Page: page, Secure: secName, NonSecure: nsecName})
	}
	return out
}

// Write marshals a dump document to path.
func Write(path string, contents Contents) error {
	b, err := json.MarshalIndent(contents, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// Save writes the chip's current rows to path.
func Save(c *rp2350.Chip, path string) error {
	return Write(path, ContentsOf(c))
}
