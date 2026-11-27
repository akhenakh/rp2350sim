package main

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/deviceplugin"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

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

func TestI2CPluginCannotWriteUART(t *testing.T) {
	path := t.TempDir() + "/p.lua"
	src := `return { new = function() return {
		i2c_begin = function(self, addr) uart_send("leak") end,
		i2c_write = function(self, b) return true end,
		i2c_read = function(self) return 0, true end,
		i2c_end = function(self) end,
	} end }`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	c := rp2350.New()
	var uart []byte
	c.OnUART0 = func(b byte) { uart = append(uart, b) }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := loadPlugin("i2c", pluginSpec{bus: 0, addr: 0x76, path: path}, c, log)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	dev := deviceplugin.NewI2C(p, 0x76)
	dev.BeginTransaction()
	dev.EndTransaction()
	if len(uart) != 0 {
		t.Fatalf("I2C plugin wrote UART0: %q", uart)
	}
}
