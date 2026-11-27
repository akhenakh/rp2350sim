//go:build !js

package deviceplugin

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	pluginCallTimeout = 50 * time.Millisecond
	pluginInitTimeout = 500 * time.Millisecond
)

// sharedCompilationCache is process-wide: the Lua runtime and repeated plugin
// instances compile once and reuse the same machine code.
var sharedCompilationCache = wazero.NewCompilationCache()

type wazeroBackend struct {
	p   *Plugin
	cfg Config
	rt  wazero.Runtime
	mod api.Module
}

func newBackend(p *Plugin, cfg Config, artifact []byte) (backend, error) {
	ctx := context.Background()
	rconf := wazero.NewRuntimeConfigCompiler().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(1024).
		WithCompilationCache(sharedCompilationCache)
	rt := wazero.NewRuntimeWithConfig(ctx, rconf)
	b := &wazeroBackend{p: p, cfg: cfg, rt: rt}
	closeOnError := func(err error) (backend, error) {
		_ = rt.Close(ctx)
		return nil, err
	}

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return closeOnError(fmt.Errorf("deviceplugin: wasi: %w", err))
	}
	if _, err := rt.NewHostModuleBuilder("plugin").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, ptr, n uint32) {
			line := readString(mod, ptr, n, 4096)
			if line != "" {
				p.addLog(line)
			}
		}).Export("host_log").
		NewFunctionBuilder().
		WithFunc(func() uint64 { return p.nowUS() }).Export("host_now_us").
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
		Instantiate(ctx); err != nil {
		return closeOnError(fmt.Errorf("deviceplugin: host: %w", err))
	}

	if cfg.Lang == Lua {
		program := cfg.Source
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
	b.mod = mod
	return b, nil
}

func (b *wazeroBackend) Init(config []byte) error {
	if v := b.call("plugin_abi_version"); v != 1 {
		return fmt.Errorf("deviceplugin: %s ABI %d, want 1", b.p.name, v)
	}
	alloc := b.mod.ExportedFunction("plugin_alloc")
	initf := b.mod.ExportedFunction("plugin_init")
	if alloc == nil || initf == nil {
		return fmt.Errorf("deviceplugin: %s does not export plugin_alloc/plugin_init", b.p.name)
	}
	ptr, err := b.callErrWith(alloc, pluginInitTimeout, uint64(len(config)))
	if err != nil {
		return fmt.Errorf("deviceplugin: %s alloc: %w", b.p.name, err)
	}
	if len(config) > 0 {
		if ptr == 0 {
			return fmt.Errorf("deviceplugin: %s plugin_alloc returned 0 for %d config bytes", b.p.name, len(config))
		}
		mem := b.mod.Memory()
		if mem == nil || !mem.Write(uint32(ptr), config) {
			return fmt.Errorf("deviceplugin: %s config does not fit memory", b.p.name)
		}
	}
	res, err := b.callErrWith(initf, pluginInitTimeout, ptr, uint64(len(config)))
	if err != nil {
		return fmt.Errorf("deviceplugin: %s init: %w", b.p.name, err)
	}
	if uint32(res) != 0 {
		return fmt.Errorf("deviceplugin: %s init failed (status %d)", b.p.name, uint32(res))
	}
	return nil
}

func (b *wazeroBackend) Close() error { return b.rt.Close(context.Background()) }

func (b *wazeroBackend) call(name string, args ...uint64) int32 {
	f := b.mod.ExportedFunction(name)
	if f == nil {
		return -1
	}
	v, err := b.callErr(f, args...)
	if err != nil {
		b.p.setErr(err)
		return -1
	}
	return int32(v)
}

func (b *wazeroBackend) callErr(f api.Function, args ...uint64) (uint64, error) {
	return b.callErrWith(f, pluginCallTimeout, args...)
}

func (b *wazeroBackend) callErrWith(f api.Function, timeout time.Duration, args ...uint64) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := f.Call(ctx, args...)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

func (b *wazeroBackend) Tick(us uint64) {
	if f := b.mod.ExportedFunction("plugin_tick"); f != nil {
		if _, err := b.callErr(f, us); err != nil {
			b.p.setErr(err)
		}
	}
}

func (b *wazeroBackend) Reset() {
	if f := b.mod.ExportedFunction("plugin_reset"); f != nil {
		if _, err := b.callErr(f); err != nil {
			b.p.setErr(err)
		}
	}
}

func (b *wazeroBackend) I2CBegin(addr uint8)  { b.call("i2c_begin", uint64(addr)) }
func (b *wazeroBackend) I2CEnd()              { b.call("i2c_end") }
func (b *wazeroBackend) I2CWrite(v byte) bool { return b.call("i2c_write", uint64(v)) == 1 }
func (b *wazeroBackend) I2CRead() (byte, bool) {
	v := b.call("i2c_read")
	if v < 0 {
		return 0, false
	}
	return byte(v), true
}

func (b *wazeroBackend) SPISelect(active bool) {
	v := uint64(0)
	if active {
		v = 1
	}
	b.call("spi_select", v)
}

func (b *wazeroBackend) SPITransfer(tx byte) byte {
	v := b.call("spi_transfer", uint64(tx))
	if v < 0 {
		return 0xFF
	}
	return byte(v)
}

func (b *wazeroBackend) UARTTX(v byte) { b.call("uart_tx", uint64(v)) }

func readString(mod api.Module, ptr, n, limit uint32) string {
	if n > limit || mod.Memory() == nil {
		return ""
	}
	return string(readBytes(mod, ptr, n))
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
