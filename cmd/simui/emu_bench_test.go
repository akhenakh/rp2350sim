package main

import (
	"os"
	"path/filepath"
	"testing"
)

func benchEmu(b *testing.B) *Emu {
	b.Helper()
	fw := filepath.Join("..", "..", "build", "lcd.elf")
	if _, err := os.Stat(fw); err != nil {
		b.Skip("build/lcd.elf is not built")
	}
	defs, err := LoadBoards()
	if err != nil {
		b.Fatal(err)
	}
	var board *BoardDef
	for i := range defs {
		if defs[i].ID == "geek" {
			board = &defs[i]
		}
	}
	if board == nil {
		b.Fatal("geek board missing")
	}
	e := NewEmu()
	e.SetBoard(board)
	if err := e.Load(fw); err != nil {
		b.Fatal(err)
	}
	e.RunFor(150_000_000)
	b.ResetTimer()
	return e
}

func BenchmarkSnapshot(b *testing.B) {
	e := benchEmu(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = e.Snapshot()
	}
}

func BenchmarkSnapshotAfterRun(b *testing.B) {
	e := benchEmu(b)
	b.ReportAllocs()
	for b.Loop() {
		e.RunFor(150_000)
		_ = e.Snapshot()
	}
}
