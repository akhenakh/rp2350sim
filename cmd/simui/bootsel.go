package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
	shireiwidgets "go.hasen.dev/shirei/widgets"
)

var (
	bootselBrowserOpen bool
	bootselSelection   string
	bootselPath        string
	bootselMsg         string
	bootselErr         bool
	bootselZone        bool
)

// bootselPanel is the virtual RPI-RP2 drive. It appears when BOOTSEL is held or
// no firmware is loaded; dropping/choosing a UF2 flashes it with the BOOTSEL
// policy (any family id, flash targets only).
func bootselPanel(snap MachineState) {
	Container(Attrs(Grow(1), Expand, Gap(12), Pad4(8, 12, 8, 12)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("BOOTSEL", FontSize(16), FontWeight(700))
			Label("RPI-RP2 virtual drive", FontSize(12), FontWeight(600), TextColor(20, 60, 80, 1))
			Filler(1)
			if snap.BootselHeld {
				Label("held at reset", FontSize(11), TextColor(0, 70, 55, 1))
			} else if snap.Firmware == "" {
				Label("no firmware loaded", FontSize(11), TextColor(220, 6, 62, 1))
			}
		})

		// Drop zone. The desktop UI toolkit does not expose native file-drop
		// events, so a click opens the same .uf2 picker; the path field accepts
		// a pasted path for scripted/remote use.
		drop := Attrs(Grow(1), Expand, Pad4(24, 24, 24, 24), Gap(10), CrossMid,
			Background(220, 8, 16, 1), BorderWidth(1), BorderColor(220, 10, 32, 1), Corners(10))
		Container(drop, func() {
			st := ProcessToggleEvents(&bootselZone, false)
			if st.Clicked {
				openBootselDialog()
			}
			if st.Hovered {
				ModAttrs(Background(220, 10, 22, 1), BorderColor(140, 45, 42, 1))
			}
			Label("Drop a .uf2 here", FontSize(20), FontWeight(700))
			Label("Click to choose a file. Any UF2 family is accepted, but every block must land in flash.",
				FontSize(12), TextColor(220, 6, 62, 1))
			Label(".uf2 only · flash is erased before programming", FontSize(11), TextColor(220, 6, 62, 1))
		})

		Container(Attrs(Row, CrossMid, Gap(8)), func() {
			Label("Path", FontSize(12), FontWeight(600))
			attrs := DefaultTextInputAttrs()
			attrs.MinWidth = 420
			TextInputExt(&bootselPath, attrs)
			if Button(NoIcon, "Flash UF2") && strings.TrimSpace(bootselPath) != "" {
				flashUF2File(strings.TrimSpace(bootselPath))
			}
			if Button(NoIcon, "Choose…") {
				openBootselDialog()
			}
		})
		if bootselMsg != "" {
			col := TextColor(0, 70, 55, 1)
			if bootselErr {
				col = TextColor(220, 6, 62, 1)
			}
			Label(bootselMsg, FontSize(12), col)
		}
		Label("The real RP2350 bootrom presents a USB mass-storage drive; this panel is the emulated equivalent. "+
			"The image boots as soon as programming finishes.", FontSize(11), TextColor(220, 6, 62, 1))
	})
}

func openBootselDialog() {
	if webOpenFirmware() {
		return
	}
	bootselSelection = strings.TrimSpace(bootselPath)
	bootselBrowserOpen = true
	RequestNextFrame()
}

func bootselBrowserAttrs() shireiwidgets.FileBrowserAttrs {
	start := ""
	if p := strings.TrimSpace(bootselPath); p != "" {
		start = filepath.Dir(p)
	}
	return shireiwidgets.FileBrowserAttrs{
		Title: "Flash UF2 (BOOTSEL)",
		Width: 760,
		Files: true,
		Exts:  []string{".uf2"},
		Start: start,
	}
}

// bootselBrowserModal renders the .uf2 picker and flashes the accepted file.
func bootselBrowserModal() {
	if !bootselBrowserOpen {
		return
	}
	if !shireiwidgets.FileBrowserModal(&bootselBrowserOpen, &bootselSelection, bootselBrowserAttrs()) {
		return
	}
	path := strings.TrimSpace(bootselSelection)
	if path == "" {
		return
	}
	bootselPath = path
	flashUF2File(path)
}

func flashUF2File(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		bootselMsg, bootselErr = err.Error(), true
		RequestNextFrame()
		return
	}
	if err := emu.FlashUF2(filepath.Base(path), data); err != nil {
		bootselMsg, bootselErr = err.Error(), true
		RequestNextFrame()
		return
	}
	fwPath = path
	flashMode = false
	bootselMsg, bootselErr = fmt.Sprintf("flashed %s (%d bytes)", filepath.Base(path), len(data)), false
	activeTab = tabBoard
	RequestNextFrame()
}
