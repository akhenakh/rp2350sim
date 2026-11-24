package main

import (
	"path/filepath"
	"strings"

	"go.hasen.dev/shirei"
	"go.hasen.dev/shirei/widgets"
)

var (
	firmwareBrowserOpen      bool
	firmwareBrowserSelection string
)

// openFirmwareDialog opens the in-window Shirei file browser. The modal is
// rendered by RootView on the UI thread, so no native dialog or goroutine is
// involved.
func openFirmwareDialog() {
	firmwareBrowserSelection = strings.TrimSpace(fwPath)
	firmwareBrowserOpen = true
	shirei.RequestNextFrame()
}

func firmwareBrowserAttrs() widgets.FileBrowserAttrs {
	start := ""
	if p := strings.TrimSpace(fwPath); p != "" {
		start = filepath.Dir(p)
	}
	return widgets.FileBrowserAttrs{
		Title: "Load firmware",
		Width: 760,
		Files: true,
		Exts:  []string{".uf2", ".elf", ".bin"},
		Start: start,
	}
}

// firmwareBrowserModal renders the firmware picker and loads an accepted file.
// Load errors are published through Emu.Snapshot().Err and shown by the toolbar.
func firmwareBrowserModal() {
	if !firmwareBrowserOpen {
		return
	}
	if !widgets.FileBrowserModal(&firmwareBrowserOpen, &firmwareBrowserSelection, firmwareBrowserAttrs()) {
		return
	}
	path := strings.TrimSpace(firmwareBrowserSelection)
	if path == "" {
		return
	}
	if err := emu.Load(path); err != nil {
		shirei.RequestNextFrame()
		return
	}
	fwPath = path
	flashMode = false
	watchFile(path, "firmware")
	shirei.RequestNextFrame()
}
