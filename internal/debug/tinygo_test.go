package debug_test

import (
	"os"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/debug"
	"github.com/akhenakh/rp2350sim/internal/firmware"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestTinyGoDebugVariables(t *testing.T) {
	const path = "../../build/debugvars.elf"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not built", path)
	}
	info, err := debug.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := info.Resolve("marker") // suffix match for main.marker
	if !ok {
		t.Fatal("main.marker not found from short name")
	}
	img, err := firmware.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := rp2350.New()
	for _, seg := range img.Segments {
		c.LoadSegment(seg.Addr, seg.Data)
	}
	c.Debug = rp2350.NewDebugger()
	c.Debug.AddBreakpoint(addr)
	c.Boot()
	c.Run(100_000_000)
	if !c.Debug.Stopped || c.Debug.StopPC != addr {
		t.Fatalf("stopped=%v at %#x reason=%q, want %#x",
			c.Debug.Stopped, c.Debug.StopPC, c.Debug.StopReason, addr)
	}

	fn, ok := info.FunctionAt(addr)
	if !ok || fn.Name != "main.marker" {
		t.Fatalf("FunctionAt = %+v", fn)
	}
	if file, line, ok := info.LineAt(addr); !ok || line != 15 {
		t.Fatalf("LineAt = %q:%d, %v; want main.go:15", file, line, ok)
	}

	read := func(addr uint32, size int) ([]byte, bool) { return c.PeekBytes(addr, size) }
	vars := info.VariablesAt(addr, [16]uint32(c.CPU0.R), read)
	for name, want := range map[string]string{
		"arg":   "0x12345678",
		"local": "0x1d3b5977",
	} {
		v, ok := findVariable(vars, name)
		if !ok {
			t.Errorf("%s missing from TinyGo variables", name)
			continue
		}
		if !v.Available || v.Value != want {
			t.Errorf("%s = %q available=%v note=%q, want %q", name, v.Value, v.Available, v.Note, want)
		}
	}
}
