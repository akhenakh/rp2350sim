package ble

import (
	"encoding/binary"
	"io"
)

// LINKTYPE_BLUETOOTH_HCI_H4 from the pcap link-layer registry.
const linkTypeHCIH4 = 187

// PCAPNG writes HCI H4 packets in a pcapng file that Wireshark dissects as
// Bluetooth HCI. Timestamps are virtual microseconds, so captures replay with
// the same timing regardless of host load.
type PCAPNG struct {
	w io.Writer
}

// NewPCAPNG writes the section and interface headers.
func NewPCAPNG(w io.Writer) (*PCAPNG, error) {
	p := &PCAPNG{w: w}
	if err := p.sectionHeader(); err != nil {
		return nil, err
	}
	if err := p.interfaceDescription(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PCAPNG) sectionHeader() error {
	// SHB body: byte-order magic, major, minor, section length (-1).
	body := make([]byte, 16)
	binary.LittleEndian.PutUint32(body[0:], 0x1A2B3C4D)
	binary.LittleEndian.PutUint16(body[4:], 1)
	binary.LittleEndian.PutUint16(body[6:], 0)
	binary.LittleEndian.PutUint64(body[8:], ^uint64(0))
	return p.block(0x0A0D0D0A, body)
}

func (p *PCAPNG) interfaceDescription() error {
	// IDB body: link type, reserved, snaplen, then if_tsresol=6 (microseconds).
	opts := make([]byte, 0, 12)
	opts = appendOption(opts, 9, []byte{6}) // if_tsresol
	opts = appendU32(opts, 0)               // opt_endofopt
	body := make([]byte, 8, 8+len(opts))
	binary.LittleEndian.PutUint16(body[0:], linkTypeHCIH4)
	binary.LittleEndian.PutUint32(body[4:], 0)
	body = append(body, opts...)
	return p.block(0x00000001, body)
}

// Packet appends one HCI H4 packet. data starts with the H4 packet type byte.
func (p *PCAPNG) Packet(timestampMicros uint64, data []byte) error {
	body := make([]byte, 20, 20+len(data)+4)
	binary.LittleEndian.PutUint32(body[0:], 0) // interface id
	tsHigh := uint32(timestampMicros >> 32)
	tsLow := uint32(timestampMicros)
	binary.LittleEndian.PutUint32(body[4:], tsHigh)
	binary.LittleEndian.PutUint32(body[8:], tsLow)
	binary.LittleEndian.PutUint32(body[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(body[16:], uint32(len(data)))
	body = append(body, data...)
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	return p.block(0x00000006, body)
}

func (p *PCAPNG) block(typ uint32, body []byte) error {
	total := uint32(12 + len(body))
	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:], typ)
	binary.LittleEndian.PutUint32(hdr[4:], total)
	if _, err := p.w.Write(hdr); err != nil {
		return err
	}
	if _, err := p.w.Write(body); err != nil {
		return err
	}
	var tail [4]byte
	binary.LittleEndian.PutUint32(tail[:], total)
	_, err := p.w.Write(tail[:])
	return err
}

func appendOption(dst []byte, code uint16, value []byte) []byte {
	dst = appendU16(dst, code)
	dst = appendU16(dst, uint16(len(value)))
	dst = append(dst, value...)
	for len(dst)%4 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

func appendU16(dst []byte, v uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return append(dst, b[:]...)
}

func appendU32(dst []byte, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return append(dst, b[:]...)
}
