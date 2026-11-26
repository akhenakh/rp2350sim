package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// parseDHT parses PIN[:KIND[:HUMIDITY[:TEMP_C]]].
func parseDHT(raw string) (int, devices.DHTConfig, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) > 4 {
		return 0, devices.DHTConfig{}, fmt.Errorf("dht: invalid spec %q", raw)
	}
	pin, err := strconv.Atoi(parts[0])
	if err != nil || pin < 0 || pin > 47 {
		return 0, devices.DHTConfig{}, fmt.Errorf("dht: invalid pin %q", raw)
	}
	cfg := devices.DHTConfig{Kind: "dht22", Humidity: 45, Temperature: 23}
	if len(parts) > 1 && parts[1] != "" {
		cfg.Kind = strings.ToLower(parts[1])
		if cfg.Kind != "dht11" && cfg.Kind != "dht22" {
			return 0, devices.DHTConfig{}, fmt.Errorf("dht: unknown kind %q", parts[1])
		}
	}
	if len(parts) > 2 && parts[2] != "" {
		cfg.Humidity, err = strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return 0, devices.DHTConfig{}, fmt.Errorf("dht: invalid humidity %q", parts[2])
		}
	}
	if len(parts) > 3 && parts[3] != "" {
		cfg.Temperature, err = strconv.ParseFloat(parts[3], 64)
		if err != nil {
			return 0, devices.DHTConfig{}, fmt.Errorf("dht: invalid temperature %q", parts[3])
		}
	}
	return pin, cfg, nil
}

// parseUltrasonic parses TRIG:ECHO[:DISTANCE_CM].
func parseUltrasonic(raw string) (int, int, float64, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, fmt.Errorf("ultrasonic: invalid spec %q", raw)
	}
	trig, err1 := strconv.Atoi(parts[0])
	echo, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || trig < 0 || trig > 47 || echo < 0 || echo > 47 || trig == echo {
		return 0, 0, 0, fmt.Errorf("ultrasonic: invalid pins %q", raw)
	}
	cm := 100.0
	if len(parts) == 3 && parts[2] != "" {
		cm, err1 = strconv.ParseFloat(parts[2], 64)
		if err1 != nil || cm <= 0 {
			return 0, 0, 0, fmt.Errorf("ultrasonic: invalid distance %q", parts[2])
		}
	}
	return trig, echo, cm, nil
}

func attachWireDevices(c *rp2350.Chip, dhtSpecs, ultrasonicSpecs []string) error {
	for _, raw := range dhtSpecs {
		pin, cfg, err := parseDHT(raw)
		if err != nil {
			return err
		}
		devices.NewDHT(devices.NewWire(c, pin), cfg)
	}
	used := map[int]bool{}
	for _, raw := range ultrasonicSpecs {
		trig, echo, cm, err := parseUltrasonic(raw)
		if err != nil {
			return err
		}
		if used[trig] || used[echo] {
			return fmt.Errorf("ultrasonic: pin reused in %q", raw)
		}
		used[trig], used[echo] = true, true
		devices.NewHCSR04(devices.NewWire(c, trig), devices.NewWire(c, echo), cm)
	}
	return nil
}
