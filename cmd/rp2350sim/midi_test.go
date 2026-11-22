package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func pendingCount(b *midiBridge) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func TestMIDIBridgeFeedQueuesMessages(t *testing.T) {
	b := newMIDIBridge(rp2350.New(), false, -1, false, quietLogger())
	b.feed([]byte{0x90, 60, 100, 62, 0})
	if got := pendingCount(b); got != 2 {
		t.Fatalf("pending = %d, want 2 (running status)", got)
	}
}

// The UART tap must only report firmware traffic: re-injecting it would make
// an echo firmware loop forever.
func TestMIDIBridgeObserveDoesNotReinject(t *testing.T) {
	b := newMIDIBridge(rp2350.New(), false, -1, false, quietLogger())
	b.feed([]byte{0x90, 60, 100})
	b.observe([]byte{0x90, 60, 100})
	b.observe([]byte{0x80, 60, 0})
	if got := pendingCount(b); got != 1 {
		t.Fatalf("pending = %d, want only the injected message", got)
	}
}
