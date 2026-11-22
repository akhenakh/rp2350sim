package devices

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// LogicSection is one decoded protocol block in a saved logic trace.
type LogicSection struct {
	Name   string
	Detail string
	Text   string
}

// LogicTrace is a portable snapshot of the logic analyzer: raw pin samples and
// decoded protocol text. Both the UI and the headless runner write this format.
type LogicTrace struct {
	BoardID   string
	BoardName string
	Captured  time.Time
	Interval  uint64
	Pins      []int
	Samples   []TraceSample
	Sections  []LogicSection
}

// SampleHz is the trace sample rate derived from the cycle interval.
func (t LogicTrace) SampleHz() float64 {
	if t.Interval == 0 {
		return 0
	}
	return 150e6 / float64(t.Interval)
}

// WriteLogicTrace writes the text format shown by the UI's Save trace button.
func WriteLogicTrace(w io.Writer, t LogicTrace) error {
	var b strings.Builder
	when := t.Captured
	if when.IsZero() {
		when = time.Now()
	}
	fmt.Fprintln(&b, "# rp2350sim logic analyzer trace")
	fmt.Fprintf(&b, "board: %s\n", t.BoardID)
	if t.BoardName != "" {
		fmt.Fprintf(&b, "board_name: %s\n", t.BoardName)
	}
	fmt.Fprintf(&b, "captured: %s\n", when.Format(time.RFC3339))
	fmt.Fprintf(&b, "sample_interval_cycles: %d\n", t.Interval)
	if hz := t.SampleHz(); hz > 0 {
		fmt.Fprintf(&b, "sample_rate_hz: %.0f\n", hz)
	}
	fmt.Fprintf(&b, "samples: %d\n", len(t.Samples))
	fmt.Fprintf(&b, "channels:")
	for _, p := range t.Pins {
		fmt.Fprintf(&b, " GP%d", p)
	}
	fmt.Fprintln(&b)

	if len(t.Samples) > 0 {
		fmt.Fprintln(&b, "\n# samples")
		fmt.Fprint(&b, "# time_us")
		for _, p := range t.Pins {
			fmt.Fprintf(&b, " GP%d", p)
		}
		fmt.Fprintln(&b)
		base := t.Samples[0].T
		for _, s := range t.Samples {
			fmt.Fprintf(&b, "%.3f", float64(s.T-base)/150.0)
			for i := range t.Pins {
				if s.Bit(i) {
					fmt.Fprint(&b, " 1")
				} else {
					fmt.Fprint(&b, " 0")
				}
			}
			fmt.Fprintln(&b)
		}
	}

	for _, sec := range t.Sections {
		fmt.Fprintf(&b, "\n# %s\n", sec.Name)
		if sec.Detail != "" {
			fmt.Fprintf(&b, "# %s\n", sec.Detail)
		}
		text := strings.TrimRight(sec.Text, "\n")
		if text == "" {
			text = "(none)"
		}
		fmt.Fprintln(&b, text)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// LogicTraceFilename builds logic-analyze-trace-<date>-<board>.txt.
func LogicTraceFilename(board string, when time.Time) string {
	return fmt.Sprintf("logic-analyze-trace-%s-%s.txt", when.Format("20060102-150405"), traceBoardSlug(board))
}

// VCDFilename is the default name for a value change dump.
func VCDFilename(board string, when time.Time) string {
	return fmt.Sprintf("logic-trace-%s-%s.vcd", when.Format("20060102-150405"), traceBoardSlug(board))
}

func traceBoardSlug(board string) string {
	if board == "" {
		board = "board"
	}
	var b strings.Builder
	for _, r := range board {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "board"
	}
	return name
}
