//go:build js

package deviceplugin

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"syscall/js"
)

// nativeBackend runs the plugin in the browser's WebAssembly engine. The host
// imports are JavaScript functions that call back into Go; calls are
// synchronous once the module has been instantiated.
type nativeBackend struct {
	p        *Plugin
	cfg      Config
	source   []byte
	instance js.Value
	exports  js.Value
	funcs    []js.Func
	seed     uint64
	throwing js.Value
}

func newBackend(p *Plugin, cfg Config, artifact []byte) (backend, error) {
	wasm := js.Global().Get("WebAssembly")
	if wasm.IsUndefined() || wasm.IsNull() {
		return nil, fmt.Errorf("deviceplugin: WebAssembly is unavailable")
	}
	b := &nativeBackend{p: p, cfg: cfg, source: cfg.Source, seed: 0x9e3779b97f4a7c15}
	if len(artifact) == 0 {
		return nil, fmt.Errorf("deviceplugin: empty wasm artifact")
	}
	b.throwing = js.Global().Get("Function").New("msg", "throw new Error(msg)")

	imports := js.Global().Get("Object").New()
	imports.Set("plugin", b.hostImports())
	imports.Set("plugin_rt", b.runtimeImports())
	imports.Set("wasi_snapshot_preview1", b.wasiImports())

	bytes := js.Global().Get("Uint8Array").New(len(artifact))
	js.CopyBytesToJS(bytes, artifact)

	done := make(chan struct{})
	var instance js.Value
	var loadErr error
	settled := false
	settle := func() {
		if !settled {
			settled = true
			close(done)
		}
	}
	then := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			instance = args[0].Get("instance")
		}
		settle()
		return nil
	})
	catch := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			loadErr = fmt.Errorf("%s", args[0].String())
		} else {
			loadErr = fmt.Errorf("WebAssembly.instantiate failed")
		}
		settle()
		return nil
	})
	defer then.Release()
	defer catch.Release()
	promise := wasm.Call("instantiate", bytes, imports)
	promise.Call("then", then).Call("catch", catch)
	<-done
	if loadErr != nil {
		b.release()
		return nil, fmt.Errorf("deviceplugin: instantiate %s: %w", p.name, loadErr)
	}
	if instance.IsUndefined() || instance.IsNull() {
		b.release()
		return nil, fmt.Errorf("deviceplugin: %s did not instantiate", p.name)
	}
	b.instance = instance
	b.exports = instance.Get("exports")
	return b, nil
}

func (b *nativeBackend) release() {
	for _, f := range b.funcs {
		f.Release()
	}
	b.funcs = b.funcs[:0]
}

func (b *nativeBackend) Close() error {
	b.release()
	return nil
}

func (b *nativeBackend) Init(config []byte) error {
	if v := b.callInt("plugin_abi_version"); v != 1 {
		return fmt.Errorf("deviceplugin: %s ABI %d, want 1", b.p.name, v)
	}
	alloc := b.exports.Get("plugin_alloc")
	initf := b.exports.Get("plugin_init")
	if alloc.IsUndefined() || initf.IsUndefined() {
		return fmt.Errorf("deviceplugin: %s does not export plugin_alloc/plugin_init", b.p.name)
	}
	ptr := alloc.Invoke(js.ValueOf(len(config)))
	if ptr.IsUndefined() {
		return fmt.Errorf("deviceplugin: %s alloc failed", b.p.name)
	}
	if len(config) > 0 && ptr.Int() == 0 {
		return fmt.Errorf("deviceplugin: %s plugin_alloc returned 0 for %d config bytes", b.p.name, len(config))
	}
	if len(config) > 0 && !b.writeMemory(uint32(ptr.Int()), config) {
		return fmt.Errorf("deviceplugin: %s config does not fit memory", b.p.name)
	}
	res := initf.Invoke(ptr, js.ValueOf(len(config)))
	if res.IsUndefined() || res.Int() != 0 {
		return fmt.Errorf("deviceplugin: %s init failed", b.p.name)
	}
	return nil
}

// call invokes one exported function, converting a JS exception into a Go
// error. Callers that need the result use callInt.
func (b *nativeBackend) call(name string, args ...js.Value) (result js.Value, err error) {
	f := b.exports.Get(name)
	if f.IsUndefined() {
		return js.Undefined(), fmt.Errorf("deviceplugin: %s does not export %s", b.p.name, name)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("deviceplugin: %s.%s: %v", b.p.name, name, r)
		}
	}()
	anyArgs := make([]any, len(args))
	for i := range args {
		anyArgs[i] = args[i]
	}
	return f.Invoke(anyArgs...), nil
}

