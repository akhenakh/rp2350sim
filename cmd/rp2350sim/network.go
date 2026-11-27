package main

import (
	"log/slog"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/devices/cyw43439"
)

// networkReporter logs the guest identity and host endpoints whenever the
// attached controllers' network state changes. Without it a user running a
// server firmware has no way to learn which host port to connect to.
type networkReporter struct {
	log  *slog.Logger
	last string
}

func (r *networkReporter) infos(w5500 *devices.W5500, w6300 *devices.W6300, ch *devices.CH9120, cyw *cyw43439.Device) []devices.NetworkInfo {
	var out []devices.NetworkInfo
	if w5500 != nil {
		out = append(out, w5500.NetworkInfo())
	}
	if w6300 != nil {
		out = append(out, w6300.NetworkInfo())
	}
	if ch != nil {
		if info := ch.NetworkInfo(); info.Configured {
			out = append(out, info)
		}
	}
	if cyw != nil {
		out = append(out, cyw.NetworkInfo())
	}
	return out
}

// report emits one line per change. The summary is stable so repeated polls
// at chunk boundaries stay quiet.
func (r *networkReporter) report(w5500 *devices.W5500, w6300 *devices.W6300, ch *devices.CH9120, cyw *cyw43439.Device) {
	infos := r.infos(w5500, w6300, ch, cyw)
	if len(infos) == 0 {
		return
	}
	parts := make([]string, 0, len(infos))
	for _, info := range infos {
		parts = append(parts, info.Summary())
	}
	line := strings.Join(parts, " | ")
	if line == r.last {
		return
	}
	r.last = line
	r.log.Info("network", slog.String("status", line))
}
