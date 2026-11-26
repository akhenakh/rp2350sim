package deviceplugin

import (
	"os"
	"strings"
	"testing"
)

func TestLuaI2CUARTAndLog(t *testing.T) {
	var uart []byte
	var logs []string
	p, err := Load(Config{
		Name: "lua-test",
		Lang: Lua,
		Source: []byte(`
return {
  new = function(config)
    local d = { seed = config.seed }
    function d:i2c_begin(addr) d.addr = addr end
    function d:i2c_write(b) return b ~= 0 end
    function d:i2c_read() return 0x60, true end
    function d:i2c_end() end
    function d:spi_select(active) end
    function d:spi_transfer(tx) return tx + 1 end
    function d:uart_tx(b)
      print("tx " .. b .. " seed " .. d.seed)
      uart_send("ok")
    end
    function d:tick(us) end
    return d
  end,
}
`),
		Config:    []byte("{seed=7}"),
		UARTWrite: func(data []byte) { uart = append(uart, data...) },
		Log:       func(line string) { logs = append(logs, line) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.i2cBegin(0x76)
	if !p.i2cWrite(0x42) {
		t.Fatal("write was NAKed")
	}
	if p.i2cWrite(0) {
		t.Fatal("write 0 should be NAKed")
	}
	if v, ok := p.i2cRead(); !ok || v != 0x60 {
		t.Fatalf("read = %#x/%v", v, ok)
	}
	p.i2cEnd()
	if got := p.spiTransfer(0x10); got != 0x11 {
		t.Fatalf("spi transfer = %#x", got)
	}
	p.uartTX(9)
	if string(uart) != "ok" {
		t.Fatalf("uart = %q logs=%v", uart, p.Logs())
	}
	if len(p.Logs()) == 0 {
		t.Fatal("no logs")
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "tx 9 seed 7") {
		t.Fatalf("logs = %v", logs)
	}
	if len(p.Logs()) != 1 {
		t.Fatalf("plugin logs = %v", p.Logs())
	}
}

func TestWASMSpiPlugin(t *testing.T) {
	data, err := os.ReadFile("../../plugins/examples/spi-flash.wasm")
	if err != nil {
		t.Skip(err)
	}
	p, err := Load(Config{Name: "wasm-flash", Lang: WASM, Source: data, Config: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.spiSelect(true)
	if got := p.spiTransfer(0x9F); got != 0xEF {
		t.Fatalf("JEDEC[0] = %#x", got)
	}
	if got := p.spiTransfer(0); got != 0x40 {
		t.Fatalf("JEDEC[1] = %#x", got)
	}
	if got := p.spiTransfer(0); got != 0x15 {
		t.Fatalf("JEDEC[2] = %#x", got)
	}
}
