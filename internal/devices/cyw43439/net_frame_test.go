//go:build !js

package cyw43439

import (
	"encoding/binary"
	"testing"
)

// A neighbour probe for the guest's own address is duplicate-address
// detection, not a request for the AP; answering it would make the guest
// reject the DHCP lease.
func TestARPDoesNotClaimGuestAddress(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{})
	d.hostNet = &stubDataPath{}
	d.linkUp = true
	n := &network{
		d: d,
		cfg: &NetConfig{
			GuestIP:   [4]byte{10, 0, 2, 15},
			GatewayIP: [4]byte{10, 0, 2, 2},
			APMAC:     [6]byte{0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0xee},
		},
	}
	src := [6]byte{0x02, 0, 0, 0, 0, 1}

	n.handleARP(arpRequest([4]byte{10, 0, 2, 15}), src)
	if len(d.queue) != 0 {
		t.Fatalf("AP answered an ACD probe for the guest address (%d replies)", len(d.queue))
	}

	n.handleARP(arpRequest([4]byte{10, 0, 2, 2}), src)
	if len(d.queue) != 1 {
		t.Fatalf("AP did not answer a probe for the gateway (%d replies)", len(d.queue))
	}
}

func arpRequest(tpa [4]byte) []byte {
	frame := make([]byte, 14+28)
	copy(frame[0:6], []byte{0x02, 0, 0, 0, 0, 1})
	copy(frame[6:12], []byte{0x02, 0, 0, 0, 0, 2})
	binary.BigEndian.PutUint16(frame[12:], 0x0806)
	p := frame[14:]
	binary.BigEndian.PutUint16(p[0:], 1)      // htype
	binary.BigEndian.PutUint16(p[2:], 0x0800) // ptype
	p[4] = 6
	p[5] = 4
	binary.BigEndian.PutUint16(p[6:], 1) // request
	copy(p[8:14], []byte{0x02, 0, 0, 0, 0, 1})
	copy(p[18:24], []byte{0, 0, 0, 0, 0, 0})
	copy(p[24:28], tpa[:])
	return frame
}

// Data-channel packets carry a 4-byte BDC header, so a short one must not be
// rejected by the control-channel header requirement.
func TestWlanWriteShortDataFrame(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{})
	dp := &stubDataPath{}
	d.hostNet = dp
	d.linkUp = true

	packet := make([]byte, sdpcmHeaderLen+4+4)
	binary.LittleEndian.PutUint16(packet, uint16(len(packet)))
	packet[5] = 2 // data channel
	packet[7] = sdpcmHeaderLen
	packet[sdpcmHeaderLen+3] = 0 // one BDC header, no offset
	d.wlanWrite(packet)

	if len(dp.frames) != 1 {
		t.Fatalf("short data frame delivered %d times, want 1", len(dp.frames))
	}
	if got := len(dp.frames[0]); got != 4 {
		t.Fatalf("payload = %d bytes, want 4", got)
	}
}
