package main

import (
	"fmt"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"

	"github.com/akhenakh/rp2350sim/internal/midi"
)

var (
	midiOctave  = 5
	midiChannel = 0
	midiHeld    = map[int]bool{}
)

// midiPanel is the MIDI monitor plus a clickable chromatic keyboard. Key
// presses are tracked per frame: a held key sends Note On once on press and
// Note Off on release, so dragging across the keyboard works like a piano.
func midiPanel(snap MachineState) {
	events := emu.MIDIEvents()
	Container(Attrs(Grow(1), Expand, Gap(8)), func() {
		Container(Attrs(Row, CrossMid, Gap(10)), func() {
			Label("MIDI", FontSize(16), FontWeight(700))
			if selectedBoard != nil && selectedBoard.MIDI != nil {
				kind := "USB"
				if selectedBoard.MIDI.Kind == "uart" {
					kind = fmt.Sprintf("UART%d", selectedBoard.MIDI.UART)
				}
				Label(kind, FontSize(11), FontWeight(600), TextColor(220, 6, 62, 1))
			}
			Label(fmt.Sprintf("channel %d", midiChannel+1), FontSize(12))
			Filler(1)
			if Button(NoIcon, "- Octave") {
				if midiOctave > 2 {
					midiOctave--
				}
				RequestNextFrame()
			}
			Label(midi.NoteName(midiOctave*12), FontSize(13), FontWeight(600))
			if Button(NoIcon, "+ Octave") {
				if midiOctave < 9 {
					midiOctave++
				}
				RequestNextFrame()
			}
			if Button(NoIcon, "Panic") {
				panicMIDI()
				RequestNextFrame()
			}
		})

		// Monitor: newest message first, so playing a note is visible at the
		// top without scrolling.
		Container(Attrs(Grow(1), Expand, Clip, Viewport, Gap(2), Pad2(4, 6),
			Background(220, 10, 12, 1), Corners(6)), func() {
			ScrollOnInput()
			if len(events) == 0 {
				Label("waiting for MIDI traffic", FontSize(12), TextColor(220, 6, 62, 1))
				return
			}
			for i := len(events) - 1; i >= 0 && i >= len(events)-200; i-- {
				ev := events[i]
				Container(Attrs(Row, Gap(12)), func() {
					Label(fmt.Sprintf("%8.3fs", float64(ev.Cycle)/150e6), Fonts(Monospace...), FontSize(11),
						TextColor(220, 6, 62, 1))
					Label(ev.Bytes, Fonts(Monospace...), FontSize(11), TextColor(140, 45, 72, 1))
					Label(ev.Text, FontSize(12))
				})
			}
		})

		// Keyboard: a real piano layout spanning the full panel width. Held
		// UI notes and notes the firmware is playing both light up.
		base := midiOctave * 12
		hi := min(base+23, 127)
		active := emu.SoundingNotes()
		for note := range midiHeld {
			active[note] = true
		}
		pianoKeyboard(base, hi, 260, active, func(note int) {
			midiHeld[note] = true
			sendMIDI(midi.Message{Status: 0x90 | byte(midiChannel), Data: [2]byte{byte(note), 100}, Len: 3})
		}, func(note int) {
			delete(midiHeld, note)
			sendMIDI(midi.Message{Status: 0x80 | byte(midiChannel), Data: [2]byte{byte(note), 0}, Len: 3})
		})
		Label("Click or drag the keys · host-to-device MIDI uses the board's USB-MIDI or UART endpoint.",
			FontSize(11), TextColor(220, 6, 62, 1))
	})
}

// panicMIDI releases every note the UI thinks it pressed and sends All Notes
// Off on every channel.
func panicMIDI() {
	// Make room for release messages even if the normal queue is full, then
	// let SetBoard flush the whole sequence to the old transport.
	emu.FlushMIDI()
	for note := range midiHeld {
		sendMIDI(midi.Message{Status: 0x80 | byte(midiChannel), Data: [2]byte{byte(note), 0}, Len: 3})
	}
	midiHeld = map[int]bool{}
	for ch := range 16 {
		sendMIDI(midi.Message{Status: 0xB0 | byte(ch), Data: [2]byte{123, 0}, Len: 3})
	}
}

func sendMIDI(m midi.Message) {
	if selectedBoard == nil || selectedBoard.MIDI == nil {
		return
	}
	emu.SendMIDI(m)
	RequestNextFrame()
}
