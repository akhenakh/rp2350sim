package ble

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestCommandCompleteEncoding(t *testing.T) {
	pkt := commandComplete(opReset, []byte{statusSuccess})
	want := []byte{packetEvent, evtCommandComplete, 4, 1, 0x03, 0x0C, 0}
	if !bytes.Equal(pkt, want) {
		t.Fatalf("command complete = % x, want % x", pkt, want)
	}
}

func TestCommandStatusEncoding(t *testing.T) {
	pkt := commandStatus(opLECreateConnection, statusSuccess)
	want := []byte{packetEvent, evtCommandStatus, 4, 0, 1, 0x0D, 0x20}
	if !bytes.Equal(pkt, want) {
		t.Fatalf("command status = % x, want % x", pkt, want)
	}
}

func TestACLPacketHeader(t *testing.T) {
	pkt := aclPacket(0x0040, []byte{0xAA, 0xBB})
	want := []byte{packetACL, 0x40, 0x20, 2, 0, 0xAA, 0xBB}
	if !bytes.Equal(pkt, want) {
		t.Fatalf("acl = % x, want % x", pkt, want)
	}
}

func TestL2CAPHeader(t *testing.T) {
	frame := l2cap(attCID, []byte{0x02, 0x9B, 0x06})
	want := []byte{3, 0, 4, 0, 0x02, 0x9B, 0x06}
	if !bytes.Equal(frame, want) {
		t.Fatalf("l2cap = % x, want % x", frame, want)
	}
}

func TestLESupportedFeatures(t *testing.T) {
	f := localFeatures()
	if f[4]&(1<<5) == 0 {
		t.Fatal("BR/EDR not supported bit missing")
	}
	if f[4]&(1<<6) == 0 {
		t.Fatal("LE supported bit missing")
	}
}

func TestSupportedCommandsOnlyAdvertisesInitBits(t *testing.T) {
	bm := supportedCommands()
	if len(bm) != 64 {
		t.Fatalf("bitmap length = %d", len(bm))
	}
	if bm[14]&(1<<7) == 0 {
		t.Fatal("Read Buffer Size bit missing")
	}
	if bm[24]&(1<<6) == 0 {
		t.Fatal("Write LE Host Supported bit missing")
	}
}

func TestPeerServiceDiscovery(t *testing.T) {
	p := newPeer()
	req := []byte{attFindByTypeValueReq, 0x01, 0x00, 0xFF, 0xFF, 0x00, 0x28, 0x1A, 0x18}
	rsp, _ := p.handleATT(req)
	want := []byte{attFindByTypeValueRsp, 0x10, 0x00, 0x14, 0x00}
	if !bytes.Equal(rsp, want) {
		t.Fatalf("find response = % x, want % x", rsp, want)
	}
}

func TestPeerCharacteristicAndNotification(t *testing.T) {
	p := newPeer()
	req := []byte{attReadByTypeReq, 0x10, 0x00, 0x14, 0x00, 0x03, 0x28}
	rsp, _ := p.handleATT(req)
	if len(rsp) != 9 || rsp[0] != attReadByTypeRsp || rsp[1] != 7 {
		t.Fatalf("read by type response = % x", rsp)
	}
	if got := binary.LittleEndian.Uint16(rsp[2:]); got != charDeclHandle {
		t.Fatalf("declaration handle = %#x", got)
	}

	// Enable notifications on the CCCD.
	rsp, notify := p.handleATT([]byte{attWriteReq, 0x13, 0x00, 0x01, 0x00})
	if !notify || !bytes.Equal(rsp, []byte{attWriteRsp}) {
		t.Fatalf("cccd write = % x notify=%v", rsp, notify)
	}
	n := p.notification()
	want := []byte{attHandleValueNotify, 0x12, 0x00, 0x29, 0x09}
	if !bytes.Equal(n, want) {
		t.Fatalf("notification = % x, want % x", n, want)
	}
}
