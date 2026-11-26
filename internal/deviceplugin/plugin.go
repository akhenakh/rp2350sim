// Package deviceplugin runs external I2C, SPI and UART device models supplied
// as files. Lua scripts run inside a bundled Lua interpreter compiled to
// WebAssembly; compiled plugins are ordinary WASM modules. Both expose the
// same small ABI. Desktop uses wazero; the browser uses the native WebAssembly
// engine through a build-tagged backend.
package deviceplugin

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

//go:embed assets/device_lua.wasm
var luaRuntime []byte

// Language selects the plugin frontend.
type Language int

const (
	// Lua is a script run by the bundled device_lua.wasm interpreter.
	Lua Language = iota
	// WASM is a compiled device plugin module.
	WASM
)

// Config is one plugin instance. Source is the .lua or .wasm file; Config is
// the plugin's own configuration. A Lua config is a Lua table expression
// (empty means {}); a WASM config is opaque JSON bytes.
type Config struct {
	Name   string
	Lang   Language
	Source []byte
	Config []byte

	// GPIORead reports a configured GPIO input to the plugin.
	GPIORead func(pin int) int
	// GPIOWrite drives a configured output pin from the plugin.
	GPIOWrite func(pin int, level bool)
	// UARTWrite delivers bytes from the plugin to the firmware UART.
	UARTWrite func(data []byte)
	// Log receives one plugin line (print/host_log).
	Log func(line string)
}

// backend is the language/runtime-specific half. It is implemented by
// plugin_wazero.go on desktop and plugin_native_js.go in the browser.
type backend interface {
	Init(config []byte) error
	Close() error
	Tick(us uint64)
	Reset()
	I2CBegin(addr uint8)
	I2CWrite(b byte) bool
	I2CRead() (byte, bool)
	I2CEnd()
	SPISelect(active bool)
	SPITransfer(tx byte) byte
	UARTTX(b byte)
}

// Plugin is one loaded device model.
type Plugin struct {
	name    string
	backend backend

	mu      sync.Mutex
	logs    []string
	log     func(string)
	err     error
	clockUS uint64
}

const maxLogLines = 200

// Load compiles and initializes one plugin.
func Load(cfg Config) (*Plugin, error) {
	if len(cfg.Source) == 0 {
		return nil, errors.New("deviceplugin: empty plugin source")
	}
	var artifact []byte
	switch cfg.Lang {
	case Lua:
		artifact = luaRuntime
	case WASM:
		artifact = cfg.Source
	default:
		return nil, fmt.Errorf("deviceplugin: unknown language %d", cfg.Lang)
	}
	p := &Plugin{name: cfg.Name, log: cfg.Log}
	if p.name == "" {
		p.name = "plugin"
	}
	b, err := newBackend(p, cfg, artifact)
	if err != nil {
		return nil, err
	}
	p.backend = b
	if err := b.Init(cfg.Config); err != nil {
		_ = b.Close()
		return nil, err
	}
	return p, nil
}

// Close releases the plugin runtime.
func (p *Plugin) Close() error {
	if p.backend == nil {
		return nil
	}
	return p.backend.Close()
}

// Name is the instance name.
func (p *Plugin) Name() string { return p.name }

// Failed reports a Lua/runtime failure.
func (p *Plugin) Failed() bool { return p.Err() != nil }

// Err returns the last plugin/runtime error, if any.
func (p *Plugin) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Logs returns recent plugin output, oldest first.
func (p *Plugin) Logs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.logs...)
}

func (p *Plugin) addLog(line string) {
	p.mu.Lock()
	p.logs = append(p.logs, line)
	if len(p.logs) > maxLogLines {
		p.logs = p.logs[len(p.logs)-maxLogLines:]
	}
	p.mu.Unlock()
	if p.log != nil {
		p.log(line)
	}
}

func (p *Plugin) setErr(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	p.addLog("plugin error: " + err.Error())
}

// nowUS is the deterministic host clock exposed to Lua.
func (p *Plugin) nowUS() uint64 { return p.clockUS }

// SetNowUS advances the plugin clock, usually from the emulator's periodic hook.
func (p *Plugin) SetNowUS(us uint64) { p.clockUS = us }

