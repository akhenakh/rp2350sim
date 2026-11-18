package main

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// lineHandler renders slog records as one compact line, which is what the
// Ethernet, Wi-Fi and console panels show. The panels are read by a human, so
// the attributes are appended as key=value pairs instead of JSON.
type lineHandler struct {
	sink  func(string)
	attrs []slog.Attr
}

func (h lineHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h lineHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	writeAttrs := func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		return true
	}
	for _, a := range h.attrs {
		writeAttrs(a)
	}
	r.Attrs(writeAttrs)
	h.sink(b.String())
	return nil
}

func (h lineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(slices.Clip(h.attrs), attrs...)
	return h
}

func (h lineHandler) WithGroup(string) slog.Handler { return h }

// panelLog collects device log lines behind its own mutex. The emulator mutex
// is already held while the chip steps, and device logs arrive from both the
// stepping goroutine and the network goroutines.
type panelLog struct {
	mu    sync.Mutex
	lines []string
}

func (p *panelLog) add(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, line)
	if len(p.lines) > 64 {
		p.lines = p.lines[len(p.lines)-32:]
	}
}

func (p *panelLog) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

func (p *panelLog) reset() {
	p.mu.Lock()
	p.lines = p.lines[:0]
	p.mu.Unlock()
}

func (p *panelLog) logger() *slog.Logger {
	return slog.New(lineHandler{sink: p.add})
}
