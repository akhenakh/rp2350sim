//go:build !js

package cyw43439

import (
	"encoding/binary"
	"net"
)

// DHCP server for the virtual AP. The guest gets GuestIP/24 with the gateway
// as router and DNSIP as resolver; the lease never expires from the emulator's
// point of view.

const dhcpMinLen = 240

func (n *network) handleDHCP(srcIP [4]byte, srcPort uint16, dstIP [4]byte, dstPort uint16, p []byte) {
	if len(p) < dhcpMinLen || p[0] != 1 { // BOOTREQUEST
		return
	}
	var chaddr [6]byte
	copy(chaddr[:], p[28:34])

	msgType := byte(0)
	var requested [4]byte
	for i := dhcpMinLen; i < len(p); {
		code := p[i]
		if code == 255 {
			break
		}
		if code == 0 {
			i++
			continue
		}
		if i+1 >= len(p) {
			break
		}
		l := int(p[i+1])
		if i+2+l > len(p) {
			break
		}
		val := p[i+2 : i+2+l]
		switch code {
		case 53:
			if l >= 1 {
				msgType = val[0]
			}
		case 50:
			if l >= 4 {
				copy(requested[:], val[:4])
			}
		}
		i += 2 + l
	}
	var replyType byte
	switch msgType {
	case 1: // DISCOVER
		replyType = 2 // OFFER
	case 3: // REQUEST
		replyType = 5 // ACK
	case 4, 7: // DECLINE, RELEASE
		return
	default:
		return
	}

	reply := make([]byte, dhcpMinLen+64)
	reply[0] = 2 // BOOTREPLY
	reply[1] = 1
	reply[2] = 6
	copy(reply[4:8], p[4:8]) // xid
	binary.BigEndian.PutUint16(reply[10:], 0x8000)
	copy(reply[16:20], n.cfg.GuestIP[:])
	copy(reply[20:24], n.cfg.GatewayIP[:])
	copy(reply[28:34], chaddr[:])
	reply[236] = 0x63
	reply[237] = 0x82
	reply[238] = 0x53
	reply[239] = 0x63
	off := dhcpMinLen
	put := func(code byte, val ...byte) {
		reply[off] = code
		reply[off+1] = byte(len(val))
		copy(reply[off+2:], val)
		off += 2 + len(val)
	}
	put(53, replyType)
	put(54, n.cfg.GatewayIP[:]...)
	put(51, 0, 0x01, 0x51, 0x80) // 86400 s
	put(1, maskBytes(n.cfg.Prefix)...)
	put(3, n.cfg.GatewayIP[:]...)
	put(6, n.cfg.DNSIP[:]...)
	put(15, []byte("lan")...)
	reply[off] = 255

	out := n.udpFrame([6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		n.cfg.GatewayIP, [4]byte{255, 255, 255, 255}, 67, 68, reply)
	n.d.queueDataFrame(out)
	kind := "offer"
	if replyType == 5 {
		kind = "ack"
	}
	n.netLog.Info("dhcp "+kind,
		"ip", net.IP(n.cfg.GuestIP[:]).String(),
		"mac", net.HardwareAddr(chaddr[:]).String())
}

func maskBytes(prefix int) []byte {
	var m [4]byte
	for i := range 4 {
		if prefix >= (i+1)*8 {
			m[i] = 0xff
		} else if prefix > i*8 {
			m[i] = byte(0xff << uint(8-(prefix-i*8)))
		}
	}
	return m[:]
}
