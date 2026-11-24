//go:build qemu

package m33

// Differential test harness: runs single instructions in QEMU's Cortex-M33
// (mps2-an505) through arm-none-eabi-gdb and in this interpreter, comparing
// registers and flags.
//
// Requires qemu-system-arm and arm-none-eabi-gdb.
//
// Run with: go test -tags qemu ./internal/m33 -run TestQEMU

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type oracleResult struct {
	Regs []uint32 `json:"regs"`
	XPSR uint32   `json:"xpsr"`
	Halt string   `json:"halt,omitempty"`
}

func runOracle(t *testing.T, cases []diffCase) []oracleResult {
	t.Helper()
	qemu, err := exec.LookPath("qemu-system-arm")
	if err != nil {
		t.Skip("qemu-system-arm not installed")
	}
	gdbPath, err := exec.LookPath("arm-none-eabi-gdb")
	if err != nil {
		t.Skip("arm-none-eabi-gdb not installed")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cmd := exec.Command(qemu, "-M", "mps2-an505", "-S", "-display", "none",
		"-nographic", "-gdb", fmt.Sprintf("tcp:127.0.0.1:%d", port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	var script strings.Builder
	fmt.Fprintf(&script, "target remote :%d\nset write on\n", port)
	// Clear the exclusive monitor before each case.
	fmt.Fprintf(&script, "set {unsigned char}0x%08x = 0x%02x\n", qemuCodeBase+0x80, 0xbf)
	fmt.Fprintf(&script, "set {unsigned char}0x%08x = 0x%02x\n", qemuCodeBase+0x81, 0xf3)
	fmt.Fprintf(&script, "set {unsigned char}0x%08x = 0x%02x\n", qemuCodeBase+0x82, 0x2f)
	fmt.Fprintf(&script, "set {unsigned char}0x%08x = 0x%02x\n", qemuCodeBase+0x83, 0x8f)
	for i, c := range cases {
		// Reset the scratch and stack regions so memory state matches the
		// fresh test bus used locally.
		for addr := uint32(0x20000400); addr < 0x20000440; addr += 4 {
			fmt.Fprintf(&script, "set {unsigned int}0x%08x = 0\n", addr)
		}
		for addr := uint32(0x20006FC0); addr < 0x20007040; addr += 4 {
			fmt.Fprintf(&script, "set {unsigned int}0x%08x = 0\n", addr)
		}
		fmt.Fprintf(&script, "set $pc = 0x%08x\n", qemuCodeBase|0x81)
		script.WriteString("stepi\n")
		for off, b := range c.bytes() {
			fmt.Fprintf(&script, "set {unsigned char}0x%08x = 0x%02x\n", qemuCodeBase+uint32(off), b)
		}
		for addr, v := range c.mem {
			fmt.Fprintf(&script, "set {unsigned int}0x%08x = 0x%08x\n", addr, v)
		}
		for r := range 13 {
			fmt.Fprintf(&script, "set $r%d = 0x%08x\n", r, c.regs[r])
		}
		fmt.Fprintf(&script, "set $sp = 0x%08x\n", qemuStackTop)
		fmt.Fprintf(&script, "set $lr = 0x%08x\n", qemuCodeBase+0x101)
		fmt.Fprintf(&script, "set $xpsr = 0x%08x\n", c.cpsr|0x01000000)
		fmt.Fprintf(&script, "set $pc = 0x%08x\n", qemuCodeBase|1)
		steps := max(c.steps, 1)
		for range steps {
			script.WriteString("stepi\n")
		}
		fmt.Fprintf(&script, "printf \"CASE %d", i)
		for range 13 {
			script.WriteString(" %08x")
		}
		script.WriteString(" %08x %08x %08x %08x\\n\"")
		args := []string{}
		for r := range 13 {
			args = append(args, fmt.Sprintf("$r%d", r))
		}
		args = append(args, "$sp", "$lr", "$pc", "$xpsr")
		for _, a := range args {
			script.WriteString(", " + a)
		}
		script.WriteString("\n")
	}
	script.WriteString("kill\nquit\n")

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "oracle.gdb")
	if err := os.WriteFile(scriptPath, []byte(script.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	g := exec.Command(gdbPath, "-batch", "-x", scriptPath)
	var out, stderr strings.Builder
	g.Stdout = &out
	g.Stderr = &stderr
	gdbErr := g.Run()

	results := make([]oracleResult, len(cases))
	seen := make([]bool, len(cases))
	halt := ""
	for line := range strings.SplitSeq(out.String(), "\n") {
		if strings.Contains(line, "Program received signal") {
			halt = strings.TrimSpace(line)
			continue
		}
		if !strings.HasPrefix(line, "CASE ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 19 {
			t.Fatalf("malformed oracle result: %q", line)
		}
		var idx int
		if _, err := fmt.Sscanf(f[1], "%d", &idx); err != nil || idx < 0 || idx >= len(cases) {
			t.Fatalf("invalid oracle case index: %q", line)
		}
		if seen[idx] {
			t.Fatalf("duplicate oracle result for case %d", idx)
		}
		var vals [17]uint32
		for k := range 17 {
			if _, err := fmt.Sscanf(f[2+k], "%x", &vals[k]); err != nil {
				t.Fatalf("invalid oracle register in %q: %v", line, err)
			}
		}
		res := oracleResult{XPSR: vals[16], Halt: halt}
		res.Regs = vals[:16]
		results[idx] = res
		seen[idx] = true
		halt = ""
	}
	oracleStopped := gdbErr != nil && strings.Contains(out.String()+stderr.String(), "Remote connection closed")
	if gdbErr != nil && !oracleStopped {
		t.Fatalf("gdb failed: %v\nstdout:\n%s\nstderr:\n%s", gdbErr, out.String(), stderr.String())
	}
	for i := range results {
		if seen[i] {
			continue
		}
		if oracleStopped && cases[i].expectHalt {
			results[i].Halt = "oracle halt"
			continue
		}
		if gdbErr != nil {
			t.Fatalf("gdb failed with a missing non-halt case %d: %v\nstdout:\n%s\nstderr:\n%s",
				i, gdbErr, out.String(), stderr.String())
		}
		results[i].Halt = "no result"
	}
	return results
}

func TestQEMUInstructions(t *testing.T) {
	cases := instructionCorpus()
	results := runOracle(t, cases)
	if len(results) != len(cases) {
		t.Fatalf("oracle returned %d results for %d cases", len(results), len(cases))
	}

	failures := 0
	for i, c := range cases {
		_, diff := compareOracleCase(c, results[i])
		if len(diff) != 0 {
			failures++
			if failures <= 30 {
				t.Errorf("%s [%04x %04x]: %s", c.name, c.hw[0], hwOrZero(c.hw), strings.Join(diff, ", "))
			}
		}
	}
	if failures > 0 {
		t.Fatalf("%d/%d cases diverged", failures, len(cases))
	}
}

func hwOrZero(hw []uint16) uint16 {
	if len(hw) > 1 {
		return hw[1]
	}
	return 0
}

// runLocal executes a differential case in this interpreter.
func runLocal(c diffCase) (*CPU, *testBus) {
	bus := newTestBusAt(qemuCodeBase, 0x100000)
	bus.load(c)
	cpu := New(bus)
	cpu.R[15] = qemuCodeBase
	cpu.R[13] = qemuStackTop
	cpu.R[14] = qemuCodeBase + 0x101
	cpu.APSR = c.cpsr
	for i := range 13 {
		cpu.R[i] = c.regs[i]
	}
	steps := max(c.steps, 1)
	for range steps {
		cpu.Step()
	}
	return cpu, bus
}

func compareOracleCase(c diffCase, want oracleResult) (*CPU, []string) {
	cpu, _ := runLocal(c)
	if want.Halt == "no result" {
		return cpu, []string{"oracle produced no result"}
	}
	if c.expectHalt {
		if want.Halt == "" {
			return cpu, []string{"oracle did not halt as expected"}
		}
		if !cpu.Halted {
			return cpu, []string{"local core did not halt as expected"}
		}
		return cpu, nil
	}
	if want.Halt != "" {
		return cpu, []string{"oracle halted: " + want.Halt}
	}
	if cpu.Halted {
		return cpu, []string{"local halted: " + cpu.HaltReason}
	}
	var diff []string
	for r := range 13 {
		if cpu.R[r] != want.Regs[r] {
			diff = append(diff, fmt.Sprintf("r%d got %#08x want %#08x", r, cpu.R[r], want.Regs[r]))
		}
	}
	if cpu.R[13] != want.Regs[13] {
		diff = append(diff, fmt.Sprintf("sp got %#08x want %#08x", cpu.R[13], want.Regs[13]))
	}
	if cpu.R[14] != want.Regs[14] {
		diff = append(diff, fmt.Sprintf("lr got %#08x want %#08x", cpu.R[14], want.Regs[14]))
	}
	if cpu.R[15] != want.Regs[15] {
		diff = append(diff, fmt.Sprintf("pc got %#08x want %#08x", cpu.R[15], want.Regs[15]))
	}
	gotFlags := cpu.APSR & 0xF8000000
	wantFlags := want.XPSR & 0xF8000000
	if gotFlags != wantFlags {
		diff = append(diff, fmt.Sprintf("apsr got %#08x want %#08x", gotFlags, wantFlags))
	}
	return cpu, diff
}

// TestQEMUITBlockSequence checks flag behaviour across two IT blocks with a
// 16-bit conditional MOV between them (the LLVM pattern used by TinyGo's SPI
// DMA setup).
func TestQEMUITBlockSequence(t *testing.T) {
	cases := []diffCase{{
		name: "it-seq",
		hw: []uint16{
			0x2000,         // movs r0,#0
			0x2800,         // cmp r0,#0
			0xF04F, 0x0144, // mov.w r1,#0x44
			0xBF08,         // it eq
			0x2104,         // moveq r1,#4
			0xF04F, 0x0211, // mov.w r2,#0x11
			0xF502, 0x2280, // add.w r2,r2,#0x40000
			0xF04F, 0x0148, // mov.w r1,#0x48
			0xBF08, // it eq
			0x2108, // moveq r1,#8
		},
		steps: 12,
	}}
	results := runOracle(t, cases)
	cpu, diff := compareOracleCase(cases[0], results[0])
	if len(diff) != 0 {
		t.Fatalf("IT sequence diverged: %s", strings.Join(diff, ", "))
	}
	if cpu.R[1] != 8 || cpu.R[2] != 0x40011 {
		t.Fatalf("IT sequence r1=%#x r2=%#x, want 8 and 0x40011", cpu.R[1], cpu.R[2])
	}
}

func TestQEMUITFlagRules(t *testing.T) {
	cases := []struct {
		diffCase
		wantFlags uint32
		wantR0    uint32
	}{
		{diffCase{name: "it-adds", hw: []uint16{0x2000, 0x2800, 0xBF08, 0x3001}, steps: 4}, 0x60000000, 1},
		{diffCase{name: "it-cmp", hw: []uint16{0x2000, 0x2800, 0xBF08, 0x2805}, steps: 4}, 0x80000000, 0},
		{diffCase{name: "it-tst", hw: []uint16{0x2000, 0x2800, 0xBF08, 0x4208}, steps: 4}, 0x60000000, 0},
	}
	oracleCases := make([]diffCase, len(cases))
	for i := range cases {
		oracleCases[i] = cases[i].diffCase
	}
	results := runOracle(t, oracleCases)
	for i := range cases {
		t.Run(cases[i].name, func(t *testing.T) {
			cpu, diff := compareOracleCase(cases[i].diffCase, results[i])
			if len(diff) != 0 {
				t.Fatalf("diverged: %s", strings.Join(diff, ", "))
			}
			if got := cpu.APSR & 0xF8000000; got != cases[i].wantFlags {
				t.Fatalf("APSR flags = %#x, want %#x", got, cases[i].wantFlags)
			}
			if cpu.R[0] != cases[i].wantR0 {
				t.Fatalf("r0 = %#x, want %#x", cpu.R[0], cases[i].wantR0)
			}
		})
	}
}

func TestQEMUBranchLink(t *testing.T) {
	cases := []struct {
		diffCase
		wantLR uint32
	}{
		{diffCase{name: "bl", hw: []uint16{0xF000, 0xF882}, steps: 1}, qemuCodeBase + 5},
		{diffCase{name: "blx_register", hw: []uint16{0x4780}, regs: [13]uint32{0: qemuCodeBase + 0x100}, steps: 1}, qemuCodeBase + 3},
	}
	oracleCases := make([]diffCase, len(cases))
	for i := range cases {
		oracleCases[i] = cases[i].diffCase
	}
	results := runOracle(t, oracleCases)
	for i := range cases {
		t.Run(cases[i].name, func(t *testing.T) {
			cpu, diff := compareOracleCase(cases[i].diffCase, results[i])
			if len(diff) != 0 {
				t.Fatalf("diverged: %s", strings.Join(diff, ", "))
			}
			if cpu.R[14] != cases[i].wantLR {
				t.Fatalf("LR = %#x, want %#x", cpu.R[14], cases[i].wantLR)
			}
		})
	}
}

func TestQEMUExpectedHalt(t *testing.T) {
	cases := []diffCase{{name: "udf", hw: []uint16{0xDE00}, steps: 1, expectHalt: true}}
	results := runOracle(t, cases)
	if _, diff := compareOracleCase(cases[0], results[0]); len(diff) != 0 {
		t.Fatalf("expected-halt case diverged: %s", strings.Join(diff, ", "))
	}
}
