package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// busBridgeSpec is one -i2c-tcp / -spi-tcp flag.
type busBridgeSpec struct {
	kind    devices.BusKind
	bus     int
	addrs   []uint8 // I2C only
	cs      int     // SPI only
	listen  string
	connect string
}

// parseBusBridgeSpecs parses repeatable bridge flags:
//
//	i2c: bus:addr[,addr...][@listen|=connect]
//	spi: bus:cs[@listen|=connect]
//
// The endpoint defaults to 127.0.0.1:0 in listen mode.
func parseBusBridgeSpecs(kind devices.BusKind, specs []string) ([]busBridgeSpec, error) {
	var out []busBridgeSpec
	for _, raw := range specs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		sep := byte(0)
		before, endpoint := raw, ""
		if i := strings.IndexAny(raw, "@="); i >= 0 {
			sep = raw[i]
			before, endpoint = raw[:i], raw[i+1:]
		}
		busPart, targetPart, ok := strings.Cut(before, ":")
		if !ok {
			return nil, fmt.Errorf("%s bridge %q: want bus:targets[@addr|=addr]", kind, raw)
		}
		bus, err := strconv.Atoi(strings.TrimSpace(busPart))
		if err != nil || bus < 0 || bus > 1 {
			return nil, fmt.Errorf("%s bridge %q: bus must be 0 or 1", kind, raw)
		}
		spec := busBridgeSpec{kind: kind, bus: bus}
		if sep == '=' {
			if endpoint == "" {
				return nil, fmt.Errorf("%s bridge %q: connect needs host:port", kind, raw)
			}
			spec.connect = endpoint
		} else {
			spec.listen = endpoint
		}
		if err := spec.mergeEndpoint(); err != nil {
			return nil, err
		}
		switch kind {
		case devices.BusI2C:
			for part := range strings.SplitSeq(targetPart, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				n, err := strconv.ParseUint(part, 0, 8)
				if err != nil || n < 0x08 || n > 0x77 {
					return nil, fmt.Errorf("i2c bridge %q: address %q is not 0x08..0x77", raw, part)
				}
				spec.addrs = append(spec.addrs, uint8(n))
			}
			if len(spec.addrs) == 0 {
				return nil, fmt.Errorf("i2c bridge %q: no addresses", raw)
			}
		case devices.BusSPI:
			if strings.Contains(targetPart, ",") {
				return nil, fmt.Errorf("spi bridge %q: one CS pin per bridge", raw)
			}
			n, err := strconv.Atoi(strings.TrimSpace(targetPart))
			if err != nil || n < 0 || n > 47 {
				return nil, fmt.Errorf("spi bridge %q: CS must be a GPIO 0..47", raw)
			}
			spec.cs = n
		default:
			return nil, fmt.Errorf("unknown bus kind %q", kind)
		}
		out = append(out, spec)
	}
	return out, nil
}

func (s *busBridgeSpec) mergeEndpoint() error {
	if s.listen != "" && s.connect != "" {
		return fmt.Errorf("%s bridge bus %d: listen and connect are mutually exclusive", s.kind, s.bus)
	}
	if s.connect != "" {
		return nil
	}
	if s.listen == "" {
		s.listen = "127.0.0.1:0"
	}
	return nil
}

// attachBusBridges starts the TCP bridges and attaches them to the chip.
// The caller supplies addSPI so the remote devices join the same per-bus
// client list as LCD/SD/Ethernet before the SPI handler is built.
func attachBusBridges(
	c *rp2350.Chip,
	log *slog.Logger,
	timeout, wait time.Duration,
	i2cSpecs, spiSpecs []busBridgeSpec,
	addSPI func(bus, cs int, fn func(byte) byte),
) (bus []*devices.BusBridge, spi []*devices.SPIBridge, err error) {
	closeAll := func() {
		for _, br := range bus {
			br.Close()
		}
	}
	for _, spec := range i2cSpecs {
		for _, addr := range spec.addrs {
			if c.HasI2CDevice(spec.bus, addr) {
				closeAll()
				return nil, nil, fmt.Errorf("i2c bridge: bus %d address 0x%02x already has a device", spec.bus, addr)
			}
		}
		br := devices.NewI2CBridge(spec.bus, spec.addrs, spec.listen, spec.connect)
		br.Logger = log
		br.Timeout = timeout
		br.Wait = wait
		if err := br.Start(); err != nil {
			closeAll()
			return nil, nil, err
		}
		for _, addr := range spec.addrs {
			c.AttachI2C(spec.bus, addr, br)
		}
		bus = append(bus, br.BusBridge)
	}
	for _, spec := range spiSpecs {
		br := devices.NewSPIBridge(spec.bus, spec.cs, spec.listen, spec.connect)
		br.Logger = log
		br.Timeout = timeout
		br.Wait = wait
		if err := br.Start(); err != nil {
			closeAll()
			return nil, nil, err
		}
		addSPI(spec.bus, spec.cs, br.Transfer)
		bus = append(bus, br.BusBridge)
		spi = append(spi, br)
	}
	return bus, spi, nil
}
