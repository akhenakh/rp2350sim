package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/deviceplugin"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// pluginSpec is one -i2c-plugin/-spi-plugin/-uart-plugin argument.
type pluginSpec struct {
	bus  int
	addr int
	cs   int
	uart int
	path string
}

// parsePluginSpec parses:
//
//	-i2c-plugin  0:0x76=path.lua
//	-spi-plugin  0:17=path.wasm
//	-uart-plugin 1=path.lua
func parsePluginSpec(kind, raw string) (pluginSpec, error) {
	left, path, ok := strings.Cut(raw, "=")
	if !ok || strings.TrimSpace(path) == "" {
		return pluginSpec{}, fmt.Errorf("%s expects slot=path", kind)
	}
	fields := strings.Split(left, ":")
	var s pluginSpec
	s.path = strings.TrimSpace(path)
	var err error
	switch kind {
	case "i2c":
		if len(fields) != 2 {
			return s, fmt.Errorf("-i2c-plugin expects bus:addr=path")
		}
		s.bus, err = strconv.Atoi(fields[0])
		if err != nil {
			return s, fmt.Errorf("i2c bus: %w", err)
		}
		var addr uint64
		addr, err = strconv.ParseUint(fields[1], 0, 8)
		s.addr = int(addr)
		if err != nil {
			return s, fmt.Errorf("i2c address: %w", err)
		}
		if s.bus < 0 || s.bus > 1 || s.addr < 0x08 || s.addr > 0x77 {
			return s, fmt.Errorf("i2c slot must be 0|1:0x08..0x77")
		}
	case "spi":
		if len(fields) != 2 {
			return s, fmt.Errorf("-spi-plugin expects bus:cs=path")
		}
		s.bus, err = strconv.Atoi(fields[0])
		if err != nil {
			return s, fmt.Errorf("spi bus: %w", err)
		}
		s.cs, err = strconv.Atoi(fields[1])
		if err != nil {
			return s, fmt.Errorf("spi cs: %w", err)
		}
		if s.bus < 0 || s.bus > 1 || s.cs < 0 || s.cs > 47 {
			return s, fmt.Errorf("spi slot must be 0|1:0..47")
		}
	case "uart":
		if len(fields) != 1 {
			return s, fmt.Errorf("-uart-plugin expects uart=path")
		}
		s.uart, err = strconv.Atoi(fields[0])
		if err != nil {
			return s, fmt.Errorf("uart: %w", err)
		}
		if s.uart < 0 || s.uart > 1 {
			return s, fmt.Errorf("uart must be 0 or 1")
		}
	default:
		return s, fmt.Errorf("unknown plugin kind %q", kind)
	}
	return s, nil
}

// loadPlugin reads and instantiates one CLI plugin.
func loadPlugin(kind string, spec pluginSpec, c *rp2350.Chip, log *slog.Logger) (*deviceplugin.Plugin, error) {
	source, err := os.ReadFile(spec.path)
	if err != nil {
		return nil, err
	}
	lang, err := deviceplugin.LanguageForPath(spec.path)
	if err != nil {
		return nil, err
	}
	var name string
	switch kind {
	case "i2c":
		name = fmt.Sprintf("i2c%d 0x%02x %s", spec.bus, spec.addr, spec.path)
	case "spi":
		name = fmt.Sprintf("spi%d cs%d %s", spec.bus, spec.cs, spec.path)
	default:
		name = fmt.Sprintf("uart%d %s", spec.uart, spec.path)
	}
	uart := spec.uart
	p, err := deviceplugin.Load(deviceplugin.Config{
		Name:   name,
		Lang:   lang,
		Source: source,
		GPIORead: func(pin int) int {
			if c.Pins.Level(pin) {
				return 1
			}
			return 0
		},
		GPIOWrite: func(pin int, level bool) { c.Pins.SetExternal(pin, level) },
		UARTWrite: func(data []byte) { c.SendUART(uart, data) },
		Log: func(line string) {
			log.Info("plugin", slog.String("name", name), slog.String("line", line))
		},
	})
	if err != nil {
		return nil, err
	}
	c.AddPeriodicHook(rp2350.CyclesPerMicrosecond*1000, func() { p.Tick(c.NowMicros()) })
	return p, nil
}
