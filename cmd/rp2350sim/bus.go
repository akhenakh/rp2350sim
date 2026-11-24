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

// printBusPresets lists the built-in device models for -list-presets.
func printBusPresets() {
	for _, p := range devices.BusPresetList() {
		if p.Kind == devices.BusI2C {
			addrs := make([]string, len(p.Addresses))
			for i, a := range p.Addresses {
				addrs[i] = fmt.Sprintf("0x%02x", a)
			}
			fmt.Printf("%-10s i2c  %-12s %s\n", p.Name, strings.Join(addrs, ","), p.Description)
		} else {
			fmt.Printf("%-10s spi  %-12s %s\n", p.Name, "any CS", p.Description)
		}
	}
}

// busPresetSpec is one -i2c-preset / -spi-preset flag.
type busPresetSpec struct {
	kind devices.BusKind
	bus  int
	name string
	addr int // I2C only; 0 selects the preset default
	cs   int // SPI only
}

// parseBusPresetSpecs parses repeatable preset flags:
//
//	i2c: bus:preset[@addr]
//	spi: bus:preset@cs
func parseBusPresetSpecs(kind devices.BusKind, specs []string) ([]busPresetSpec, error) {
	var out []busPresetSpec
	for _, raw := range specs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		busPart, rest, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("%s preset %q: want bus:name", kind, raw)
		}
		bus, err := strconv.Atoi(strings.TrimSpace(busPart))
		if err != nil || bus < 0 || bus > 1 {
			return nil, fmt.Errorf("%s preset %q: bus must be 0 or 1", kind, raw)
		}
		spec := busPresetSpec{kind: kind, bus: bus}
		switch kind {
		case devices.BusI2C:
			name, addrPart, hasAddr := strings.Cut(rest, "@")
			name = strings.TrimSpace(name)
			info, ok := devices.FindBusPreset(name)
			if !ok {
				return nil, fmt.Errorf("i2c preset %q: unknown preset %q", raw, name)
			}
			if info.Kind != devices.BusI2C {
				return nil, fmt.Errorf("i2c preset %q: %q is not an I2C device", raw, name)
			}
			spec.name = info.Name
			if hasAddr {
				n, err := strconv.ParseUint(strings.TrimSpace(addrPart), 0, 8)
				if err != nil || n < 0x08 || n > 0x77 {
					return nil, fmt.Errorf("i2c preset %q: address %q is not 7-bit", raw, addrPart)
				}
				spec.addr = int(n)
			}
			if spec.addr == 0 {
				spec.addr = int(info.Addresses[0])
			}
			supported := false
			for _, a := range info.Addresses {
				if spec.addr == int(a) {
					supported = true
					break
				}
			}
			if !supported {
				return nil, fmt.Errorf("i2c preset %q: %s does not implement address %#x", raw, spec.name, spec.addr)
			}
		case devices.BusSPI:
			name, csPart, ok := strings.Cut(rest, "@")
			name = strings.TrimSpace(name)
			if !ok {
				return nil, fmt.Errorf("spi preset %q: want bus:name@cs", raw)
			}
			info, ok := devices.FindBusPreset(name)
			if !ok {
				return nil, fmt.Errorf("spi preset %q: unknown preset %q", raw, name)
			}
			if info.Kind != devices.BusSPI {
				return nil, fmt.Errorf("spi preset %q: %q is not an SPI device", raw, name)
			}
			cs, err := strconv.Atoi(strings.TrimSpace(csPart))
			if err != nil || cs < 0 || cs > 47 {
				return nil, fmt.Errorf("spi preset %q: CS must be a GPIO 0..47", raw)
			}
			spec.name, spec.cs = info.Name, cs
		default:
			return nil, fmt.Errorf("unknown bus kind %q", kind)
		}
		out = append(out, spec)
	}
	return out, nil
}

// attachBusPresets attaches built-in device models to the chip. addSPI joins
// them to the same per-bus client list as LCD/SD/Ethernet.
func attachBusPresets(
	c *rp2350.Chip,
	log *slog.Logger,
	specs []busPresetSpec,
	addSPI func(bus, cs int, fn func(byte) byte),
) error {
	selects := map[int]func(){}
	for _, spec := range specs {
		switch spec.kind {
		case devices.BusI2C:
			if c.HasI2CDevice(spec.bus, uint8(spec.addr)) {
				return fmt.Errorf("i2c preset %s: bus %d address 0x%02x already has a device",
					spec.name, spec.bus, spec.addr)
			}
			dev, addr, err := devices.NewI2CPreset(spec.name, spec.addr)
			if err != nil {
				return err
			}
			c.AttachI2C(spec.bus, addr, dev)
			log.Info("attached I2C preset",
				slog.String("preset", spec.name),
				slog.Int("bus", spec.bus),
				slog.String("addr", fmt.Sprintf("0x%02x", addr)))
		case devices.BusSPI:
			p, err := devices.NewSPIPreset(spec.name)
			if err != nil {
				return err
			}
			addSPI(spec.bus, spec.cs, p.Transfer)
			selects[spec.cs] = p.Select
			log.Info("attached SPI preset",
				slog.String("preset", spec.name),
				slog.Int("bus", spec.bus),
				slog.Int("cs", spec.cs))
		}
	}
	if len(selects) > 0 {
		prev := c.Pins.OnChange
		c.Pins.OnChange = func(pin int, level bool) {
			if prev != nil {
				prev(pin, level)
			}
			if !level {
				if f := selects[pin]; f != nil {
					f()
				}
			}
		}
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
