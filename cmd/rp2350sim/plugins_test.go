package main

import "testing"

func TestParsePluginSpec(t *testing.T) {
	cases := []struct {
		kind string
		raw  string
		ok   bool
	}{
		{"i2c", "0:0x76=plugins/bme280.lua", true},
		{"i2c", "2:0x76=p.lua", false},
		{"i2c", "0:0x76", false},
		{"spi", "0:17=plugins/flash.wasm", true},
		{"spi", "0:99=p.wasm", false},
		{"uart", "1=plugins/modem.lua", true},
		{"uart", "2=p.lua", false},
	}
	for _, c := range cases {
		if _, err := parsePluginSpec(c.kind, c.raw); (err == nil) != c.ok {
			t.Errorf("parsePluginSpec(%s, %s) err=%v, want ok=%v", c.kind, c.raw, err, c.ok)
		}
	}
}
