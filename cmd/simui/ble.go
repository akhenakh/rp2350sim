package main

import (
	"fmt"
	"slices"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// bleText renders the controller state and recent HCI events for Copy.
func bleText(info BLEState) string {
	var b strings.Builder
	b.WriteString("rp2350sim BLE\n")
	fmt.Fprintf(&b, "Address: %s\n", info.Address)
	fmt.Fprintf(&b, "Peer: %s\n", info.Peer)
	fmt.Fprintf(&b, "Connected: %v  MTU: %d  notifications: %v  pending: %d\n\n",
		info.Connected, info.MTU, info.Notifications, info.Pending)
	b.WriteString("HCI events (newest first):\n")
	for _, ev := range slices.Backward(info.Events) {
		b.WriteString(ev)
		b.WriteByte('\n')
	}
	return b.String()
}

// blePanel shows the virtual CYW43 controller and the HCI packets crossing the
// transport. The HCI log is the same traffic written by -ble-hci-log.
func blePanel(snap MachineState) {
	if snap.BLE == nil {
		Label("No virtual BLE controller on this board.", FontSize(13), TextColor(220, 6, 68, 1))
		return
	}
	info := *snap.BLE
	Container(Attrs(Grow(1), Expand, Gap(10), Pad4(4, 6, 4, 6)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("BLE", FontSize(16), FontWeight(700))
			Label("CYW43 HCI · logical controller", FontSize(12))
			Filler(1)
			if Button(NoIcon, "Copy") {
				RequestTextCopy(bleText(info))
			}
			state := "idle"
			if info.Connected {
				state = "connected"
			}
			Label(state, FontSize(12), FontWeight(600))
		})
		Container(Attrs(Row, Gap(10)), func() {
			statCard("Address", info.Address)
			statCard("Peer", info.Peer)
			statCard("State", boolText(info.Connected, "connected", "advertising"))
			statCard("MTU", fmt.Sprintf("%d", info.MTU))
			statCard("Notify", boolText(info.Notifications, "on", "off"))
			statCard("Pending", fmt.Sprintf("%d", info.Pending))
		})
		Container(Attrs(Grow(1), Expand, Gap(4), Pad2(4, 8), Background(220, 10, 12, 1),
			Corners(6), Clip, Viewport), func() {
			ScrollOnInput()
			Label("HCI packets", FontSize(12), FontWeight(700))
			Label("host → controller is >, controller → host is <", FontSize(10),
				TextColor(220, 6, 68, 1))
			if len(info.Events) == 0 {
				Label("waiting for HCI traffic — run the stock Pico W BTstack firmware",
					FontSize(12), TextColor(220, 6, 68, 1))
				return
			}
			limit := max(len(info.Events)-200, 0)
			for i := len(info.Events) - 1; i >= limit; i-- {
				Label(info.Events[i], Fonts(Monospace...), FontSize(11))
			}
		})
		Label("One connection; no pairing, LESC or extended advertising. HCI pcapng: -ble-hci-log FILE.",
			FontSize(11), TextColor(220, 6, 68, 1))
	})
}

func boolText(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}
