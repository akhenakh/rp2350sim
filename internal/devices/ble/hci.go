// Package ble models a Bluetooth LE controller behind the CYW43 HCI transport.
//
// The firmware runs the real btstack host stack. The emulator replaces the
// CYW43 bus functions (cyw43_ll_bus_init, cyw43_btbus_read/write and the
// has-work checks) with hooks, so HCI commands and ACL data arrive here and
// events and ACL packets are handed back through the same transport. Nothing
// below HCI is shared with the real radio: advertising, scanning, connections
// and the scripted ATT peer are logical and deterministic.
package ble

import "encoding/binary"

// HCI packet types as they appear in byte 3 of the CYW43 transport buffer.
const (
	packetCommand byte = 0x01
	packetACL     byte = 0x02
	packetEvent   byte = 0x04
)

// HCI event codes.
const (
	evtDisconnectionComplete   byte = 0x05
	evtCommandComplete         byte = 0x0E
	evtCommandStatus           byte = 0x0F
	evtNumberOfCompletedPacket byte = 0x13
	evtLEMeta                  byte = 0x3E
)

// LE meta subevents.
const (
	leConnectionComplete       byte = 0x01
	leAdvertisingReport        byte = 0x02
	leConnectionUpdateComplete byte = 0x03
	leReadRemoteFeatures       byte = 0x04
)

// HCI opcodes used by the controller and the btstack init sequence.
const (
	opReset                      uint16 = 0x0C03
	opSetEventMask               uint16 = 0x0C01
	opHostBufferSize             uint16 = 0x0C33
	opSetControllerToHostFlow    uint16 = 0x0C52
	opWriteLEHostSupported       uint16 = 0x0C6D
	opDisconnect                 uint16 = 0x0406
	opReadLocalVersion           uint16 = 0x1001
	opReadLocalSupportedCommands uint16 = 0x1002
	opReadLocalSupportedFeatures uint16 = 0x1003
	opReadBufferSize             uint16 = 0x1005
	opReadBDAddr                 uint16 = 0x1009
	opLESetEventMask             uint16 = 0x2001
	opLEReadBufferSize           uint16 = 0x2002
	opLEReadLocalFeatures        uint16 = 0x2003
	opLESetRandomAddress         uint16 = 0x2005
	opLESetAdvParams             uint16 = 0x2006
	opLESetAdvData               uint16 = 0x2008
	opLESetAdvEnable             uint16 = 0x200A
	opLESetScanParams            uint16 = 0x200B
	opLESetScanEnable            uint16 = 0x200C
	opLECreateConnection         uint16 = 0x200D
	opLEReadWhiteListSize        uint16 = 0x200F
	opLEConnectionUpdate         uint16 = 0x2013
	opLEReadRemoteFeatures       uint16 = 0x2016
	opVendorSetBDAddr            uint16 = 0xFC06
)

// Status codes.
const (
	statusSuccess            byte = 0x00
	statusUnknownCommand     byte = 0x01
	statusCommandDisallowed  byte = 0x0C
	statusUnsupportedFeature byte = 0x11
)

// commandComplete builds an HCI Command Complete event. params are the return
// parameters after the status byte; the status is prepended.
func commandComplete(opcode uint16, params []byte) []byte {
	data := make([]byte, 0, 4+len(params))
	data = append(data, 1) // num_hci_command_packets
	data = append(data, byte(opcode), byte(opcode>>8))
	data = append(data, params...)
	return event(evtCommandComplete, data)
}

// commandStatus builds an HCI Command Status event.
func commandStatus(opcode uint16, status byte) []byte {
	return event(evtCommandStatus, []byte{status, 1, byte(opcode), byte(opcode >> 8)})
}

// event wraps an HCI event code and payload, including the CYW43 type byte.
func event(code byte, payload []byte) []byte {
	pkt := make([]byte, 3+len(payload))
	pkt[0] = packetEvent
	pkt[1] = code
	pkt[2] = byte(len(payload))
	copy(pkt[3:], payload)
	return pkt
}

// leMeta builds an LE Meta event.
func leMeta(subevent byte, payload []byte) []byte {
	data := make([]byte, 1+len(payload))
	data[0] = subevent
	copy(data[1:], payload)
	return event(evtLEMeta, data)
}

// aclPacket builds an HCI ACL data packet for the CYW43 transport.
func aclPacket(handle uint16, data []byte) []byte {
	pkt := make([]byte, 5+len(data))
	pkt[0] = packetACL
	binary.LittleEndian.PutUint16(pkt[1:], handle|0x2000) // PB=10, first fragment
	binary.LittleEndian.PutUint16(pkt[3:], uint16(len(data)))
	copy(pkt[5:], data)
	return pkt
}

// l2cap builds an L2CAP frame with a 16-bit length and channel id.
func l2cap(cid uint16, payload []byte) []byte {
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint16(frame[0:], uint16(len(payload)))
	binary.LittleEndian.PutUint16(frame[2:], cid)
	copy(frame[4:], payload)
	return frame
}
