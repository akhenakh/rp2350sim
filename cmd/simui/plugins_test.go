package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// TestUARTPluginRepliesOnAttachedUART checks both directions of a serial
// plugin. Firmware writes go to the attached UART, and uart_send replies must
// return to that same UART rather than the first one. A UART index parsed from
// the Bus-tab slot label sent every reply to UART0.
func TestUARTPluginRepliesOnAttachedUART(t *testing.T) {
	const source = `return {
  new = function(config)
    local d = {}
    function d:uart_tx(b) uart_send("K") end
    function d:tick(us) end
    return d
  end,
}
`
	path := filepath.Join(t.TempDir(), "uart-echo.lua")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	def := PluginDef{Name: "uart-echo", File: path}

	for _, uart := range []int{0, 1} {
		t.Run(fmt.Sprintf("uart%d", uart), func(t *testing.T) {
			c := rp2350.New()
			e := &Emu{chip: c}
			defer e.closePlugins()
			board := &BoardDef{UARTPlugins: []UARTPluginDef{{PluginDef: def, UART: uart}}}
			e.attachPlugins(c, board, func(int, int, func(byte) byte) {})
			if len(e.pluginErrs) != 0 {
				t.Fatalf("plugin errors: %v", e.pluginErrs)
			}
			if len(e.plugins) != 1 {
				t.Fatalf("plugins = %d, want 1", len(e.plugins))
			}

			base := uint32(0x40070000 + uart*0x8000)
			other := uint32(0x40070000 + (1-uart)*0x8000)
			c.Write32(base, '?')
			if got := c.Read32(base); got != 'K' {
				t.Fatalf("UART%d reply = %#x, want 'K'", uart, got)
			}
			// RXFE is bit 4 of UARTFR; the unused UART must stay empty.
			if fr := c.Read32(other + 0x18); fr&(1<<4) == 0 {
				t.Fatalf("UART%d received the reply instead (FR=%#x)", 1-uart, fr)
			}
		})
	}
}
