# rp2350sim

A Go emulator for the Raspberry Pi RP2350 (Pico 2) that runs unmodified
`tinygo build -target=pico2` firmware, with a Shirei desktop UI showing the
board state.

Status: runs real TinyGo programs end to end — package initialisation, the task
scheduler, `time.Sleep`, GPIO, USB CDC `println`/panic output, UART, SPI with
DMA, PWM, ADC and an ST7789 display. The Cortex-M33 interpreter is checked
against QEMU instruction by instruction.

## Layout

```
cmd/rp2350sim/      headless runner and debugging tools
ui/                 Shirei desktop UI (separate module)
internal/m33/       ARMv8-M Mainline (Cortex-M33) interpreter
internal/rp2350/    chip: memory map, SIO, NVIC, timers, clocks, GPIO, USB,
                    UART, SPI, DMA, PWM, ADC, I2C
internal/devices/   external chips (ST7789 display, W5500 Ethernet)
internal/firmware/  UF2 / ELF / raw image loaders
firmware/           example TinyGo programs (blinky, lcd)
tools/              corpus generator
Taskfile.yml        build/run/test tasks
```

## Tasks

The repository ships a [Taskfile](https://taskfile.dev) (`task` lists them).

| task | what it does |
|---|---|
| `task` | build the emulator and the blinky firmware |
| `task build` | build `build/rp2350sim` |
| `task firmware:blinky` | TinyGo blinky ELF for the Pico 2 |
| `task firmware:blinky:uf2` | blinky UF2 for flashing a real board |
| `task run:blinky` | run blinky and trace GP25 for three virtual seconds |
| `task run:lcd` | run the ST7789 example in the emulator with the LCD attached |
| `task lcd` | build/run the ST7789 example and export `build/lcd.png` |
| `task ui` | desktop UI with the blinky firmware loaded |
| `task ui:png` | headless UI render to `build/ui.png` |
| `task test` / `task test:qemu` | unit tests / QEMU differential corpus |
| `task clean` | remove `build/` |

## Running firmware

```sh
go run ./cmd/rp2350sim -fw firmware.elf -seconds 1 -trace-pins 25
# or, building and running the bundled blinky example:
task run:blinky
```

The default USB console and UART both write to stdout, so `println` output
appears directly.

Useful flags:

| flag | meaning |
|---|---|
| `-fw` | `.uf2`, `.elf` or raw binary |
| `-seconds` | virtual seconds to run (150 MHz master clock) |
| `-steps` | cycle budget instead of `-seconds` |
| `-trace-pins 25,15` | log GPIO level changes |
| `-lcd` | attach an ST7789 on SPI1 (RP2350-GEEK wiring) |
| `-lcd-png path.png` | write the LCD framebuffer at exit |
| `-strict-mmio` | halt on the first unmapped peripheral access |
| `-unhandled` | print a summary of unmapped accesses at exit |
| `-trace N`, `-trace-from ADDR` | trace instructions |
| `-trace-last` | keep a ring of recent instructions for diagnostics |
| `-bp ADDR`, `-bp2 ADDR` | stop / log at an address |
| `-watch ADDR`, `-watch-val V`, `-watch-reg N` | write and register logging |
| `-poke ADDR=VAL` | write memory before starting |
| `-sample N` | print the PC in 10M-cycle chunks |
| `-cpuprofile PATH` | write a CPU profile |

Diagnostics environment variables: `RP2350SIM_SPI_LOG`, `RP2350SIM_DMA_LOG`,
`RP2350SIM_LCD_LOG`, `RP2350SIM_USB_LOG`, `RP2350SIM_ETH_LOG`.

## Ethernet

`-eth` attaches a W5500 on SPI0 (CS GP17), the POE-ETH wiring. The emulated
hardware sockets are backed by the host network, so TinyGo's stock
`tinygo.org/x/drivers/w5500` netdev driver works unchanged and can talk to
servers running on this machine.

TinyGo's `net.Dial("tcp", "host:port")` needs `LookupPort`, which is not
implemented in this TinyGo build; construct the address explicitly:

```go
conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: 8080})
```

See `.operator/specs/hardware.md` for the W6300 difference and the roadmap.

## Desktop UI

```sh
cd ui
go run . ../firmware.elf
```

The window shows live GPIO state (function, direction, level), the ST7789
framebuffer, and the USB/UART console. **Open…** loads a firmware image through
the OS file dialog (native on macOS/Windows, zenity/kdialog on Linux); the path
field can also be edited and loaded directly. The toolbar also pauses/resumes
and resets. `--png out.png --run 0.6 firmware.elf` renders a
headless snapshot after advancing virtual time.

## Tests

```sh
go test ./...
```

The decoder regression gate is a differential harness that runs a generated
corpus of instructions (1344 cases) in QEMU's Cortex-M33 and compares registers,
SP, PC and flags with this interpreter:

```sh
go test -tags qemu ./internal/m33 -run TestQEMUInstructions
```

It needs `qemu-system-arm` and `arm-none-eabi-gdb`. `tools/gen_corpus.py`
regenerates the corpus with `llvm-mc -triple=thumbv8m.main` encodings.

## Scope and known gaps

- The emulated CPU is the Cortex-M33 (thumbv8m.main); there is no RISC-V path.
  TinyGo `-target=pico2` emits Arm, and that is the contract.
- Missing peripherals: PIO, I2C, W6300 Ethernet, USB host, PSRAM.
- The TinyGo build under test does not launch core 1, so the emulator runs
  single-core in practice.
- Time is virtual and one instruction is one cycle; throughput is about 160M
  cycles/s (faster than the emulated 150 MHz clock), so firmware runs at or
  above real time. `-cpuprofile` writes a Go CPU profile.
- Hardware notes, board wiring and design contracts live in `.operator/specs/`.

## References

- RP2350 datasheet; `tinygo/src/device/rp/rp2350.go` register definitions
- TinyGo `machine` and `runtime` packages for the boot contract
- `qemu-system-arm -M mps2-an505` as the Cortex-M33 instruction oracle
