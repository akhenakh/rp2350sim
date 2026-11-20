# rp2350sim documentation

`rp2350sim` runs unmodified `tinygo build -target=pico2` and stock Pico SDK C
firmware: a Cortex-M33 interpreter, the RP2350 chip peripherals, and models for
the boards and external chips (ST7789 LCD, W5500/W6300/CH9120 Ethernet,
WS2812B, virtual Wi-Fi). A Shirei desktop UI adds board selection, live GPIO, a
scope, a logic analyzer and Bus Pirate terminals.

![Board picker](images/picker.png)

This directory is the user guide. The same pages are rendered with Hugo and
the [Hextra](https://themes.gohugo.io/themes/hextra/) theme at
<https://akhenakh.github.io/rp2350sim/>; preview locally with `task docs:serve`.

| Document | Contents |
|---|---|
| [cli.md](cli.md) | every command-line flag, output contract, exit codes, examples |
| [peripherals.md](peripherals.md) | each peripheral from both the CLI and the UI, with reference firmware and tasks |
| [ui.md](ui.md) | the desktop UI: board picker, `boards.yaml`, tabs, scope, logic analyzer, Bus Pirate terminals |
| [debug.md](debug.md) | the Debug tab, symbols, breakpoints, watchpoints, memory and variables |
| [boards.md](boards.md) | supported boards, wiring, peripherals and current limitations |
| [ci.md](ci.md) | running firmware in CI, injecting input, asserting output and signals, capturing artifacts |
| [tinygo.md](tinygo.md) | installing, building with TinyGo, drop-in Taskfile/Makefile targets, dev loop |
| [c.md](c.md) | C firmware with the Pico SDK: build, examples, console, current gaps |
| [release.md](release.md) | release archives, GoReleaser, supported platforms |

## Install

Prebuilt archives are attached to
[GitHub Releases](https://github.com/akhenakh/rp2350sim/releases). Each archive
contains `rp2350sim` and `rp2350sim-ui` (`.exe` on Windows), `LICENSE`,
`README.md` and this `docs/` directory.

| platform | archive |
|---|---|
| Linux (`amd64`, `arm64`) | `rp2350sim_<version>_linux_<arch>.tar.gz` |
| macOS (`amd64`, `arm64`) | `rp2350sim_<version>_darwin_<arch>.tar.gz` |
| Windows (`amd64`, `arm64`) | `rp2350sim_<version>_windows_<arch>.zip` |
| checksums | `rp2350sim_<version>_checksums.txt` |

Both programs report their build with `-version`. To install from source
instead:

```sh
go install github.com/akhenakh/rp2350sim/cmd/rp2350sim@latest
```

## Quick start

```sh
# Build any TinyGo program for the Pico 2
tinygo build -target=pico2 -o firmware.elf .

# Run it for three virtual seconds
rp2350sim -fw firmware.elf -seconds 3

# Or inspect it in the UI
rp2350sim-ui --board pico2 firmware.elf
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
  SPI, I2C, PWM, ADC, PIO, USB CDC console (TinyGo), DMA, timers, multicore,
  OTP and optional QSPI PSRAM on QMI CS1.
  See [c.md](c.md) for the C path and its current limits.
- **Pico 2 W**: a *virtual* Wi-Fi netdev backed by the host stack. The real
  CYW43439 radio and Bluetooth are not emulated; see [boards.md](boards.md).
- **Pico 2 + PSRAM**: optional 1–16 MiB QSPI device on QMI CS1, with a
  configurable access latency; see [boards.md](boards.md#psram).
- **Waveshare RP2350-GEEK**: ST7789 240×135, I2C header and microSD.
- **Waveshare RP2350-Touch-LCD-2**: 240×320 touch LCD, CST816D, QMI8658,
  battery ADC and microSD.
- **Waveshare RP2350-POE-ETH**: W6300 Ethernet on SPI0.
- **Waveshare RP2350-ETH** (non-PoE): CH9120 UART-to-Ethernet on UART1.
- Breadboard variants: W5500 on SPI0, ST7789, WS2812B, UART over TCP and
  remote I2C/SPI devices over Bus Pirate tokens.

## Design notes

- The CPU is Cortex-M33 (`thumbv8m.main`); stock TinyGo firmware is the
  contract, and stock Pico SDK C images boot through the same vector table. The
  RP2350 bootrom itself is skipped; a synthetic table answers the SDK's
  `rom_func_lookup` calls.
- Time is virtual (150 MHz cycles); the emulator runs faster than real time,
  so `-seconds` is virtual time and `-wall-timeout` is real time.
- The headless runner is the automation surface; the UI is a separate module.
- ELF debug information drives the Debug tab and symbol breakpoints; UF2/raw
  images fall back to address breakpoints.
- Hardware wiring and design contracts live in `.operator/specs/` for
  contributors; the documents here are for users.
