package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := map[string]string{
		"":      FormatAuto,
		"auto":  FormatAuto,
		"TEXT":  FormatText,
		" json": FormatJSON,
	}
	for in, want := range cases {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Fatalf("ParseFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("ParseFormat accepted xml")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"WARN":    slog.LevelWarn,
		"error":   slog.LevelError,
		"warning": slog.LevelWarn,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Fatalf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("trace"); err == nil {
		t.Fatal("ParseLevel accepted trace")
	}
}

func TestNewJSONShape(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, FormatJSON, slog.LevelInfo).Info("loaded", "segments", 3)
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if rec["msg"] != "loaded" || rec["segments"] != float64(3) || rec["level"] != "INFO" {
		t.Fatalf("unexpected record: %v", rec)
	}
}

func TestNewTextHandlerFiltersLevels(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, FormatText, slog.LevelInfo)
	log.Debug("hidden")
	log.Info("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatalf("debug record leaked: %q", out)
	}
	if !strings.Contains(out, "msg=shown") {
		t.Fatalf("missing info record: %q", out)
	}
}

func TestOrDiscard(t *testing.T) {
	if OrDiscard(nil) == nil {
		t.Fatal("OrDiscard(nil) returned nil")
	}
	l := New(&bytes.Buffer{}, FormatText, slog.LevelInfo)
	if OrDiscard(l) != l {
		t.Fatal("OrDiscard replaced a logger")
	}
}
