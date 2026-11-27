//go:build !js

package cyw43439

import (
	"encoding/binary"
	"log/slog"
	"strings"
)

// DNS resolver for the virtual AP. Board hosts win; otherwise the host
// resolver is consulted in the background so the emulator never blocks.

func (n *network) handleDNS(srcIP [4]byte, srcPort uint16, dstIP [4]byte, dstPort uint16, p []byte) {
	if len(p) < 12 {
		return
	}
	id := binary.BigEndian.Uint16(p)
	flags := binary.BigEndian.Uint16(p[2:])
	if flags&0x8000 != 0 || binary.BigEndian.Uint16(p[4:]) != 1 {
		return
	}
	name, off, ok := dnsName(p, 12)
	if !ok || off+4 > len(p) {
		return
	}
	qtype := binary.BigEndian.Uint16(p[off:])
	qclass := binary.BigEndian.Uint16(p[off+2:])
	question := append([]byte(nil), p[12:off+4]...)
	server := dstIP
	if n.d.debug {
		n.log.Debug("cyw43439 dns", slog.String("name", name), slog.Int("type", int(qtype)))
	}
	go func() {
		ips := n.resolveHost(name)
		rcode := uint16(0)
		if len(ips) == 0 {
			rcode = 3 // NXDOMAIN
			n.netLog.Info("dns", "name", name, "result", "nxdomain")
		} else {
			n.netLog.Info("dns", "name", name, "ip", ips[0].String())
		}
		var answer []byte
		for _, ip := range ips {
			switch qtype {
			case 1:
				if v4 := ip.To4(); v4 != nil {
					answer = append([]byte(nil), v4...)
				}
			case 28:
				if ip.To4() == nil && ip.To16() != nil {
					answer = append([]byte(nil), ip.To16()...)
				}
			}
			if answer != nil {
				break
			}
		}
		resp := dnsResponse(id, qclass, qtype, question, answer, rcode)
		frame := n.udpFrame(n.d.cfg.MAC, server, srcIP, 53, srcPort, resp)
		n.d.queueDataFrame(frame)
	}()
}

func dnsName(msg []byte, off int) (string, int, bool) {
	var b strings.Builder
	for {
		if off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		off++
		if l == 0 {
			return b.String(), off, true
		}
		if l&0xc0 != 0 || off+l > len(msg) {
			return "", 0, false
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.Write(msg[off : off+l])
		off += l
		if b.Len() > 255 {
			return "", 0, false
		}
	}
}

func dnsResponse(id, qclass, qtype uint16, question, answer []byte, rcode uint16) []byte {
	an := uint16(0)
	if answer != nil {
		an = 1
	}
	out := make([]byte, 12, 12+len(question)+16)
	binary.BigEndian.PutUint16(out[0:], id)
	binary.BigEndian.PutUint16(out[2:], 0x8180|rcode) // QR, RD, RA
	binary.BigEndian.PutUint16(out[4:], 1)            // QDCOUNT
	binary.BigEndian.PutUint16(out[6:], an)
	out = append(out, question...)
	if answer != nil {
		out = append(out,
			0xc0, 0x0c, // name pointer to offset 12
			byte(qtype>>8), byte(qtype),
			byte(qclass>>8), byte(qclass),
			0, 0, 0, 60, // TTL
			byte(len(answer)>>8), byte(len(answer)),
		)
		out = append(out, answer...)
	}
	return out
}
