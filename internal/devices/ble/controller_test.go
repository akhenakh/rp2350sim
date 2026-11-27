package ble

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func testController(t *testing.T) *Controller {
	t.Helper()
	c := rp2350.New()
	return New(c, Options{
		HostWake: 24,
		Address:  [6]byte{0xC0, 0xDE, 0x12, 0x34, 0x56, 0x78},
		Seed:     42,
	})
}

func command(t *testing.T, ctrl *Controller, opcode uint16, params ...byte) []byte {
	t.Helper()
	data := []byte{byte(opcode), byte(opcode >> 8), byte(len(params))}
	data = append(data, params...)
	ctrl.writeHCI(packetCommand, data)
	pkt := ctrl.popEvent()
	if pkt == nil {
		t.Fatalf("opcode %#04x produced no event", opcode)
	}
	return pkt
}

func leSubevent(t *testing.T, pkt []byte, want byte) []byte {
	t.Helper()
	if len(pkt) < 4 || pkt[0] != packetEvent || pkt[1] != evtLEMeta {
		t.Fatalf("not an LE meta event: % x", pkt)
	}
	if pkt[3] != want {
		t.Fatalf("subevent = %#x, want %#x (% x)", pkt[3], want, pkt)
	}
	return pkt[4:]
}

func TestControllerInitCommands(t *testing.T) {
	ctrl := testController(t)

	pkt := command(t, ctrl, opReset)
	want := []byte{packetEvent, evtCommandComplete, 4, 1, 0x03, 0x0C, 0}
	if !bytes.Equal(pkt, want) {
		t.Fatalf("reset = % x", pkt)
	}

	pkt = command(t, ctrl, opReadLocalSupportedFeatures)
	if len(pkt) < 12 || pkt[6] != statusSuccess || pkt[7+4]&0x60 != 0x60 {
		t.Fatalf("features = % x", pkt)
	}

	pkt = command(t, ctrl, opReadBDAddr)
	if len(pkt) != 13 {
		t.Fatalf("bd addr length = %d: % x", len(pkt), pkt)
	}
	if got := pkt[7:]; !bytes.Equal(got, []byte{0x22, 0x56, 0x34, 0x12, 0xDE, 0xC0}) {
		t.Fatalf("bd addr = % x", got)
	}

	// Unknown commands are rejected, never silently completed.
	pkt = command(t, ctrl, 0x1234)
	if pkt[6] != statusUnknownCommand {
		t.Fatalf("unknown command status = %#x: % x", pkt[6], pkt)
	}
}

func TestControllerScanAndConnect(t *testing.T) {
	ctrl := testController(t)
	ctrl.setScan(true)
	data := leSubevent(t, ctrl.popEvent(), leAdvertisingReport)
	if data[0] != 1 {
		t.Fatalf("reports = %d", data[0])
	}
	// Address is little-endian in the HCI event.
	if got := data[3:9]; !bytes.Equal(got, []byte{0x78, 0x56, 0x34, 0x12, 0xDE, 0xC0}) {
		t.Fatalf("advertiser address = % x", got)
	}

	params := make([]byte, 25)
	params[5] = 0x00
	copy(params[6:12], []byte{0x78, 0x56, 0x34, 0x12, 0xDE, 0xC0})
	ctrl.createConnection(params)
	status := ctrl.popEvent()
	if status[1] != evtCommandStatus || status[3] != statusSuccess {
		t.Fatalf("create status = % x", status)
	}
	ctrl.connectComplete()
	conn := leSubevent(t, ctrl.popEvent(), leConnectionComplete)
	if conn[0] != statusSuccess || binary.LittleEndian.Uint16(conn[1:]) != connHandle {
		t.Fatalf("connection complete = % x", conn)
	}
}

func TestControllerATTExchange(t *testing.T) {
	ctrl := testController(t)
	ctrl.conn = &connection{handle: connHandle, mtu: defaultMTU}

	att := []byte{attExchangeMTUReq, 0x9B, 0x06}
	frame := l2cap(attCID, att)
	acl := make([]byte, 4+len(frame))
	binary.LittleEndian.PutUint16(acl, connHandle)
	binary.LittleEndian.PutUint16(acl[2:], uint16(len(frame)))
	copy(acl[4:], frame)
	ctrl.writeHCI(packetACL, acl)

	completed := ctrl.popEvent()
	if completed[1] != evtNumberOfCompletedPacket {
		t.Fatalf("expected completed-packets event, got % x", completed)
	}
	resp := ctrl.popEvent()
	if len(resp) < 5 || resp[0] != packetACL {
		t.Fatalf("expected ACL response, got % x", resp)
	}
	payload := resp[5:]
	if len(payload) != 7 || payload[4] != attExchangeMTURsp {
		t.Fatalf("ATT response = % x", payload)
	}
}
