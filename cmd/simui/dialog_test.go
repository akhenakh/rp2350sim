package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestFirmwareBrowserAttrs(t *testing.T) {
	oldPath, oldFlash := fwPath, flashMode
	t.Cleanup(func() { fwPath, flashMode = oldPath, oldFlash })

	fwPath = filepath.Join(t.TempDir(), "images", "demo.elf")
	flashMode = true

	attrs := firmwareBrowserAttrs()
	if attrs.Title != "Load firmware" || !attrs.Files || attrs.Dirs {
		t.Fatalf("attrs = %+v, want firmware file picker", attrs)
	}
	if attrs.Start != filepath.Dir(fwPath) {
		t.Fatalf("start = %q, want %q", attrs.Start, filepath.Dir(fwPath))
	}
	if !slices.Equal(attrs.Exts, []string{".uf2", ".elf", ".bin"}) {
		t.Fatalf("extensions = %v", attrs.Exts)
	}
}

func TestOpenFirmwareDialogSeedsSelection(t *testing.T) {
	oldPath := fwPath
	oldOpen, oldSelection := firmwareBrowserOpen, firmwareBrowserSelection
	t.Cleanup(func() {
		fwPath = oldPath
		firmwareBrowserOpen, firmwareBrowserSelection = oldOpen, oldSelection
	})

	fwPath = "  /tmp/rp2350/firmware.elf  "
	firmwareBrowserOpen = false
	firmwareBrowserSelection = ""

	openFirmwareDialog()
	if !firmwareBrowserOpen {
		t.Fatal("firmware browser did not open")
	}
	if firmwareBrowserSelection != "/tmp/rp2350/firmware.elf" {
		t.Fatalf("selection = %q", firmwareBrowserSelection)
	}
}
