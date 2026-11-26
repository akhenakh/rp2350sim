# Browser build (WebAssembly)

`task wasm:build` compiles the **desktop UI** to WebAssembly: the same
`cmd/simui` sources, the same Shirei front end, the same emulator core. Nothing
is forked or simplified for the web; the build differs only in what the page can
give it (a firmware fetched over HTTP) and in which host-facing devices are
attached.

The site is produced by Shirei's `shirei_web` tool, which writes
`index.html`, `wasm_exec.js` and `main.wasm`.

## Web demo

A live build is published with the documentation:

- **https://akhenakh.github.io/rp2350sim/demo/** — the default demo opens the
  tinychess mate-in-1 game on the Touch-LCD-2.
- TinyChess: [demo/?board=touch-lcd2&fw=firmware/tinychess.elf&tab=lcd](https://akhenakh.github.io/rp2350sim/demo/?board=touch-lcd2&fw=firmware/tinychess.elf&tab=lcd)
- GEEK LCD: [demo/?board=geek&fw=firmware/lcd.elf&tab=lcd](https://akhenakh.github.io/rp2350sim/demo/?board=geek&fw=firmware/lcd.elf&tab=lcd)

The GitHub Pages job builds it with `task docs:demo`, which runs
`task wasm:build` and copies the result into `site/public/demo`. It is the same
window you get from `task ui:touchlcd`, including the board picker, LCD,
sensors, scope, logic analyzer and console.

**Run your own firmware**: press **Open…** (the browser's file picker) or drop a
`.elf`, `.uf2` or `.bin` anywhere on the page. The image is parsed in the page
and installed with `Emu.LoadBytes`; **Load** restarts the last one. The `?fw=`
parameter is only for links to the bundled demos.

Limitations of the demo build:

- **No filesystem**: firmware arrives from the picker, a drop, or `?fw=`. There
  is no flash backing file. The bundled tinychess image is packed without
  DWARF, so its Debug tab shows registers and memory, and the Profile tab falls
  back to 256-byte PC buckets. A picked/dropped ELF is parsed in memory and
  keeps its symbols and DWARF; UF2/raw images use the same PC-bucket fallback.
  See [profiler.md](profiler.md) for the Profile tab.
- **No host devices**: no Ethernet/Wi-Fi controllers, no Bus Pirate or UART TCP
  bridges, no GDB server, no SD directory backing (the built-in FAT16 card is
  used) and no Build drawer.
- **One thread**: the emulator shares the page's thread and yields to the
  browser every few milliseconds, so the UI stays responsive at the cost of
  throughput.

Speed difference: the browser is much slower than the desktop for compute-bound
firmware, because Go's WebAssembly backend runs the interpreter at about 3.2M
emulated cycles per second against ~36M natively (same benchmark):

| demo | desktop | browser |
|---|---|---|
| Touch-LCD-2 (`touchlcd.elf`) | ~1.0× real time | ~0.15× |
| GEEK LCD (`lcd.elf`) | ~0.3× | ~0.04× |
| sleep-heavy firmware (blinky and friends) | 1.0× | ~1.0× |

The status line shows the achieved ratio and turns the virtual clock red when it
is below 0.95×. The gap is the Go wasm backend and the single page thread, not
the UI: the same firmware runs at full speed in the desktop window.

## Build and serve

```sh
task wasm:build    # build/web: page, wasm, and the demo firmware
task wasm:serve    # build and serve http://127.0.0.1:8787
```

`wasm:build` copies `build/lcd.elf` and `build/touchlcd.elf` into
`build/web/firmware/`, and bundles the prebuilt tinychess image from
`site/static/demo/firmware/tinychess.elf`. The tinychess sources live in
[github.com/akhenakh/tinychess](https://github.com/akhenakh/tinychess) and are
not vendored here; `tools/pack_demo_elf.py` turns its debug ELF into the
single-segment flash image the browser downloads (~190 KB).

The page reads its configuration from the query string:

| parameter | default | meaning |
|---|---|---|
| `board` | `touch-lcd2` | board id from `boards.yaml` |
| `fw` | `firmware/tinychess.elf` | firmware URL, relative to the page |
| `tab` | `lcd` | initial tab, same names as `--tab` |

```
http://127.0.0.1:8787/
http://127.0.0.1:8787/?board=geek&fw=firmware/lcd.elf&tab=lcd
```

Any static server works. `shirei_web -serve` additionally sets the COOP/COEP
headers Shirei's audio worklet wants; the generated `build/web/.headers` carries
them for other hosts.

## What the browser build cannot do

- **Firmware comes from the page.** `Emu.LoadBytes` installs the picked,
  dropped or fetched image; the **Open…** button uses the browser's file
  picker because there is no filesystem to list. There is no flash backing
  file. ELF bytes are parsed with `debug.LoadBytes`, so a picked ELF keeps its
  symbols and DWARF; the packed tinychess demo has none, so its Debug tab shows
  registers and memory and Profile shows PC buckets.
- **Scripted device plugins work in the page.** `.lua` and `.wasm` device
  files from `plugins/examples/` are fetched by URL and instantiated by the
  browser's native WebAssembly engine; a custom board entry can point `url:`
  at another same-origin plugin. See [device plugins](plugins.md).
- **Host devices are stripped** before boot by `webPrepareBoard`: SD directory
  backing (the built-in FAT16 image is used), UART/I2C/SPI TCP bridges, the
  W5500/W6300/CH9120 controllers, the GDB server and the Build drawer. Boards
  that need sockets (Ethernet, Wi-Fi, `pico2-bus`) still render, but their
  network devices are absent.
- **One thread.** The emulator loop shares the page's thread, so it steps in
  larger chunks and returns to the browser every few milliseconds to keep the
  UI painting.

## Performance notes

Go's WebAssembly backend runs the interpreter at roughly **3.2 million emulated
cycles per second** in Chrome (measured with the `internal/rp2350` two
instruction loop after the hot-path pass), against about 36 million per second
natively. The table above shows what that means per demo.

`task wasm:build` runs `wasm-opt -O3` over the module when binaryen is
installed: about 9% smaller and ~10% faster. Without `wasm-opt` the plain Go
module is served. A faster core hot path, or TinyGo for the wasm build, are the
ways to close the gap; the current stdlib use (`os/exec`, `net`,
`debug/dwarf`, `image/png`) rules out TinyGo.

## Building by hand

```sh
cd cmd/simui
go run go.hasen.dev/shirei/cmd/shirei_web -webcompiler=go -o ../../build/web .
```

`-webcompiler=go` is explicit: the auto mode prefers TinyGo when installed, but
the UI pulls in standard library packages TinyGo does not support (`os/exec`,
`net`, `debug/dwarf`, `image/png`), so the Go compiler is the supported path.
