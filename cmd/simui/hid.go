package main

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var (
	hidLED     byte
	hidSendHex string
)

// hidPanel shows the USB HID device the firmware enumerates and its decoded
// input reports. The emulator is the USB host: the firmware (the device) sends
// keyboard/mouse/gamepad reports, while the only host-to-device traffic is an
// output report such as keyboard LEDs or gamepad feedback.
func hidPanel(snap MachineState) {
	events := emu.HIDEvents()
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("USB HID", FontSize(16), FontWeight(700))
			if len(snap.HID) == 0 {
				Label("waiting for a HID interface", FontSize(12), TextColor(220, 6, 62, 1))
			}
			for _, d := range snap.HID {
				kinds := strings.Join(d.Kinds, "+")
				if kinds == "" {
					kinds = "hid"
				}
				Label(fmt.Sprintf("iface %d · %s · EP%d in/out", d.Interface, kinds, d.InEndpoint),
					FontSize(11), FontWeight(600), TextColor(20, 60, 80, 1))
			}
			Filler(1)
			if Button(NoIcon, "Clear") {
				emu.ClearHIDEvents()
				RequestNextFrame()
			}
		})

		// Monitor: newest report first.
		Container(Attrs(Grow(1), Expand, Clip, Viewport, Gap(2), Pad2(4, 6),
			Background(220, 10, 12, 1), Corners(6)), func() {
			ScrollOnInput()
			if len(events) == 0 {
				Label("waiting for HID reports", FontSize(12), TextColor(220, 6, 62, 1))
				return
			}
			for i := len(events) - 1; i >= 0 && i >= len(events)-200; i-- {
				ev := events[i]
				Container(Attrs(Row, Gap(12)), func() {
					Label(fmt.Sprintf("%8.3fs", float64(ev.Cycle)/150e6), Fonts(Monospace...), FontSize(11),
						TextColor(220, 6, 62, 1))
					Label(ev.Kind, FontSize(11), FontWeight(600), TextColor(140, 45, 72, 1))
					Label(ev.Text, FontSize(12))
					Label(ev.Bytes, Fonts(Monospace...), FontSize(11), TextColor(140, 45, 72, 1))
				})
			}
		})

		// Host-to-device output: keyboard LEDs and a generic report field.
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			if keyboard := hidKeyboard(snap); keyboard != nil && keyboard.OutEndpoint > 0 {
				Label("Keyboard LEDs", FontSize(12), FontWeight(600))
				for bit, name := range []string{"Num", "Caps", "Scroll"} {
					active := hidLED&(1<<uint(bit)) != 0
					label := name
					if active {
						label = name + " *"
					}
					if Button(NoIcon, label) {
						hidLED ^= 1 << uint(bit)
						id := keyboard.OutputReportID
						if id == 0 {
							id = keyboard.KeyboardReportID
						}
						if id != 0 {
							emu.SendHID([]byte{id, hidLED})
						} else {
							emu.SendHID([]byte{hidLED})
						}
						RequestNextFrame()
					}
				}
			}
			Label("Output report", FontSize(12), FontWeight(600))
			attrs := DefaultTextInputAttrs()
			attrs.MinWidth = 180
			TextInputExt(&hidSendHex, attrs)
			if Button(NoIcon, "Send hex") {
				data, err := hex.DecodeString(strings.TrimSpace(hidSendHex))
				if err == nil && len(data) > 0 {
					emu.SendHID(data)
					hidSendHex = ""
				}
				RequestNextFrame()
			}
		})
		Label("The firmware is the HID device; the emulator is the host. Reports above come from the device, the output controls go back on the interrupt OUT endpoint.",
			FontSize(11), TextColor(220, 6, 62, 1))
	})
}

func hidKeyboard(snap MachineState) *HIDDeviceState {
	for i := range snap.HID {
		if slices.Contains(snap.HID[i].Kinds, "keyboard") {
			return &snap.HID[i]
		}
	}
	return nil
}
