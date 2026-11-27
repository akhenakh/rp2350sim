package cyw43439

import (
	"encoding/binary"
	"io"
	"sync"
)

// LINKTYPE_ETHERNET from the pcap link-layer registry.
const linkTypeEthernet = 1

// pcapWriter writes Ethernet frames in a pcapng file. Timestamps are virtual
// microseconds so captures replay with the emulator's timing. The EPB flags
// option marks the direction: 1 inbound (guest to host), 2 outbound.
type pcapWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newPCAPWriter(w io.Writer) (*pcapWriter, error) {
	p := &pcapWriter{w: w}
	if err := p.sectionHeader(); err != nil {
		return nil, err
	}
	if err := p.interfaceDescription(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *pcapWriter) sectionHeader() error {
	body := make([]byte, 16)
	binary.LittleEndian.PutUint32(body[0:], 0x1A2B3C4D)
	binary.LittleEndian.PutUint16(body[4:], 1)
	binary.LittleEndian.PutUint16(body[6:], 0)
	binary.LittleEndian.PutUint64(body[8:], ^uint64(0))
	return p.block(0x0A0D0D0A, body)
}

func (p *pcapWriter) interfaceDescription() error {
	opts := make([]byte, 0, 12)
	opts = appendOption(opts, 9, []byte{6}) // if_tsresol: microseconds
	opts = appendU32(opts, 0)               // opt_endofopt
	body := make([]byte, 8, 8+len(opts))
	binary.LittleEndian.PutUint16(body[0:], linkTypeEthernet)
	binary.LittleEndian.PutUint32(body[4:], 0)
	body = append(body, opts...)
	return p.block(0x00000001, body)
}

// packet appends one Ethernet frame. inbound selects the EPB direction flag.
func (p *pcapWriter) packet(timestampMicros uint64, inbound bool, data []byte) error {
	body := make([]byte, 20, 20+len(data)+16)
	binary.LittleEndian.PutUint32(body[0:], 0) // interface id
	binary.LittleEndian.PutUint32(body[4:], uint32(timestampMicros>>32))
	binary.LittleEndian.PutUint32(body[8:], uint32(timestampMicros))
	binary.LittleEndian.PutUint32(body[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(body[16:], uint32(len(data)))
	body = append(body, data...)
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	var flags uint32 = 2 // outbound
	if inbound {
		flags = 1 // inbound
	}
	var opt [12]byte
	binary.LittleEndian.PutUint16(opt[0:], 2) // epb_flags
	binary.LittleEndian.PutUint16(opt[2:], 4)
	binary.LittleEndian.PutUint32(opt[4:], flags)
	binary.LittleEndian.PutUint16(opt[8:], 0) // opt_endofopt
	body = append(body, opt[:]...)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.block(0x00000006, body)
}

func (p *pcapWriter) block(typ uint32, body []byte) error {
	total := uint32(12 + len(body))
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:], typ)
	binary.LittleEndian.PutUint32(hdr[4:], total)
	if _, err := p.w.Write(hdr[:]); err != nil {
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
