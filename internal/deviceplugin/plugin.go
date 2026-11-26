// Package deviceplugin runs external I2C, SPI and UART device models supplied
// as files. Lua scripts run inside a bundled Lua interpreter compiled to
// WebAssembly; compiled plugins are ordinary WASM modules. Both expose the
// same small ABI and run under wazero, so desktop and browser builds use the
// same host code.
package deviceplugin

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
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

// Plugin is one loaded device model.
type Plugin struct {
	name string
	rt   wazero.Runtime
	mod  api.Module

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

	ctx := context.Background()
	// The interpreter is the only engine wazero can run in a browser build,
	// and using it everywhere keeps plugin behavior identical.
	rconf := wazero.NewRuntimeConfigInterpreter().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling)
	rt := wazero.NewRuntimeWithConfig(ctx, rconf)

	p := &Plugin{name: cfg.Name, rt: rt, log: cfg.Log}
	if p.name == "" {
		p.name = "plugin"
	}
	closeOnError := func(err error) (*Plugin, error) {
		_ = rt.Close(ctx)
		return nil, err
	}

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return closeOnError(fmt.Errorf("deviceplugin: wasi: %w", err))
	}

	// Build all host functions in one module, not one module per function.
	_, err := rt.NewHostModuleBuilder("plugin").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, ptr, n uint32) {
			line := readString(mod, ptr, n, 4096)
			if line != "" {
				p.addLog(line)
			}
		}).Export("host_log").
		NewFunctionBuilder().
		WithFunc(func() uint64 {
			// The emulator installs a virtual clock later; plugins that only
			// need a monotonic value get a deterministic count.
			return p.nowUS()
		}).Export("host_now_us").
		NewFunctionBuilder().
		WithFunc(func(pin, level int32) int32 {
			if cfg.GPIOWrite != nil {
				cfg.GPIOWrite(int(pin), level != 0)
			}
			return 0
		}).Export("host_gpio_write").
		NewFunctionBuilder().
		WithFunc(func(pin int32) int32 {
			if cfg.GPIORead != nil {
				return int32(cfg.GPIORead(int(pin)))
			}
			return 0
		}).Export("host_gpio_read").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, ptr, n uint32) int32 {
			if cfg.UARTWrite == nil || n > 4096 {
				return 0
			}
			data := readBytes(mod, ptr, n)
			cfg.UARTWrite(data)
			return int32(len(data))
		}).Export("host_uart_send").
		Instantiate(ctx)
	if err != nil {
		return closeOnError(fmt.Errorf("deviceplugin: host: %w", err))
	}

	program := cfg.Source
	if cfg.Lang == Lua {
		// The Lua runtime reads the script through plugin_rt; a compiled
		// plugin needs no program channel.
		if _, err := rt.NewHostModuleBuilder("plugin_rt").
			NewFunctionBuilder().WithFunc(func() uint32 { return uint32(len(program)) }).Export("program_len").
			NewFunctionBuilder().WithFunc(func(ctx context.Context, mod api.Module, ptr, cap uint32) uint32 {
			return uint32(copyToMemory(mod, ptr, cap, program))
		}).Export("program_read").
			Instantiate(ctx); err != nil {
			return closeOnError(fmt.Errorf("deviceplugin: runtime: %w", err))
		}
	}

	compiled, err := rt.CompileModule(ctx, artifact)
	if err != nil {
		return closeOnError(fmt.Errorf("deviceplugin: compile %s: %w", p.name, err))
	}
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
		WithName(p.name).
		WithStartFunctions().
		WithStdout(io.Discard).
		WithStderr(io.Discard))
	if err != nil {
		return closeOnError(fmt.Errorf("deviceplugin: instantiate %s: %w", p.name, err))
	}
	p.mod = mod

	if err := p.init(cfg.Config); err != nil {
		return closeOnError(err)
	}
	return p, nil
}