func (b *nativeBackend) callInt(name string, args ...js.Value) int32 {
	v, err := b.call(name, args...)
	if err != nil {
		b.p.setErr(err)
		return -1
	}
	if v.IsUndefined() || v.IsNull() {
		return 0
	}
	return int32(v.Int())
}

func (b *nativeBackend) Tick(us uint64) {
	if _, err := b.call("plugin_tick", bigInt(us)); err != nil {
		b.p.setErr(err)
	}
}

func (b *nativeBackend) Reset() {
	if _, err := b.call("plugin_reset"); err != nil {
		b.p.setErr(err)
	}
}

func (b *nativeBackend) I2CBegin(addr uint8)  { b.callInt("i2c_begin", js.ValueOf(int(addr))) }
func (b *nativeBackend) I2CEnd()              { b.callInt("i2c_end") }
func (b *nativeBackend) I2CWrite(v byte) bool { return b.callInt("i2c_write", js.ValueOf(int(v))) == 1 }
func (b *nativeBackend) I2CRead() (byte, bool) {
	v := b.callInt("i2c_read")
	if v < 0 {
		return 0, false
	}
	return byte(v), true
}

func (b *nativeBackend) SPISelect(active bool) {
	v := 0
	if active {
		v = 1
	}
	b.callInt("spi_select", js.ValueOf(v))
}

func (b *nativeBackend) SPITransfer(tx byte) byte {
	v := b.callInt("spi_transfer", js.ValueOf(int(tx)))
	if v < 0 {
		return 0xFF
	}
	return byte(v)
}

func (b *nativeBackend) UARTTX(v byte) { b.callInt("uart_tx", js.ValueOf(int(v))) }

// --- host imports --------------------------------------------------------

func (b *nativeBackend) addFunc(obj js.Value, name string, fn func(js.Value, []js.Value) any) {
	f := js.FuncOf(fn)
	b.funcs = append(b.funcs, f)
	obj.Set(name, f)
}

func (b *nativeBackend) hostImports() js.Value {
	obj := js.Global().Get("Object").New()
	b.addFunc(obj, "host_log", func(_ js.Value, args []js.Value) any {
		if len(args) < 2 {
			return nil
		}
		n := args[1].Int()
		if n <= 0 || n > 4096 {
			return nil
		}
		if data := b.readMemory(uint32(args[0].Int()), uint32(n)); len(data) > 0 {
			b.p.addLog(string(data))
		}
		return nil
	})
	b.addFunc(obj, "host_now_us", func(js.Value, []js.Value) any {
		return bigInt(b.p.nowUS())
	})
	b.addFunc(obj, "host_gpio_write", func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 && b.cfg.GPIOWrite != nil {
			b.cfg.GPIOWrite(args[0].Int(), args[1].Int() != 0)
		}
		return 0
	})
	b.addFunc(obj, "host_gpio_read", func(_ js.Value, args []js.Value) any {
		if len(args) >= 1 && b.cfg.GPIORead != nil {
			return b.cfg.GPIORead(args[0].Int())
		}
		return 0
	})
	b.addFunc(obj, "host_uart_send", func(_ js.Value, args []js.Value) any {
		if len(args) < 2 || b.cfg.UARTWrite == nil {
			return 0
		}
		n := args[1].Int()
		if n <= 0 || n > 4096 {
			return 0
		}
		data := b.readMemory(uint32(args[0].Int()), uint32(n))
		b.cfg.UARTWrite(data)
		return len(data)
	})
	return obj
}

func (b *nativeBackend) runtimeImports() js.Value {
	obj := js.Global().Get("Object").New()
	b.addFunc(obj, "program_len", func(js.Value, []js.Value) any { return len(b.source) })
	b.addFunc(obj, "program_read", func(_ js.Value, args []js.Value) any {
		if len(args) < 2 {
			return 0
		}
		ptr, cap := uint32(args[0].Int()), uint32(args[1].Int())
		return b.writeTruncated(ptr, cap, b.source)
	})
	return obj
}

