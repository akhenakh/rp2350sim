package debug_test

import (
	"os"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/debug"
	"github.com/akhenakh/rp2350sim/internal/firmware"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// loadC runs a C debug fixture until debug_marker's breakpoint.
func loadC(t *testing.T, path string) (*debug.Info, *rp2350.Chip) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not built", path)
	}
	info, err := debug.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := info.Resolve("debug_marker")
	if !ok {
		t.Fatal("debug_marker symbol not found")
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
	c.Run(200_000_000)
	if !c.Debug.Stopped || c.Debug.StopReason != "breakpoint" {
		t.Fatalf("did not stop at debug_marker: stopped=%v reason=%q pc=%#x",
			c.Debug.Stopped, c.Debug.StopReason, c.CPU0.R[15])
	}
	if c.Debug.StopPC != addr {
		t.Fatalf("stopped at %#x, want %#x", c.Debug.StopPC, addr)
	}
	return info, c
}

func findVariable(vars []debug.Variable, name string) (debug.Variable, bool) {
	for _, v := range vars {
		if v.Name == name {
			return v, true
		}
	}
	return debug.Variable{}, false
}

func TestCDebugVariables(t *testing.T) {
	info, c := loadC(t, "../../firmware/c/build/debug.elf")

	fn, ok := info.FunctionAt(c.Debug.StopPC)
	if !ok || fn.Name != "debug_marker" {
		t.Fatalf("FunctionAt(%#x) = %+v, want debug_marker", c.Debug.StopPC, fn)
	}
	file, line, ok := info.LineAt(c.Debug.StopPC)
	if !ok || file == "" || line == 0 {
		t.Fatalf("LineAt(%#x) = %q:%d, %v", c.Debug.StopPC, file, line, ok)
	}

	read := func(addr uint32, size int) ([]byte, bool) { return c.PeekBytes(addr, size) }
	vars := info.VariablesAt(c.Debug.StopPC, [16]uint32(c.CPU0.R), read)

	for name, want := range map[string]string{
		"g_magic":    "0x12345678",
		"g_wide_lo":  "0x55667788",
		"g_wide_hi":  "0x11223344",
		"g_negative": "0xfffffb2e",
	} {
		v, ok := findVariable(vars, name)
		if !ok {
			t.Errorf("%s missing from variables", name)
			continue
		}
		if !v.Available || v.Value != want {
			t.Errorf("%s = %q available=%v note=%q, want %q", name, v.Value, v.Available, v.Note, want)
		}
	}

	// Step past the prologue; the volatile local then has a known value.
	for range 20 {
		c.StepDebug(0)
		vars = info.VariablesAt(c.Debug.StopPC, [16]uint32(c.CPU0.R), read)
		if v, ok := findVariable(vars, "local"); ok && v.Available && v.Value == "0xaaaaaaaa" {
			return
		}
	}
	t.Fatalf("local never became 0xaaaaaaaa; last variables: %+v", vars)
}
