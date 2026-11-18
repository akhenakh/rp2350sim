// Package logging builds the emulator's slog loggers.
//
// The headless runner is a CLI: its diagnostics go to stderr and CI consumes
// them, so a non-interactive run defaults to one JSON object per line. A
// terminal keeps the readable text handler. The desktop UI never shares this
// policy: it routes device logs to its own panels.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Output formats accepted by ParseFormat.
const (
	FormatAuto = "auto"
	FormatText = "text"
	FormatJSON = "json"
)

// ParseFormat validates a -log-format value.
func ParseFormat(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", FormatAuto:
		return FormatAuto, nil
	case FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown log format %q (want auto, text or json)", s)
	}
}

// ParseLevel maps a -log-level value to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
	}
}

// New returns a logger writing to w. FormatAuto selects JSON when w is not a
// terminal or when the process looks like CI, and text otherwise.
func New(w io.Writer, format string, level slog.Level) *slog.Logger {
	resolved, err := ParseFormat(format)
	if err != nil || resolved == FormatAuto {
		resolved = FormatText
		if !isTerminal(w) || os.Getenv("CI") != "" {
			resolved = FormatJSON
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	if resolved == FormatJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Discard drops every record. It is the zero value for optional loggers.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// OrDiscard returns l, or a discarding logger when l is nil.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return Discard()
	}
	return l
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
