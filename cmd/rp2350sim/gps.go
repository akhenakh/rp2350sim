package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// attachGPS parses UART[:LAT:LON] and attaches an NMEA source.
func attachGPS(c *rp2350.Chip, raw string) error {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ":")
	uart, err := strconv.Atoi(parts[0])
	if err != nil || uart < 0 || uart > 1 {
		return fmt.Errorf("gps: invalid uart %q", raw)
	}
	cfg := devices.GPSConfig{Lat: 45.5017, Lon: -73.5673}
	if len(parts) > 1 && parts[1] != "" {
		cfg.Lat, err = strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return fmt.Errorf("gps: invalid latitude %q", parts[1])
		}
	}
	if len(parts) > 2 && parts[2] != "" {
		cfg.Lon, err = strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return fmt.Errorf("gps: invalid longitude %q", parts[2])
		}
	}
	g := devices.NewGPS(c, uart, cfg)
	c.AttachUART(uart, g)
	return nil
}