func (b *nativeBackend) wasiImports() js.Value {
	obj := js.Global().Get("Object").New()
	b.addFunc(obj, "environ_sizes_get", func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 {
			b.writeU32(uint32(args[0].Int()), 0)
			b.writeU32(uint32(args[1].Int()), 0)
		}
		return 0
	})
	b.addFunc(obj, "environ_get", func(js.Value, []js.Value) any { return 0 })
	b.addFunc(obj, "clock_time_get", func(_ js.Value, args []js.Value) any {
		if len(args) >= 3 {
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], b.p.nowUS()*1000)
			b.writeMemory(uint32(args[2].Int()), buf[:])
		}
		return 0
	})
	b.addFunc(obj, "fd_write", func(_ js.Value, args []js.Value) any {
		if len(args) < 4 {
			return 8 // EBADF
		}
		iovs, count := uint32(args[1].Int()), uint32(args[2].Int())
		if count > 64 {
			return 28 // EINVAL
		}
		var line []byte
		for i := uint32(0); i < count; i++ {
			raw := b.readMemory(iovs+i*8, 8)
			if len(raw) != 8 {
				return 28
			}
			ptr := binary.LittleEndian.Uint32(raw[0:])
			n := binary.LittleEndian.Uint32(raw[4:])
			if n > 4096 {
				return 28
			}
			line = append(line, b.readMemory(ptr, n)...)
		}
		b.writeU32(uint32(args[3].Int()), uint32(len(line)))
		if len(line) > 0 {
			// Lua's print is bound to host_log; this catches libc writes.
			for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
				line = line[:len(line)-1]
			}
			if len(line) > 0 {
				b.p.addLog(string(line))
			}
		}
		return 0
	})
	b.addFunc(obj, "fd_close", func(js.Value, []js.Value) any { return 0 })
	b.addFunc(obj, "fd_fdstat_get", func(js.Value, []js.Value) any { return 52 })
	b.addFunc(obj, "fd_fdstat_set_flags", func(js.Value, []js.Value) any { return 52 })
	b.addFunc(obj, "fd_prestat_get", func(js.Value, []js.Value) any { return 8 })
	b.addFunc(obj, "fd_prestat_dir_name", func(js.Value, []js.Value) any { return 8 })
	b.addFunc(obj, "fd_read", func(js.Value, []js.Value) any { return 52 })
	b.addFunc(obj, "fd_renumber", func(js.Value, []js.Value) any { return 52 })
	b.addFunc(obj, "fd_seek", func(js.Value, []js.Value) any { return 52 })
	b.addFunc(obj, "path_open", func(js.Value, []js.Value) any { return 52 })
	b.addFunc(obj, "proc_exit", func(_ js.Value, args []js.Value) any {
		code := 0
		if len(args) > 0 {
			code = args[0].Int()
		}
		b.throwing.Invoke(fmt.Sprintf("plugin proc_exit(%d)", code))
		return nil
	})
	return obj
}

func (b *nativeBackend) memory() js.Value {
	if b.exports.IsUndefined() {
		return js.Undefined()
	}
	return b.exports.Get("memory")
}

func (b *nativeBackend) readMemory(ptr, n uint32) []byte {
	if n == 0 {
		return nil
	}
	mem := b.memory()
	if mem.IsUndefined() || mem.IsNull() {
		return nil
	}
	buf := mem.Get("buffer")
	if uint64(ptr)+uint64(n) > uint64(buf.Get("byteLength").Int()) {
		return nil
	}
	view := js.Global().Get("Uint8Array").New(buf, int(ptr), int(n))
	out := make([]byte, n)
	js.CopyBytesToGo(out, view)
	return out
}

func (b *nativeBackend) writeMemory(ptr uint32, data []byte) bool {
	if len(data) == 0 {
		return true
	}
	mem := b.memory()
	if mem.IsUndefined() || mem.IsNull() {
		return false
	}
	buf := mem.Get("buffer")
	if uint64(ptr)+uint64(len(data)) > uint64(buf.Get("byteLength").Int()) {
		return false
	}
	dst := js.Global().Get("Uint8Array").New(buf, int(ptr), len(data))
	js.CopyBytesToJS(dst, data)
	return true
}

func (b *nativeBackend) writeTruncated(ptr, cap uint32, data []byte) int {
	if uint32(len(data)) > cap {
		data = data[:cap]
	}
	if !b.writeMemory(ptr, data) {
		return 0
	}
	return len(data)
}

func (b *nativeBackend) writeU32(ptr, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	b.writeMemory(ptr, buf[:])
}

func bigInt(v uint64) js.Value {
	return js.Global().Get("BigInt").Invoke(strconv.FormatUint(v, 10))
}
