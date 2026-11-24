package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildDrawerRunsCommand(t *testing.T) {
	bp := newBuildDrawerState()
	bp.buildDir = t.TempDir()
	bp.buildCmd = "printf 'hello from build\\n'"
	bp.StartBuild()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if running, status := bp.snapshot(); !running {
			if !strings.Contains(status, "succeeded") {
				t.Fatalf("status = %q", status)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running, status := bp.snapshot(); running {
		t.Fatalf("build still running: %s", status)
	}
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.log.Len() < 2 {
		t.Fatalf("log has %d lines", bp.log.Len())
	}
	var text strings.Builder
	for i := range bp.log.Len() {
		text.WriteString(bp.log.Line(i))
		text.WriteByte('\n')
	}
	if !strings.Contains(text.String(), "hello from build") {
		t.Fatalf("log = %q", text.String())
	}
	if !strings.Contains(text.String(), "$ printf") {
		t.Fatalf("log is missing the command echo: %q", text.String())
	}
}

func TestBuildDrawerFailure(t *testing.T) {
	bp := newBuildDrawerState()
	bp.buildDir = t.TempDir()
	bp.buildCmd = "exit 3"
	bp.StartBuild()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if running, status := bp.snapshot(); !running {
			if !strings.Contains(status, "failed") {
				t.Fatalf("status = %q", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("build did not finish")
}

func TestInferBuildPlanTinyGo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "firmware", "blinky")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fw := filepath.Join(root, "build", "blinky.elf")
	dir, cmd, source := inferBuildPlan(fw)
	if dir != src {
		t.Fatalf("dir = %q, want %q", dir, src)
	}
	if !strings.HasPrefix(cmd, "tinygo build") || !strings.Contains(cmd, fw) {
		t.Fatalf("cmd = %q", cmd)
	}
	if source != filepath.Join(src, "main.go") {
		t.Fatalf("source = %q", source)
	}
}

func TestInferBuildPlanCMake(t *testing.T) {
	root := t.TempDir()
	c := filepath.Join(root, "firmware", "c")
	if err := os.MkdirAll(filepath.Join(c, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c, "CMakeLists.txt"), []byte("project(x)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fw := filepath.Join(c, "build", "hello.elf")
	dir, cmd, source := inferBuildPlan(fw)
	if dir != c {
		t.Fatalf("dir = %q, want %q", dir, c)
	}
	if !strings.HasPrefix(cmd, "cmake --build ") || !strings.Contains(cmd, filepath.Join(c, "build")) {
		t.Fatalf("cmd = %q", cmd)
	}
	if source != "" {
		t.Fatalf("source = %q", source)
	}
}
