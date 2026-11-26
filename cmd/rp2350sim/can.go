package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func parseCAN(raw string) (cs, intPin int, peer, reply uint32, err error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 1 || len(parts) > 3 {
		return 0, 0, 0, 0, fmt.Errorf("can: invalid spec %q", raw)
	}
	cs, err = strconv.Atoi(parts[0])
	if err != nil || cs < 0 || cs > 47 {
		return 0, 0, 0, 0, fmt.Errorf("can: invalid CS %q", parts[0])
	}
	intPin = -1
	if len(parts) > 1 && parts[1] != "" {
		intPin, err = strconv.Atoi(parts[1])
		if err != nil || intPin < 0 || intPin > 47 {
			return 0, 0, 0, 0, fmt.Errorf("can: invalid INT %q", parts[1])
		}
	}
	peer = 0x123
	if len(parts) > 2 && parts[2] != "" {
		v, e := strconv.ParseUint(strings.TrimPrefix(parts[2], "0x"), 16, 32)
		if e != nil {
			return 0, 0, 0, 0, fmt.Errorf("can: invalid peer id %q", parts[2])
		}
		peer = uint32(v)
	}
	return cs, intPin, peer, peer + 1, nil
}

// attachCAN builds a bus, a responding peer and one MCP2515 controller.
func attachCAN(c *rp2350.Chip, raw string) (*devices.MCP2515, int, error) {
	cs, intPin, peer, reply, err := parseCAN(raw)
	if err != nil {
		return nil, 0, err
	}
	bus := devices.NewCANBus(c)
	devices.NewCANPeer(bus, peer, reply, false)
	m := devices.NewMCP2515(c, bus, cs, intPin)
	c.Pins.AddChangeListener(func(pin int, level bool) {
		if pin == cs && !level {
			m.Select()
		}
	})
	return m, cs, nil
}
