package ble

import (
	"fmt"
	"net"

	"github.com/akhenakh/rp2350sim/internal/debug"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// BindCYW43 installs the CYW43 symbol hooks on a loaded ELF. The binding is
// deliberately strict: a missing symbol means LTO, a different SDK or a
// stripped image, and running the real bus path would hang instead of telling
// the user why.
func BindCYW43(c *rp2350.Chip, info *debug.Info, ctrl *Controller) error {
	if info == nil {
		return fmt.Errorf("ble: the image has no symbol table; use an unstripped ELF")
	}
	type binding struct {
		name     string
		fn       rp2350.SymbolHook
		required bool
	}
	bindings := []binding{
		{"cyw43_ll_bus_init", ctrl.busInit, true},
		{"cyw43_ll_bus_sleep", ctrl.busSleep, true},
		{"cyw43_ll_wifi_get_mac", ctrl.wifiGetMAC, true},
		{"cyw43_btbus_init", ctrl.btbusInit, true},
		{"cyw43_btbus_write", ctrl.btbusWrite, true},
		{"cyw43_btbus_read", ctrl.btbusRead, true},
		{"cyw43_ll_bt_has_work", ctrl.btHasWork, true},
		{"cyw43_ll_has_work", ctrl.wifiHasWork, true},
		{"cyw43_gpio_set", ctrl.gpioSet, false},
	}
	for _, b := range bindings {
		addr, ok := info.Resolve(b.name)
		if !ok {
			if b.required {
				return fmt.Errorf("ble: %s missing from the ELF (LTO or a different SDK?)", b.name)
			}
			continue
		}
		c.InstallSymbolHook(addr, b.fn)
	}
	return nil
}

// ParseAddress parses a colon-separated BLE address in display order.
func ParseAddress(s string) ([6]byte, error) {
	var out [6]byte
	mac, err := net.ParseMAC(s)
	if err != nil || len(mac) != 6 {
		return out, fmt.Errorf("ble: invalid address %q", s)
	}
	copy(out[:], mac)
	return out, nil
}
