# rp2350sim

A Go emulator for the Raspberry Pi RP2350 (Pico 2), aimed at running TinyGo
firmware with a Shirei desktop UI.

Status: **early work in progress**. The M33 core runs real TinyGo firmware
through package initialisation (clocks/PLL setup included). A remaining
instruction-level bug in runtime code (heap allocator or the 64-bit division
helper) stops it before `main()`; `AGENTS.md` records what is already fixed.

## Layout

```
cmd/rp2350sim/      headless runner and debugging tools
internal/m33/       ARMv8-M Mainline (Cortex-M33) interpreter
internal/rp2350/    chip: memory map, SIO, NVIC, timers, clocks, GPIO
internal/firmware/  UF2 / ELF / raw image loaders
```

## Running firmware

```sh
go run ./cmd/rp2350sim -fw firmware.elf -seconds 1 -trace-pins 25
```

Useful flags:

| flag | meaning |
|---|---|
| `-fw` | `.uf2`, `.elf` or raw binary |
| `-seconds` | virtual seconds to run (150 MHz master clock) |
| `-steps` | cycle budget instead of `-seconds` |
| `-trace-pins 25,15` | log GPIO level changes |
| `-strict-mmio` | halt on the first unmapped peripheral access |
| `-unhandled` | print a summary of unmapped accesses at exit |
| `-trace N` | trace the first N instructions |
| `-trace-last` | keep a ring of recent instructions for diagnostics |
| `-bp 0x10001b30` | breakpoint at an address |
| `-bp2 0x...` | logging breakpoint that continues |
| `-watch 0x2000177c` | log every write to an address |
| `-poke 0xADDR=0xVAL` | write memory before starting |

## Tests

```sh
go test ./...
```

`internal/m33/thumb_test.go` exercises instruction semantics with hand-assembled
encodings (LLVM `llvm-mc -triple=thumbv8m.main`).

## Known issues

- The firmware reaches `runtime.initAll` but the PLL divisor search yields
  `pd1 = 0`, which trips `pll.init`'s range check and panics. Control flow inside
  the inlined search loop diverges from expected behaviour; a QEMU-based
  differential harness is the planned way to flush the remaining core bugs.
- No USB CDC, UART, SPI, DMA, PIO or PWM yet, so serial output is unavailable and
  panic messages cannot be printed.
- Cycle accounting is one cycle per instruction; timing is not accurate.

## References

- RP2350 datasheet, `tinygo/src/device/rp/rp2350.go` for register definitions
- TinyGo `machine` and `runtime` packages for the boot contract
- `qemu-system-arm -M mps2-an505` as the Cortex-M33 instruction oracle
