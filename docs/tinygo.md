# Using rp2350sim with TinyGo

`rp2350sim` runs stock `tinygo build -target=pico2` ELF or UF2 images. There is
no special build flag, runtime shim or patched TinyGo: if it runs on a Pico 2,
it runs in the emulator.

## Install the emulator

Prebuilt archives for Linux, macOS and Windows (with both `rp2350sim` and
`rp2350sim-ui`) are on the
[GitHub Releases page](https://github.com/akhenakh/rp2350sim/releases). Unpack
the archive and put both binaries on `PATH`.

Or install from source:

```sh
go install github.com/akhenakh/rp2350sim/cmd/rp2350sim@latest
```

This installs the host binary (built with the normal Go toolchain); TinyGo is
only needed to build firmware. From a checkout of this repository use:

```sh
task install        # go install ./cmd/rp2350sim
# or
go install ./cmd/rp2350sim
```

Make sure `$(go env GOPATH)/bin` is on `PATH`.

## Build and run firmware

```sh
tinygo build -target=pico2 -o build/firmware.elf .
rp2350sim -fw build/firmware.elf -seconds 3
```

A `.uf2` straight from a release also works:

```sh
rp2350sim -fw firmware.uf2 -seconds 3
```

Pico 2 W firmware is built with the same `pico2` target in this emulator; the
board's virtual Wi-Fi is provided by the emulator, not by TinyGo's radio
support. See [boards.md](boards.md).

## Drop-in Taskfile

Copy or include `tools/Taskfile.pico2sim.yml`:

```yaml
version: '3'

includes:
  pico2sim:
    taskfile: ../rp2350sim/tools/Taskfile.pico2sim.yml

# Optional defaults for the included tasks
vars:
  FIRMWARE: build/firmware.elf
  EMU: rp2350sim
```

Then:

```sh
task pico2sim:build
task pico2sim:run SECONDS=5
task pico2sim:ci SECONDS=5 EXPECT="status: ok"
```

The include uses `TINYGO`, `TARGET`, `FIRMWARE`, `EMU`, `SECONDS`, `EXPECT` and
`INPUT_FILE` variables (environment or CLI). `pico2sim:ci` writes
`<firmware>.json` and fails unless `EXPECT` appears in the console output.

## Drop-in Makefile

```make
include ../rp2350sim/tools/pico2sim.mk
```

Targets:

```sh
make pico2-build
make pico2-run RP2350SIM_SECONDS=5
make pico2-ci
```

Variables: `TINYGO`, `PICO2_TARGET`, `PICO2_FIRMWARE`, `RP2350SIM`,
`RP2350SIM_SECONDS`.

## A development loop

TinyGo has no watch mode. A simple loop watches source files, rebuilds, and
runs; `watchexec` and `entr` both work:

```sh
watchexec -e go,toml 'tinygo build -target=pico2 -o build/firmware.elf .'
```

Run the emulator separately (or the UI for interactive inspection):

```sh
rp2350sim -fw build/firmware.elf -seconds 5 -trace-pins 25
```

The desktop UI has **Open…** and **Load** buttons; pick the freshly built ELF
after each rebuild. See [ui.md](ui.md).

## Headless UI snapshots

The UI module can render a deterministic frame, which is useful for design
reviews or CI artifacts:

```sh
rp2350sim-ui --board geek --tab lcd --run 0.6 --png ui.png build/firmware.elf
```

## Choosing hardware

The headless runner attaches devices with flags (`-lcd`, `-eth`, `-w6300`,
`-ch9120`, `-ws2812`); the UI uses board definitions in `cmd/simui/boards.yaml`. Use
the flag that matches the wiring your firmware expects. The Pico 2 itself needs
no flags.

## Firmware formats

- **ELF** (what `tinygo build` produces without `-o` extension changes): the
  recommended format, includes segment addresses and the entry point.
- **UF2**: family ID `0xe48bff59` is validated; convenient for release
  artifacts.
- **Raw binary**: loaded at the flash base; the first two words are the initial
  SP and PC.

See [cli.md](cli.md) for every runtime flag.
