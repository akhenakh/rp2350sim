// Package midi implements the MIDI byte stream and USB-MIDI event packets
// used by the USB and UART bridges and the UI monitor.
package midi

import (
	"fmt"
	"strings"
)

// Message is one complete MIDI message. Len includes the status byte.
type Message struct {
	Status byte
	Data   [2]byte
	Len    int
}

// Bytes returns the wire bytes.
func (m Message) Bytes() []byte {
	switch m.Len {
	case 1:
		return []byte{m.Status}
	case 2:
		return []byte{m.Status, m.Data[0]}
	default:
		return []byte{m.Status, m.Data[0], m.Data[1]}
	}
}

// String formats a message for the monitor.
func (m Message) String() string {
	ch := int(m.Status&0x0F) + 1
	switch m.Status & 0xF0 {
	case 0x80:
		return fmt.Sprintf("ch%d note off %d vel %d", ch, m.Data[0], m.Data[1])
	case 0x90:
		if m.Data[1] == 0 {
			return fmt.Sprintf("ch%d note off %d", ch, m.Data[0])
		}
		return fmt.Sprintf("ch%d note on %d vel %d", ch, m.Data[0], m.Data[1])
	case 0xA0:
		return fmt.Sprintf("ch%d poly %d %d", ch, m.Data[0], m.Data[1])
	case 0xB0:
		return fmt.Sprintf("ch%d cc %d %d", ch, m.Data[0], m.Data[1])
	case 0xC0:
		return fmt.Sprintf("ch%d program %d", ch, m.Data[0])
	case 0xD0:
		return fmt.Sprintf("ch%d pressure %d", ch, m.Data[0])
	case 0xE0:
		return fmt.Sprintf("ch%d bend %d", ch, int(m.Data[0])|int(m.Data[1])<<7)
	}
	return fmt.Sprintf("status %02x % x", m.Status, m.Bytes()[1:])
}

// Parser turns a byte stream into complete messages, honouring running status.
type Parser struct {
	status byte
	data   [2]byte
	n      int
	sysex  bool
}

// Feed consumes one byte and returns any completed messages.
func (p *Parser) Feed(b byte) []Message {
	if p.sysex {
		if b == 0xF7 {
			p.sysex = false
		}
		return nil
	}
	if b >= 0xF8 { // real-time, may appear inside any message
		return []Message{{Status: b, Len: 1}}
	}
	if b >= 0x80 {
		if b == 0xF0 {
			p.sysex = true
			p.status = 0
			p.n = 0
			return nil
		}
		if b >= 0xF0 {
			p.status = b
			p.n = 0
			if expected(b) == 0 {
				p.status = 0
				return []Message{{Status: b, Len: 1}}
			}
			return nil
		}
		p.status = b
		p.n = 0
		return nil
	}
	if p.status < 0x80 {
		return nil // data without a status byte
	}
	want := expected(p.status)
	if p.n < len(p.data) {
		p.data[p.n] = b
	}
	p.n++
	if p.n < want {
		return nil
	}
	m := Message{Status: p.status, Data: p.data, Len: want + 1}
	p.n = 0
	if p.status >= 0xF0 {
		p.status = 0 // system common cancels running status
	}
	return []Message{m}
}

// Reset clears running status and partial messages.
func (p *Parser) Reset() {
	p.status = 0
	p.n = 0
	p.sysex = false
}

func expected(status byte) int {
	switch status & 0xF0 {
	case 0xC0, 0xD0:
		return 1
	case 0x80, 0x90, 0xA0, 0xB0, 0xE0:
		return 2
	case 0xF0:
		switch status {
		case 0xF1, 0xF3:
			return 1
		case 0xF2:
			return 2
		case 0xF6, 0xF7, 0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF:
			return 0
		}
	}
	return 0
}

// Code Index Number for a channel message status.
func cin(status byte) byte {
	switch status & 0xF0 {
	case 0x80, 0x90, 0xA0, 0xB0, 0xC0, 0xD0, 0xE0:
		return status >> 4
	}
	switch status {
	case 0xF1, 0xF3:
		return 0x2
	case 0xF2:
		return 0x3
	case 0xF6:
		// Single-byte System Common (also 0xF1/0xF3, handled above).
		return 0x5
	case 0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF:
		return 0xF
	}
	return 0x4 // SysEx start/continue
}

// USBMessage encodes a message as one USB-MIDI event packet.
func USBMessage(m Message) [4]byte {
	b := m.Bytes()
	var p [4]byte
	p[0] = cin(m.Status)
	copy(p[1:], b)
	return p
}

// DecodeUSB splits a USB-MIDI bulk payload into messages.
func DecodeUSB(data []byte) []Message {
	var out []Message
	for len(data) >= 4 {
		cin := data[0] & 0x0F
		status := data[1]
		switch cin {
		case 0x8, 0x9, 0xA, 0xB, 0xE:
			out = append(out, Message{Status: status, Data: [2]byte{data[2], data[3]}, Len: 3})
		case 0xC, 0xD:
			out = append(out, Message{Status: status, Data: [2]byte{data[2]}, Len: 2})
		case 0xF:
			out = append(out, Message{Status: status, Len: 1})
		case 0x2:
			out = append(out, Message{Status: status, Data: [2]byte{data[2]}, Len: 2})
		case 0x3:
			out = append(out, Message{Status: status, Data: [2]byte{data[2], data[3]}, Len: 3})
		case 0x5:
			out = append(out, Message{Status: status, Len: 1})
		}
		data = data[4:]
	}
	return out
}

// NoteName returns the conventional name for a MIDI note number.
func NoteName(n int) string {
	names := []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	return fmt.Sprintf("%s%d", names[n%12], n/12-1)
}

// ParseText is a small helper for the CLI monitor to show raw bytes.
func ParseText(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02x", v)
	}
	return strings.Join(parts, " ")
}
