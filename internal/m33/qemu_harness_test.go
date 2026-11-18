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
		for r := 0; r < 13; r++ {
			fmt.Fprintf(&script, "set $r%d = 0x%08x\n", r, c.regs[r])
		}
		fmt.Fprintf(&script, "set $sp = 0x%08x\n", qemuStackTop)
		fmt.Fprintf(&script, "set $lr = 0x%08x\n", qemuCodeBase+0x101)
		fmt.Fprintf(&script, "set $xpsr = 0x%08x\n", c.cpsr|0x01000000)
		fmt.Fprintf(&script, "set $pc = 0x%08x\n", qemuCodeBase|1)
		steps := c.steps
		if steps < 1 {
			steps = 1
		}
		for k := 0; k < steps; k++ {
			script.WriteString("stepi\n")
		}
		fmt.Fprintf(&script, "printf \"CASE %d", i)
		for r := 0; r < 13; r++ {
			script.WriteString(" %08x")
		}
		script.WriteString(" %08x %08x %08x %08x\\n\"")
		args := []string{}
		for r := 0; r < 13; r++ {
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
	var out strings.Builder
	g.Stdout = &out
	g.Stderr = nil
	err = g.Run()
	text := out.String()
	if err != nil && len(text) < len(cases) {
		t.Fatalf("gdb failed: %v\n%s", err, text)
	}

	results := make([]oracleResult, len(cases))
	seen := make([]bool, len(cases))
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "CASE ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 19 {
			continue
		}
		var idx int
		fmt.Sscanf(f[1], "%d", &idx)
		if idx < 0 || idx >= len(cases) {
			continue
		}
		var vals [17]uint32
		ok := true
		for k := 0; k < 17; k++ {
			if _, err := fmt.Sscanf(f[2+k], "%x", &vals[k]); err != nil {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		res := oracleResult{XPSR: vals[16]}
		res.Regs = vals[:16]
		results[idx] = res
		seen[idx] = true
	}
	for i := range results {
		if !seen[i] {
			results[i].Halt = "no result"
		}
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
		want := results[i]
		if want.Halt != "" {
			// The oracle could not run this case; skip it.
			continue
		}
		cpu, _ := runLocal(c)
		if cpu.Halted {
			t.Errorf("%s: local halted: %s", c.name, cpu.HaltReason)
			failures++
			continue
		}
		var diff []string
		for r := 0; r < 13; r++ {
			if cpu.R[r] != want.Regs[r] {
				diff = append(diff, fmt.Sprintf("r%d got %#08x want %#08x", r, cpu.R[r], want.Regs[r]))
			}
		}
		if cpu.R[13] != want.Regs[13] {
			diff = append(diff, fmt.Sprintf("sp got %#08x want %#08x", cpu.R[13], want.Regs[13]))
		}
		if cpu.R[15] != want.Regs[15] {
			diff = append(diff, fmt.Sprintf("pc got %#08x want %#08x", cpu.R[15], want.Regs[15]))
		}
		gotFlags := cpu.APSR & 0xF8000000
		wantFlags := want.XPSR & 0xF8000000
		if gotFlags != wantFlags {
			diff = append(diff, fmt.Sprintf("apsr got %#08x want %#08x", gotFlags, wantFlags))
		}
		if len(diff) > 0 {
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
	for i := 0; i < 13; i++ {
		cpu.R[i] = c.regs[i]
	}
	steps := c.steps
	if steps < 1 {
		steps = 1
	}
	for i := 0; i < steps; i++ {
		cpu.Step()
	}
	return cpu, bus
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
	got := results[0]
	t.Logf("qemu r1=%#x r2=%#x apsr=%#x halt=%q", got.Regs[1], got.Regs[2], got.XPSR, got.Halt)
	if got.Halt != "" {
		t.Skipf("oracle could not run: %s", got.Halt)
	}
	cpu, _ := runLocal(cases[0])
	t.Logf("local r1=%#x r2=%#x apsr=%#x", cpu.R[1], cpu.R[2], cpu.APSR)
	if cpu.R[1] != got.Regs[1] {
		t.Fatalf("r1 local %#x qemu %#x", cpu.R[1], got.Regs[1])
	}
}

func TestQEMUITFlagRules(t *testing.T) {
	cases := []diffCase{
		{name: "it-adds", hw: []uint16{0x2000, 0x2800, 0xBF08, 0x3001}, steps: 4},
		{name: "it-cmp", hw: []uint16{0x2000, 0x2800, 0xBF08, 0x2805}, steps: 4},
		{name: "it-tst", hw: []uint16{0x2000, 0x2800, 0xBF08, 0x4208}, steps: 4},
	}
	results := runOracle(t, cases)
	for i, got := range results {
		t.Logf("%s: qemu apsr=%#x halt=%q", cases[i].name, got.XPSR, got.Halt)
		if got.Halt != "" {
			continue
		}
		cpu, _ := runLocal(cases[i])
		t.Logf("%s: local apsr=%#x", cases[i].name, cpu.APSR)
	}
}
