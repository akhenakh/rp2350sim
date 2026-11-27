package deviceplugin

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestLuaConfigEscaping(t *testing.T) {
	hostile := `"} ; evil() ; t={"` + "\n\t\x00"
	cfg := map[string]any{
		"x":     hostile,
		"list":  []any{"a", 1.5, true},
		"snow":  "☃",
		"float": -0.25,
	}
	got, err := ConfigBytes(Lua, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := "[" + strconv.Quote("x") + "]=" + strconv.Quote(hostile)
	if !strings.Contains(string(got), want) {
		t.Fatalf("config = %s, missing %s", got, want)
	}
}

func TestLuaConfigRejectsBadValues(t *testing.T) {
	if _, err := ConfigBytes(Lua, map[string]any{"nan": math.NaN()}); err == nil {
		t.Fatal("NaN accepted")
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := ConfigBytes(Lua, cycle); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestPluginExecutionTimeout(t *testing.T) {
	if runtime.GOOS == "js" {
		t.Skip("the browser engine cannot preempt a synchronous call")
	}
	p, err := Load(Config{
		Name: "spin",
		Lang: Lua,
		Source: []byte(`
return {
  new = function() return {
    tick = function(self) while true do end end,
  } end,
}
`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	done := make(chan struct{})
	go func() {
		p.Tick(1)
		close(done)
	}()
	select {
	case <-done:
		if p.Err() == nil {
			t.Fatal("infinite tick returned without an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("infinite tick was not interrupted")
	}
}

func TestLanguageForPathQuery(t *testing.T) {
	for _, p := range []string{"dev.lua?v=2", "dev.wasm#frag"} {
		if _, err := LanguageForPath(p); err != nil {
			t.Fatalf("LanguageForPath(%q): %v", p, err)
		}
	}
}

func TestLuaTimeIsDeterministic(t *testing.T) {
	src := []byte(`
return { new = function() return {
  tick = function(self)
    math.randomseed()
    print(string.format("%.9f", math.random()))
  end,
} end }
`)
	run := func() string {
		var logs []string
		p, err := Load(Config{Name: "det", Lang: Lua, Source: src,
			Log: func(line string) { logs = append(logs, line) }})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		p.SetNowUS(5_000_000)
		p.Tick(5_000_000)
		if len(logs) == 0 {
			t.Fatal("no log")
		}
		return logs[0]
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("time-seeded random differs: %q vs %q", a, b)
	}
}
