package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/ncruces/zenity"
	"go.hasen.dev/shirei"
)

// dialogOpen guards against opening a second file dialog while one is showing.
var dialogOpen atomic.Bool

// openFirmwareDialog asks the OS for a firmware image and loads it. It runs the
// dialog on a goroutine: the frame loop must not block on a modal window.
func openFirmwareDialog() {
	if !dialogOpen.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer dialogOpen.Store(false)

		if !zenity.IsAvailable() {
			// No native dialog backend: fall back to the path in the toolbar.
			if p := strings.TrimSpace(fwPath); p != "" {
				emu.Load(p)
			}
			shirei.RequestNextFrame()
			return
		}

		options := []zenity.Option{
			zenity.Title("Load TinyGo firmware"),
			zenity.FileFilter{
				Name:     "Firmware images",
				Patterns: []string{"*.uf2", "*.elf", "*.bin"},
				CaseFold: true,
			},
			zenity.FileFilter{Name: "All files", Patterns: []string{"*"}},
		}
		if p := strings.TrimSpace(fwPath); p != "" {
			options = append(options, zenity.Filename(p))
		}

		path, err := zenity.SelectFile(options...)
		if err != nil {
			if errors.Is(err, zenity.ErrCanceled) {
				return
			}
			_ = zenity.Error(fmt.Sprintf("Could not open the file dialog:\n%v", err),
				zenity.Title("rp2350sim"))
			return
		}
		if path == "" {
			return
		}
		if err := emu.Load(path); err != nil {
			_ = zenity.Error(err.Error(), zenity.Title("rp2350sim"))
			return
		}
		shirei.WithFrameLock(func() { fwPath = path })
		shirei.RequestNextFrame()
	}()
}
