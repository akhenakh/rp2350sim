package debug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSourceFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSourceOutOfRangeLineIsEmpty(t *testing.T) {
	path := writeSourceFixture(t)
	lines, start, err := Source(path, 360, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 || start != 0 {
		t.Fatalf("Source out of range = %q start %d, want empty/0", lines, start)
	}
}

func TestSourceClampsFirstLineContext(t *testing.T) {
	path := writeSourceFixture(t)
	lines, start, err := Source(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if start != 1 {
		t.Fatalf("start = %d, want 1", start)
	}
	if got, want := strings.Join(lines, "|"), "one|two|three"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}