// init writes the config through plugin_alloc and calls plugin_init.
func (p *Plugin) init(config []byte) error {
	if v := p.call("plugin_abi_version"); v != 1 {
		return fmt.Errorf("deviceplugin: %s ABI %d, want 1", p.name, v)
	}
	alloc := p.mod.ExportedFunction("plugin_alloc")
	initf := p.mod.ExportedFunction("plugin_init")
	if alloc == nil || initf == nil {
		return fmt.Errorf("deviceplugin: %s does not export plugin_alloc/plugin_init", p.name)
	}
	ptr, err := p.callErr(alloc, uint64(len(config)))
	if err != nil {
		return fmt.Errorf("deviceplugin: %s alloc: %w", p.name, err)
	}
	if len(config) > 0 {
		mem := p.mod.Memory()
		if mem == nil || !mem.Write(uint32(ptr), config) {
			return fmt.Errorf("deviceplugin: %s config does not fit memory", p.name)
		}
	}
	res, err := p.callErr(initf, ptr, uint64(len(config)))
	if err != nil {
		return fmt.Errorf("deviceplugin: %s init: %w", p.name, err)
	}
	if uint32(res) != 0 {
		return fmt.Errorf("deviceplugin: %s init failed (status %d)", p.name, uint32(res))
	}
	return nil
}

// Close releases the plugin runtime.
func (p *Plugin) Close() error { return p.rt.Close(context.Background()) }

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

// --- ABI calls ------------------------------------------------------------

func (p *Plugin) call(name string, args ...uint64) int32 {
	f := p.mod.ExportedFunction(name)
	if f == nil {
		return -1
	}
	v, err := p.callErr(f, args...)
	if err != nil {
		p.setErr(err)
		return -1
	}
	return int32(v)
}

func (p *Plugin) callErr(f api.Function, args ...uint64) (uint64, error) {
	res, err := f.Call(context.Background(), args...)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

func (p *Plugin) tick(us uint64) {
	if f := p.mod.ExportedFunction("plugin_tick"); f != nil {
		_, err := f.Call(context.Background(), us)
		if err != nil {
			p.setErr(err)
		}
	}
}

func (p *Plugin) reset() {
	if f := p.mod.ExportedFunction("plugin_reset"); f != nil {
		_, err := f.Call(context.Background())
		if err != nil {
			p.setErr(err)
		}
	}
}

func (p *Plugin) i2cBegin(addr uint8)  { p.call("i2c_begin", uint64(addr)) }
func (p *Plugin) i2cEnd()              { p.call("i2c_end") }
func (p *Plugin) i2cWrite(b byte) bool { return p.call("i2c_write", uint64(b)) == 1 }
func (p *Plugin) i2cRead() (byte, bool) {
	v := p.call("i2c_read")
	if v < 0 {
		return 0, false
	}
	return byte(v), true
}

func (p *Plugin) spiSelect(active bool) {
	v := uint64(0)
	if active {
		v = 1
	}
	p.call("spi_select", v)
}
func (p *Plugin) spiTransfer(tx byte) byte {
	v := p.call("spi_transfer", uint64(tx))
	if v < 0 {
		return 0xFF
	}
	return byte(v)
}
func (p *Plugin) uartTX(b byte) { p.call("uart_tx", uint64(b)) }

func readString(mod api.Module, ptr, n uint32, limit uint32) string {
	if n > limit || mod.Memory() == nil {
		return ""
	}
	data := readBytes(mod, ptr, n)
	return string(data)
}

func readBytes(mod api.Module, ptr, n uint32) []byte {
	if n == 0 || mod.Memory() == nil {
		return nil
	}
	data, ok := mod.Memory().Read(ptr, n)
	if !ok {
		return nil
	}
	return append([]byte(nil), data...)
}

func copyToMemory(mod api.Module, ptr, cap uint32, data []byte) int {
	if mod.Memory() == nil || cap == 0 {
		return 0
	}
	if uint32(len(data)) > cap {
		data = data[:cap]
	}
	if !mod.Memory().Write(ptr, data) {
		return 0
	}
	return len(data)
}

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
