package main

import (
	"log/slog"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestParseBusPresetSpecs(t *testing.T) {
	i2c, err := parseBusPresetSpecs(devices.BusI2C, []string{
		"0:bme280",
		"1:ssd1306@0x3d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(i2c) != 2 || i2c[0].name != "bme280" || i2c[0].addr != 0x76 ||
		i2c[1].name != "ssd1306" || i2c[1].addr != 0x3d {
		t.Fatalf("i2c presets = %+v", i2c)
	}
	spi, err := parseBusPresetSpecs(devices.BusSPI, []string{"0:w25q16@17"})
	if err != nil {
		t.Fatal(err)
	}
	if len(spi) != 1 || spi[0].name != "w25q16" || spi[0].cs != 17 {
		t.Fatalf("spi presets = %+v", spi)
	}

	badI2C := []string{
		"0:nope",
		"2:bme280",
		"0:bme280@0x50",
		"0:w25q16",
		"0:ssd1306@0x76",
	}
	for _, spec := range badI2C {
		if _, err := parseBusPresetSpecs(devices.BusI2C, []string{spec}); err == nil {
			t.Fatalf("accepted invalid I2C preset %q", spec)
		}
	}
	badSPI := []string{
		"0:w25q16",
		"0:bme280@17",
		"0:w25q16@99",
		"3:w25q16@17",
	}
	for _, spec := range badSPI {
		if _, err := parseBusPresetSpecs(devices.BusSPI, []string{spec}); err == nil {
			t.Fatalf("accepted invalid SPI preset %q", spec)
		}
	}
}

func TestAttachBusPresets(t *testing.T) {
	c := rp2350.New()
	specs, err := parseBusPresetSpecs(devices.BusI2C, []string{"0:bme280@0x76", "0:mpu6050@0x68"})
	if err != nil {
		t.Fatal(err)
	}
	spi, err := parseBusPresetSpecs(devices.BusSPI, []string{"0:w25q16@17"})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	type spiDev struct {
		bus, cs int
		fn      func(byte) byte
	}
	var got []spiDev
	err = attachBusPresets(c, log, append(specs, spi...), func(bus, cs int, fn func(byte) byte) {
		got = append(got, spiDev{bus, cs, fn})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasI2CDevice(0, 0x76) || !c.HasI2CDevice(0, 0x68) {
		t.Fatal("I2C presets were not attached")
	}
	if len(got) != 1 || got[0].bus != 0 || got[0].cs != 17 {
		t.Fatalf("SPI presets = %+v", got)
	}
	// The W25Q16 answers JEDEC EF 40 15 after CS is asserted.
	c.Pins.SetExternal(17, true)
	c.Pins.SetExternal(17, false)
	if v := got[0].fn(0x9F); v != 0xEF {
		t.Fatalf("JEDEC first byte = %#x", v)
	}
	if v := got[0].fn(0x9F); v != 0x40 {
		t.Fatalf("JEDEC second byte = %#x", v)
	}
	// A second attachment on the same address must be rejected.
	if err := attachBusPresets(c, log, specs[:1], func(int, int, func(byte) byte) {}); err == nil {
		t.Fatal("duplicate I2C preset was accepted")
	}
}
