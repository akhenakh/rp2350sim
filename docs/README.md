# rp2350sim documentation

`rp2350sim` runs unmodified `tinygo build -target=pico2` firmware: a Cortex-M33
interpreter, the RP2350 chip peripherals, and models for the boards and
external chips (ST7789 LCD, W5500/W6300/CH9120 Ethernet, WS2812B, virtual
Wi-Fi). A Shirei desktop UI adds board selection, live GPIO, a scope and a
logic analyzer.

This directory is the user guide:

| Document | Contents |
|---|---|
| [cli.md](cli.md) | every command-line flag, output contract, exit codes, examples |
| [ci.md](ci.md) | running firmware in CI, injecting input, asserting output, capturing artifacts |
| [tinygo.md](tinygo.md) | installing, building with TinyGo, drop-in Taskfile/Makefile targets, dev loop |
| [ui.md](ui.md) | the desktop UI, board picker, `boards.yaml`, tabs, scope, logic analyzer, serial bridge |
| [boards.md](boards.md) | supported boards, wiring, peripherals, and current limitations |
| [c.md](c.md) | C firmware with the Pico SDK: build, examples, console, current gaps |

## Quick start

```sh
# Install the emulator into $GOBIN (usually ~/go/bin)
go install github.com/akhenakh/rp2350sim/cmd/rp2350sim@latest

# Build any TinyGo program for the Pico 2
tinygo build -target=pico2 -o firmware.elf .

# Run it for three virtual seconds
rp2350sim -fw firmware.elf -seconds 3
```

Firmware console output goes to **stdout**; emulator diagnostics and the final
summary go to **stderr**, so `rp2350sim ... > firmware.log` captures only what
the firmware printed.

A minimal CI assertion:

```sh
printf 'status\n' | rp2350sim -fw firmware.elf -seconds 3 -stdin \
  -expect "status: ok" -stop-on-expect -quiet > firmware.log
```

See [ci.md](ci.md) for the full recipe.

## Supported targets at a glance

- **Raspberry Pi Pico 2** (RP2350A), TinyGo and **Pico SDK C**: GPIO, UART,
  SPI, I2C, PWM, ADC, PIO, USB CDC console (TinyGo), DMA, timers, multicore.
  See [c.md](c.md) for the C path and its current limits.
- **Pico 2 W**: a *virtual* Wi-Fi netdev backed by the host stack. The real
  CYW43439 radio and Bluetooth are not emulated; see [boards.md](boards.md).
- **Waveshare RP2350-GEEK**: ST7789 240×135 on SPI1.
- **Waveshare RP2350-POE-ETH**: W6300 Ethernet on SPI0.
- **Waveshare RP2350-ETH** (non-PoE): CH9120 UART-to-Ethernet on UART1.
- Breadboard variants: W5500 on SPI0, ST7789, WS2812B, UART over TCP.

## Design notes

- The CPU is Cortex-M33 (`thumbv8m.main`); stock TinyGo firmware is the
  contract, and stock Pico SDK C images boot through the same vector table. The
  RP2350 bootrom itself is skipped; a synthetic table answers the SDK's
  `rom_func_lookup` calls.
- Time is virtual (150 MHz cycles); the emulator runs faster than real time,
  so `-seconds` is virtual time and `-wall-timeout` is real time.
- The headless runner is the automation surface; the UI is a separate module.
- Hardware wiring and design contracts live in `.operator/specs/` for
  contributors; the documents here are for users.
