package main

import (
	"fmt"
	"slices"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// gpsPanel shows the attached NMEA source and a few parsed fields. The GPS
// itself is a UART device; the panel reads its last emitted sentence.
func gpsPanel() {
	info := emu.GPSInfo()
	Container(Attrs(Grow(1), Expand, Gap(12), Pad4(4, 6, 4, 6)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("GPS", FontSize(16), FontWeight(700))
			Label(fmt.Sprintf("NMEA 0183 · UART%d", info.UART), FontSize(12))
			Filler(1)
			if info.Fix {
				Label("FIX", FontSize(12), FontWeight(700), TextColor(40, 75, 65, 1))
			} else {
				Label("NO FIX", FontSize(12), FontWeight(700), TextColor(220, 6, 62, 1))
			}
		})
		Container(Attrs(Row, Gap(10)), func() {
			statCard("Latitude", fmt.Sprintf("%.6f", info.Lat))
			statCard("Longitude", fmt.Sprintf("%.6f", info.Lon))
			statCard("Satellites", fmt.Sprintf("%d", info.Satellites))
			statCard("Altitude", fmt.Sprintf("%.1f m", info.Altitude))
			statCard("Speed", fmt.Sprintf("%.2f kn", info.SpeedKnots))
			statCard("Course", fmt.Sprintf("%.1f°", info.Course))
		})
		Container(Attrs(Grow(1), Expand, Gap(6), Pad(12), Corners(8),
			Background(220, 12, 12, 1), BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
			Container(Attrs(Row, CrossMid, Gap(8)), func() {
				Label("NMEA log", FontSize(12), FontWeight(700))
				Label(fmt.Sprintf("%d sentences emitted", info.Sentences), FontSize(11),
					TextColor(220, 6, 68, 1))
				Filler(1)
				if Button(NoIcon, "Copy") {
					RequestTextCopy(strings.Join(info.History, ""))
				}
			})
			Container(Attrs(Grow(1), Expand, Clip, Viewport, Gap(2), Pad2(4, 6),
				Background(220, 12, 8, 1), Corners(4)), func() {
				ScrollOnInput()
				ScrollBars()
				if len(info.History) == 0 {
					Label("waiting for the first sentence…", FontSize(12))
					return
				}
				for _, line := range slices.Backward(info.History) {
					Label(strings.TrimRight(line, "\r\n"), Fonts(Monospace...), FontSize(12),
						TextColor(150, 45, 72, 1))
				}
			})
		})
		Label("The same NMEA stream is available on UART receive; use the Logic tab to decode it.",
			FontSize(11), TextColor(220, 6, 68, 1))
	})
}