func (p *Plugin) i2cBegin(addr uint8)      { p.backend.I2CBegin(addr) }
func (p *Plugin) i2cEnd()                  { p.backend.I2CEnd() }
func (p *Plugin) i2cWrite(b byte) bool     { return p.backend.I2CWrite(b) }
func (p *Plugin) i2cRead() (byte, bool)    { return p.backend.I2CRead() }
func (p *Plugin) spiSelect(active bool)    { p.backend.SPISelect(active) }
func (p *Plugin) spiTransfer(tx byte) byte { return p.backend.SPITransfer(tx) }
func (p *Plugin) uartTX(b byte)            { p.backend.UARTTX(b) }
func (p *Plugin) tick(us uint64)           { p.backend.Tick(us) }
func (p *Plugin) reset()                   { p.backend.Reset() }

// I2C adapts a plugin to rp2350.I2CDevice/I2CTransaction.
type I2C struct {
	p    *Plugin
	addr uint8
}

// NewI2C returns an I2C target. The plugin is responsible for knowing which
// addresses it implements.
func NewI2C(p *Plugin, addr uint8) *I2C { return &I2C{p: p, addr: addr} }

func (d *I2C) SetAddress(addr uint8) { d.addr = addr }
func (d *I2C) BeginTransaction()     { d.p.i2cBegin(d.addr) }
func (d *I2C) EndTransaction()       { d.p.i2cEnd() }
func (d *I2C) Write(b byte)          { _ = d.p.i2cWrite(b) }
func (d *I2C) WriteAck(b byte) bool  { return d.p.i2cWrite(b) }
func (d *I2C) Read() byte {
	v, _ := d.p.i2cRead()
	return v
}
func (d *I2C) ReadAck() (byte, bool) { return d.p.i2cRead() }

// SPI adapts a plugin to the SPI transfer/select hooks used by the UI and CLI.
type SPI struct {
	p *Plugin
}

// NewSPI returns a chip-select-bound SPI target.
func NewSPI(p *Plugin) *SPI { return &SPI{p: p} }

// Select is called on the chip-select edges.
func (d *SPI) Select(active bool) { d.p.spiSelect(active) }

// Transfer clocks one byte.
func (d *SPI) Transfer(tx byte) byte { return d.p.spiTransfer(tx) }

// UART adapts a plugin to rp2350.UARTDevice.
type UART struct {
	p *Plugin
}

// NewUART returns a UART device. Configure Config.UARTWrite to deliver plugin
// responses to the firmware UART.
func NewUART(p *Plugin) *UART { return &UART{p: p} }

// TX receives bytes the firmware writes.
func (d *UART) TX(b byte) { d.p.uartTX(b) }

// Reset asks the script to reset.
func (d *UART) Reset() { d.p.reset() }

// Tick advances the plugin clock and calls its tick hook.
func (p *Plugin) Tick(us uint64) {
	p.SetNowUS(us)
	p.tick(us)
}

// LanguageForPath picks the runtime from a .lua/.wasm file name.
func LanguageForPath(path string) (Language, error) {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".lua"):
		return Lua, nil
	case strings.HasSuffix(lower, ".wasm"):
		return WASM, nil
	default:
		return 0, fmt.Errorf("deviceplugin: %s is not a .lua or .wasm file", path)
	}
}

// ConfigBytes encodes a board/UI configuration map for a plugin. Lua gets a
// table expression (the shim evaluates it); WASM gets JSON.
func ConfigBytes(lang Language, cfg map[string]any) ([]byte, error) {
	if len(cfg) == 0 {
		if lang == WASM {
			return []byte("{}"), nil
		}
		return nil, nil
	}
	if lang == WASM {
		return json.Marshal(cfg)
	}
	var b strings.Builder
	if err := writeLuaValue(&b, cfg); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func writeLuaValue(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("nil")
	case string:
		b.WriteString(strconv.Quote(x))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case float32:
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	case []any:
		b.WriteByte('{')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeLuaValue(b, item); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case map[string]any:
		b.WriteByte('{')
		first := true
		for k, item := range x {
			if !first {
				b.WriteByte(',')
			}
			first = false
			b.WriteByte('[')
			b.WriteString(strconv.Quote(k))
			b.WriteString("]=")
			if err := writeLuaValue(b, item); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("deviceplugin: cannot encode config value %T", v)
	}
	return nil
}
