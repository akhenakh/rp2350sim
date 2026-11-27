//go:build !js

package cyw43439

import (
	"encoding/binary"
	"time"
)

// NTP responder: a synthetic stratum-1 server whose time is the host clock.

func (n *network) handleNTP(srcIP [4]byte, srcPort uint16, dstIP [4]byte, dstPort uint16, p []byte) {
	if len(p) < 48 {
		return
	}
	now := time.Now()
	out := make([]byte, 48)
	out[0] = 0x24 // LI 0, version 4, mode server
	out[1] = 1    // stratum
	out[2] = 4    // poll
	out[3] = 0xec // precision -20
	copy(out[12:16], "LOCL")
	putNTP(out[16:24], now.Add(-time.Second))
	copy(out[24:32], p[40:48]) // originate = client transmit
	putNTP(out[32:40], now.Add(-100*time.Microsecond))
	putNTP(out[40:48], now)
	frame := n.udpFrame(n.d.cfg.MAC, dstIP, srcIP, 123, srcPort, out)
	n.d.queueDataFrame(frame)
}

func putNTP(dst []byte, t time.Time) {
	const ntpEpoch = 2208988800 // seconds from 1900 to 1970
	sec := uint32(t.Unix() + ntpEpoch)
	frac := uint32(uint64(t.Nanosecond()) * (1 << 32) / 1e9)
	binary.BigEndian.PutUint32(dst, sec)
	binary.BigEndian.PutUint32(dst[4:], frac)
}
