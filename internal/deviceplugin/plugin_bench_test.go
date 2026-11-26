package deviceplugin

import (
	"os"
	"testing"
)

func benchPlugin(b *testing.B, lang Language, path, name string) {
	b.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		b.Skip(err)
	}
	p, err := Load(Config{Name: name, Lang: lang, Source: data})
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.spiSelect(true)
		p.spiTransfer(0x9F)
		p.spiTransfer(0)
		p.spiTransfer(0)
	}
}

// BenchmarkLuaSPI measures the Lua reactor's per-transaction overhead.
func BenchmarkLuaSPI(b *testing.B) {
	benchPlugin(b, Lua, "../../plugins/examples/w25q16.lua", "lua-spi")
}

// BenchmarkWASMSPI measures the same ABI through a compiled C module.
func BenchmarkWASMSPI(b *testing.B) {
	benchPlugin(b, WASM, "../../plugins/examples/spi-flash.wasm", "wasm-spi")
}
