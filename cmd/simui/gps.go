package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

var (
	gpsEditField string
	gpsEditBuf   string
	gpsEditOrig  string
	gpsEditErr   string
)

// applyGPSField parses one edited field and pushes the whole configuration.
func applyGPSField(field, text string, info *devices.GPSInfo) {
	v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		gpsEditErr = "number expected"
		return
	}
	lat, lon, alt, speed, course := info.Lat, info.Lon, info.Altitude, info.SpeedKnots, info.Course
	switch field {
	case "lat":
		lat = v
	case "lon":
		lon = v
	case "alt":
		alt = v
	case "speed":
		speed = v
	case "course":
		course = v
	}
	if err := emu.SetGPSConfig(lat, lon, alt, speed, course); err != nil {
		gpsEditErr = err.Error()
		return
	}
	gpsEditErr = ""
}

// gpsFieldValue is the numeric text used when a field enters edit mode.
func gpsFieldValue(field string, info *devices.GPSInfo) string {
	switch field {
	case "lat":
		return fmt.Sprintf("%.6f", info.Lat)
	case "lon":
		return fmt.Sprintf("%.6f", info.Lon)
	case "alt":
		return fmt.Sprintf("%.1f", info.Altitude)
	case "speed":
		return fmt.Sprintf("%.2f", info.SpeedKnots)
	case "course":
		return fmt.Sprintf("%.1f", info.Course)
	}
	return ""
}

// gpsEditableCard renders one read-only value or an active text field. A
// double-click enters edit mode; the field auto-focuses, applies live, and
// commits on Enter (Escape restores the value it opened with).
func gpsEditableCard(title, field, value string, info *devices.GPSInfo) {
	if gpsEditField == field {
		Container(Attrs(Grow(1), Gap(2), Pad(10), Corners(8), Background(220, 12, 12, 1),
			BorderWidth(1), BorderColor(140, 45, 70, 1)), func() {
			Label(title, FontSize(11), TextColor(220, 6, 68, 1))
			attrs := DefaultTextInputAttrs()
			attrs.MinWidth = 120
			attrs.FixedWidth = true
			TextInputExt(&gpsEditBuf, attrs)
			if gpsEditErr != "" {
				Label(gpsEditErr, FontSize(10), TextColor(0, 65, 55, 1))
			}
		})
		key := GetFrameInput().Key
		outside := GetFrameInput().Mouse.Has(MouseClick) && !IsHovered()
		switch {
		case key == KeyEscape:
			applyGPSField(field, gpsEditOrig, info)
			gpsEditField, gpsEditBuf, gpsEditErr = "", "", ""
			Blur()
		case key == KeyEnter, outside && key == 0:
			gpsEditField, gpsEditBuf, gpsEditErr = "", "", ""
			Blur()
		default:
			applyGPSField(field, gpsEditBuf, info)
		}
		return
	}
	Container(Attrs(Grow(1), Gap(2), Pad(10), Corners(8), Background(220, 12, 12, 1),
		BorderWidth(1), BorderColor(220, 8, 26, 1)), func() {
		Label(title, FontSize(11), TextColor(220, 6, 68, 1))
		Label(value, FontSize(17), FontWeight(600), Fonts(Monospace...))
		if IsDoubleClicked() {
			edit := gpsFieldValue(field, info)
			gpsEditField, gpsEditBuf, gpsEditOrig, gpsEditErr = field, edit, edit, ""
			RequestNextFrame()
		}
	})
}

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
		Label("Double-click a value to edit it; Enter commits, Escape restores.",
			FontSize(11), TextColor(220, 6, 68, 1))
		Container(Attrs(Row, Gap(10)), func() {
			gpsEditableCard("Latitude", "lat", fmt.Sprintf("%.6f", info.Lat), info)
			gpsEditableCard("Longitude", "lon", fmt.Sprintf("%.6f", info.Lon), info)
			statCard("Satellites", fmt.Sprintf("%d", info.Satellites))
			gpsEditableCard("Altitude", "alt", fmt.Sprintf("%.1f m", info.Altitude), info)
			gpsEditableCard("Speed", "speed", fmt.Sprintf("%.2f kn", info.SpeedKnots), info)
			gpsEditableCard("Course", "course", fmt.Sprintf("%.1f°", info.Course), info)
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
