package main

import (
	"fmt"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// The virtual keyboard follows a real piano geometry: white keys share the
// full width, black keys are narrower, shorter and floated on top of the
// boundary between two whites. Interaction happens at the bed level (not per
// key) so dragging across the keys plays a glissando.

func isBlackKey(m int) bool {
	switch ((m % 12) + 12) % 12 {
	case 1, 3, 6, 8, 10:
		return true
	}
	return false
}

// pianoKeyLayout is the horizontal geometry of one key in pixels.
type pianoKeyLayout struct {
	X, W float32
}

// buildKeyLayout assigns each note its x/width. White keys tile the width;
// black keys straddle the boundary and are 60% as wide.
func buildKeyLayout(width float32, min, max int) []pianoKeyLayout {
	whites := 0
	for m := min; m <= max; m++ {
		if !isBlackKey(m) {
			whites++
		}
	}
	out := make([]pianoKeyLayout, max-min+1)
	if whites == 0 || width <= 0 {
		return out
	}
	ww := width / float32(whites)
	wi := 0
	for m := min; m <= max; m++ {
		i := m - min
		if !isBlackKey(m) {
			out[i] = pianoKeyLayout{X: float32(wi) * ww, W: ww}
			wi++
		} else {
			out[i] = pianoKeyLayout{X: (float32(wi) - 0.30) * ww, W: 0.60 * ww}
		}
	}
	return out
}

// keyAt returns the note under x, preferring the black key when the point is
// inside both.
func keyAt(x float32, layout []pianoKeyLayout, min, max int) int {
	for m := max; m >= min; m-- {
		k := layout[m-min]
		if !isBlackKey(m) {
			continue
		}
		if x >= k.X && x <= k.X+k.W {
			return m
		}
	}
	if len(layout) == 0 {
		return min
	}
	ww := layout[0].W
	idx := int(x / ww)
	if idx < 0 {
		return min
	}
	seen := 0
	for m := min; m <= max; m++ {
		if isBlackKey(m) {
			continue
		}
		if seen == idx {
			return m
		}
		seen++
	}
	return max
}

var (
	pianoWhite       = Vec4{240, 14, 90, 1}
	pianoWhiteBorder = Vec4{240, 9, 58, 1}
	pianoBlack       = Vec4{240, 17, 9, 1}
	pianoBlackBorder = Vec4{0, 0, 4, 1}
	pianoLit         = Vec4{150, 60, 62, 1}
	pianoBed         = Vec4{240, 16, 4, 1}
	pianoLabel       = Vec4{240, 6, 42, 1}
)

// pianoKeyboard draws min..max inclusive and reports presses/releases. active
// holds the notes to light (firmware playback and UI-held keys alike).
func pianoKeyboard(min, max int, height float32, active map[int]bool, onDown, onUp func(int)) {
	Container(Attrs(FixHeight(height), Expand, NoAnimate, Clip, BackgroundVec(pianoBed)), func() {
		w := GetContentWidth()
		h := GetContentHeight()

		last := Use[int]("piano-last")
		st := ProcessButtonEvents(false)
		if onDown != nil || onUp != nil {
			rect := GetScreenRect()
			x := GetInputState().MousePoint[0] - rect.Origin[0]
			layout := buildKeyLayout(rect.Size[0], min, max)
			if st.Active && x >= 0 && x <= rect.Size[0] {
				key := keyAt(x, layout, min, max)
				if *last-1 != key {
					if *last > 0 && onUp != nil {
						onUp(*last - 1)
					}
					if onDown != nil {
						onDown(key)
					}
					*last = key + 1
				}
			} else if *last > 0 {
				if onUp != nil {
					onUp(*last - 1)
				}
				*last = 0
			}
		}

		layout := buildKeyLayout(w, min, max)
		blackH := h * 0.62

		for m := min; m <= max; m++ {
			if isBlackKey(m) {
				continue
			}
			k := layout[m-min]
			bg, border := pianoWhite, pianoWhiteBorder
			if active[m] {
				bg, border = pianoLit, shade(pianoLit, -18)
			}
			Container(Attrs(Float(k.X, 0), FixSize(k.W-1, h), NoAnimate,
				BackgroundVec(bg), Corners4(0, 0, 4, 4),
				BorderWidth(1), BorderColorVec(border)), func() {
				if m%12 == 0 {
					Container(Attrs(Float(0, h-20), FixSize(k.W-1, 18),
						MainAlign(AlignMiddle), CrossAlign(AlignMiddle)), func() {
						col := pianoLabel
						if active[m] {
							col = Vec4{0, 0, 16, 1}
						}
						Label(midiNoteName(m), FontSize(10), TextColorVec(col))
					})
				}
			})
		}

		for m := min; m <= max; m++ {
			if !isBlackKey(m) {
				continue
			}
			k := layout[m-min]
			bg, border := pianoBlack, pianoBlackBorder
			if active[m] {
				bg, border = shade(pianoLit, -22), shade(pianoLit, -35)
			}
			Container(Attrs(Float(k.X, 0), Z(2), FixSize(k.W, blackH), NoAnimate,
				BackgroundVec(bg), Corners4(0, 0, 3, 3),
				BorderWidth(1), BorderColorVec(border), BoxShadow(3)), func() {})
		}
	})
}

// midiNoteName is the conventional name (C4 = 60) used on the key labels.
func midiNoteName(n int) string {
	names := [...]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	return fmt.Sprintf("%s%d", names[((n%12)+12)%12], n/12-1)
}

func shade(c Vec4, delta float32) Vec4 {
	c[2] = min(max(c[2]+delta, 0), 100)
	return c
}
