package main

import (
	"fmt"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

func canMode(v byte) string {
	switch (v >> 5) & 7 {
	case 0:
		return "normal"
	case 1:
		return "sleep"
	case 2:
		return "loopback"
	case 3:
		return "listen-only"
	case 4:
		return "configuration"
	}
	return "reserved"
}

// canPanel shows the MCP2515 registers and the recent CAN bus frames.
func canPanel() {
	info, frames, ok := emu.CANInfo()
	if !ok {
		Label("No CAN controller on this board.", FontSize(13), TextColor(220, 6, 68, 1))
		return
	}
	Container(Attrs(Grow(1), Expand, Gap(10), Pad4(4, 6, 4, 6)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("CAN", FontSize(16), FontWeight(700))
			Label("MCP2515 · classic CAN 2.0B", FontSize(12))
			Filler(1)
			Label(fmt.Sprintf("%d frames", len(frames)), FontSize(12), FontWeight(600))
		})
		Container(Attrs(Row, Gap(10)), func() {
			statCard("Mode", canMode(info.CANSTAT))
			statCard("CANSTAT", fmt.Sprintf("%#02x", info.CANSTAT))
			statCard("CANCTRL", fmt.Sprintf("%#02x", info.CANCTRL))
			statCard("CANINTF", fmt.Sprintf("%#02x", info.CANINTF))
			statCard("EFLG", fmt.Sprintf("%#02x", info.EFLG))
		})
		Container(Attrs(Grow(1), Expand, Gap(4), Pad2(4, 8), Background(220, 10, 12, 1),
			Corners(6), Clip, Viewport), func() {
			ScrollOnInput()
			Label("Bus frames", FontSize(12), FontWeight(700))
			Container(Attrs(Row, Gap(8)), func() {
				colLabel(90, "time", FontSize(10), TextColor(220, 6, 68, 1))
				colLabel(80, "id", FontSize(10), TextColor(220, 6, 68, 1))
				colLabel(60, "type", FontSize(10), TextColor(220, 6, 68, 1))
				colLabel(45, "dlc", FontSize(10), TextColor(220, 6, 68, 1))
				Label("data", FontSize(10), TextColor(220, 6, 68, 1))
			})
			if len(frames) == 0 {
				Label("waiting for traffic — the peer answers a matching request", FontSize(12),
					TextColor(220, 6, 68, 1))
			}
			limit := max(len(frames)-100, 0)
			for i := len(frames) - 1; i >= limit; i-- {
				f := frames[i]
				typ := "STD"
				if f.Ext {
					typ = "EXT"
				}
				if f.RTR {
					typ += "/RTR"
				}
				Container(Attrs(Row, Gap(8)), func() {
					colLabel(90, fmt.Sprintf("%8.2f ms", float64(f.Time)/150_000), Fonts(Monospace...), FontSize(11))
					colLabel(80, fmt.Sprintf("%03X", f.ID), Fonts(Monospace...), FontSize(11))
					colLabel(60, typ, FontSize(11))
					colLabel(45, fmt.Sprintf("%d", len(f.Data)), Fonts(Monospace...), FontSize(11))
					Label(fmt.Sprintf("% X", f.Data), Fonts(Monospace...), FontSize(11))
				})
			}
		})
		Label("CAN FD and bus-off/error-frame injection are separate controller follow-ups.",
			FontSize(11), TextColor(220, 6, 68, 1))
	})
}
