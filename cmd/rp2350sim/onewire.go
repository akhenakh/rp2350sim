package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// parseOneWireSpec parses PIN[:TEMP_C] for -onewire.
func parseOneWireSpec(raw string) (pin int, temp float64, err error) {
	pinText, tempText, hasTemp := strings.Cut(strings.TrimSpace(raw), ":")
	pin, err = strconv.Atoi(pinText)
	if err != nil || pin < 0 || pin > 47 {
		return 0, 0, fmt.Errorf("onewire: invalid pin %q", raw)
	}
	temp = 22.5
	if hasTemp && tempText != "" {
		temp, err = strconv.ParseFloat(tempText, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("onewire: invalid temperature %q", tempText)
		}
	}
	return pin, temp, nil
}

// attachOneWire attaches one DS18B20 per -onewire spec.
func attachOneWire(c *rp2350.Chip, specs []string) error {
	seen := map[int]bool{}
	for _, raw := range specs {
		pin, temp, err := parseOneWireSpec(raw)
		if err != nil {
			return err
		}
		if seen[pin] {
			return fmt.Errorf("onewire: pin %d used twice", pin)
		}
		seen[pin] = true
		w := devices.NewWire(c, pin)
		devices.NewDS18B20(w, devices.DS18B20Config{TemperatureC: temp})
	}
	return nil
}
