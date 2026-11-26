// Package hang turns a tight-loop report from the chip into a human-readable
// explanation using DWARF and the RP2350 SVD.
package hang

import (
	"fmt"
	"time"

	"github.com/akhenakh/rp2350sim/internal/debug"
	"github.com/akhenakh/rp2350sim/internal/regmap"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// Describe reports where the core is spinning, for how long, and what MMIO
// register/bit it polls. peek must be side-effect free (Chip.PeekRegister).
func Describe(info rp2350.HangInfo, infoDB *debug.Info, peek func(uint32) (uint32, bool)) string {
	loc := fmt.Sprintf("%#08x", info.PC)
	if infoDB != nil {
		fn := ""
		if f, ok := infoDB.FunctionAt(info.PC); ok {
			fn = f.Name
		}
		file, line, ok := infoDB.LineAt(info.PC)
		switch {
		case fn != "" && ok:
			loc = fmt.Sprintf("%s (%s:%d)", fn, file, line)
		case fn != "":
			loc = fn
		case ok:
			loc = fmt.Sprintf("%s:%d", file, line)
		}
	}
	dur := time.Duration(float64(info.LoopCycles) / 150e6 * float64(time.Second)).Round(time.Millisecond)
	out := fmt.Sprintf("core%d is spinning at %s for %s", info.Core, loc, dur)
	if info.MMIOAddr != 0 {
		out += " · " + regmap.Describe(info.MMIOAddr, info.MMIOVal, peek)
	} else {
		out += " · no peripheral poll (tight loop)"
	}
	return out
}
