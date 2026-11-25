# Browser build (WebAssembly)

`task wasm:build` compiles the **desktop UI** to WebAssembly: the same
`cmd/simui` sources, the same Shirei front end, the same emulator core. Nothing
is forked or simplified for the web; the build differs only in what the page can
give it (a firmware fetched over HTTP) and in which host-facing devices are
attached.

The site is produced by Shirei's `shirei_web` tool, which writes
`index.html`, `wasm_exec.js` and `main.wasm`.

## Build and serve

```sh
task wasm:build    # build/web: page, wasm, and the demo firmware
task wasm:serve    # rebuild and serve http://127.0.0.1:8787
```

`wasm:build` copies `build/lcd.elf` and `build/touchlcd.elf` into
`build/web/firmware/`. The page reads its configuration from the query string:

| parameter | default | meaning |
|---|---|---|
| `board` | `geek` | board id from `boards.yaml` |
| `fw` | `firmware/lcd.elf` | firmware URL, relative to the page |
| `tab` | `board` | initial tab, same names as `--tab` |

```
http://127.0.0.1:8787/?board=touch-lcd2&fw=firmware/touchlcd.elf&tab=lcd
```

Serving the directory with any static server works; `task wasm:serve` uses
`shirei_web -serve`, which also sets the COOP/COEP headers Shirei's audio
worklet wants. Other servers can copy `build/web/.headers`.

## What the browser build cannot do

- **Firmware comes over HTTP.** `Emu.LoadBytes` installs the fetched image;
  there is no local file picker (the **Open…** dialog has no filesystem to
  list), no flash backing file and no DWARF symbol load, so the Debug tab shows
  registers and memory but no named variables.
- **Host devices are stripped** before boot by `webPrepareBoard`: SD directory
  backing (the built-in FAT16 image is used), UART/I2C/SPI TCP bridges, the
  W5500/W6300/CH9120 controllers, the GDB server and the Build drawer. Boards
  that need sockets (Ethernet, Wi-Fi, `pico2-bus`) still render, but their
  network devices are absent.
- **One thread.** The emulator loop shares the page's thread, so it steps in
  larger chunks and returns to the browser every few milliseconds to keep the
  UI painting.

## Performance

Go's WebAssembly backend runs the interpreter at roughly **3.2 million emulated
cycles per second** in Chrome (measured with the `internal/rp2350` two
instruction loop after the hot-path pass below), against about 36 million per
second natively. Firmware that is compute-bound therefore runs well below real
time:

| demo | speed in the browser |
|---|---|
| Touch-LCD-2 (`touchlcd.elf`) | ~0.15× |
| GEEK LCD (`lcd.elf`) | ~0.04× |
| sleep-heavy firmware (blinky and friends) | ~1× (idle fast-forward is unaffected) |

The status line shows the achieved ratio and turns the virtual clock red when it
is below 0.95×. This is the Go wasm backend, not the UI: the same firmware runs
at 1.0× in the desktop window. A faster core hot path, or TinyGo for the wasm
build, would be the ways to close the gap.

`task wasm:build` runs `wasm-opt -O3` over the module when binaryen is
installed: about 9% smaller and ~10% faster. Without `wasm-opt` the plain Go
module is served.

## Building by hand

```sh
cd cmd/simui
go run go.hasen.dev/shirei/cmd/shirei_web -webcompiler=go -o ../../build/web .
```

`-webcompiler=go` is explicit: the auto mode prefers TinyGo when installed, but
the UI pulls in standard library packages TinyGo does not support (`os/exec`,
`net`, `debug/dwarf`, `image/png`), so the Go compiler is the supported path.
