# rp2350sim

A Go emulator for the Raspberry Pi RP2350 (Pico 2 and derived) that runs unmodified
`tinygo build -target=pico2` firmware, with a desktop UI showing the board state.

Status: runs real TinyGo programs end to end — package initialisation, the task
scheduler, `time.Sleep`, GPIO, USB CDC `println`/panic output, UART, SPI with
DMA, PWM, ADC, I2C, PIO (with a WS2812B wire decoder), ST7789 displays, a
CST816D touch controller, a QMI8658 IMU, a microSD card model that can be
backed by a real host directory, and W5500 Ethernet, plus a W6300 driver for the PoE board. Stock **Pico SDK C**
firmware boots through the same vector table and runs its runtime, UART stdio,
GPIO, alarms and multicore; single-precision FPU is supported. The Cortex-M33
interpreter is checked against QEMU instruction by instruction.

## Layout

```
cmd/rp2350sim/      headless runner and debugging tools
cmd/simui/          Shirei desktop UI (separate module)
firmware/c/         Pico SDK C examples (CMake)
internal/m33/       ARMv8-M Mainline (Cortex-M33) interpreter
internal/rp2350/    chip: memory map, SIO, NVIC, timers, clocks, GPIO, USB,
                    UART, SPI, DMA, PWM, ADC, I2C
internal/devices/   external chips (ST7789, CST816D, QMI8658, SD, W5500/W6300)
internal/firmware/  UF2 / ELF / raw image loaders
drivers/w6300/      TinyGo netdev driver for the W6300 (separate module)
firmware/           example TinyGo programs (blinky, lcd, piotest, ws2812, w6300)
tools/              corpus generator, host Ethernet test server
Taskfile.yml        build/run/test tasks
```

## Tasks

The repository ships a [Taskfile](https://taskfile.dev) (`task` lists them).

| task | what it does |
|---|---|
| `task` | build the emulator and the blinky firmware |
| `task build` | build `build/rp2350sim` |
| `task install` | install `rp2350sim` into `$(go env GOPATH)/bin` |
| `task firmware:blinky` | TinyGo blinky ELF for the Pico 2 |
| `task firmware:blinky:uf2` | blinky UF2 for flashing a real board |
| `task run:blinky` | run blinky and trace GP25 for three virtual seconds |
| `task run:lcd` | run the ST7789 example in the emulator with the LCD attached |
| `task run:touchlcd` | run the Touch-LCD-2 example with a deterministic touch |
| `task touchlcd` | run the Touch-LCD-2 example and export its framebuffer |
| `task run:piotest` | run the PIO API checks (FIFOs, autopush/pull, IRQ, side-set) |
| `task run:cores` | build with `-scheduler=cores`, run a task on core 1, trace both pins |
| `task run:echo` | echo injected console input through USB CDC |
| `task run:ws2812` | run the WS2812B example and print the decoded LED colours |
| `task run:w6300` | run the W6300 example (TCP client, UDP, TCP server) against `tools/ethhost` |
| `task lcd` | build/run the ST7789 example and export `build/lcd.png` |
| `task ui:blinky` (`ui`) | desktop UI on the `pico2` board with the blinky firmware |
| `task ui:lcd` | desktop UI on the `geek` board with the ST7789 example |
| `task ui:touchlcd` | desktop UI on the `touch-lcd2` board |
| `task ui:ws2812` | desktop UI on the `pico2-ws2812` board |
| `task ui:w6300` | desktop UI on the `poe-eth` board with the W6300 example |
| `task run:ch9120` | CH9120 UART-to-Ethernet example against the host echo |
| `task ui:ch9120` | desktop UI on the `rp2350-eth` board with the CH9120 example |
| `task run:wifi` | virtual Wi-Fi TCP client against the host echo |
| `task ui:wifi` | desktop UI on the Pico 2 W (virtual Wi-Fi) with the netdev example |
| `task ui:scope` | scope demo with a 1 kHz sine on GP26 (`pico2-analog`) |
| `task ui:logic` | logic analyzer with the UART/I2C/SPI demo firmware |
| `task ui:serial` | UART1 bridged over TCP with an echo firmware |
| `task ui:png` | headless UI render of the LCD tab to `build/ui.png` |
| `task test` / `task test:qemu` | unit tests / QEMU differential corpus |
| `task lint` | go vet, golangci-lint (modernize) and go fix diff in every module |
| `task c:build` | build the Pico SDK C examples (needs `PICO_SDK_PATH`) |
| `task c:hello`, `c:blinky`, `c:float`, `c:multicore` | run the C examples |
| `task ci` | vet, unit tests, QEMU corpus, firmware smokes and a UI snapshot |
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
| `-eth` | attach a W5500 on SPI0 CS GP17 (stock TinyGo driver) |
| `-w6300` | attach a W6300 on SPI0 CS GP17 (PoE board) |
| `-ch9120` | attach a CH9120 on UART1 GP20/21 (RP2350-ETH board) |
| `-lcd-png path.png` | write the LCD framebuffer at exit |
| `-strict-mmio` | halt on the first unmapped peripheral access |
| `-unhandled` | print a summary of unmapped accesses at exit |
| `-trace N`, `-trace-from ADDR` | trace instructions |
| `-trace-last` | keep a ring of recent instructions for diagnostics |
| `-bp ADDR`, `-bp2 ADDR` | stop / log at an address |
| `-watch ADDR`, `-watch-val V`, `-watch-reg N` | write and register logging |
| `-poke ADDR=VAL` | write memory before starting |
| `-peek ADDR` | print a memory word at exit |
| `-ws2812 GPIO` | decode a WS2812B strip driven on that pin |
| `-input TEXT` | send bytes to USB CDC and UART0 after boot |
| `-sample N` | print the PC in 10M-cycle chunks |
| `-cpuprofile PATH` | write a CPU profile |
| `-input-file FILE`, `-input-at D`, `-stdin` | scripted console input |
| `-expect TEXT` (repeatable), `-stop-on-expect` | assert console output and fail the run |
| `-wall-timeout D`, `-summary-file PATH` | bound real time / write a JSON summary |
| `-allow-halt`, `-quiet` | tolerate a halted core / suppress the informational summary |
| `-log-format auto\|text\|json`, `-log-level LEVEL` | diagnostic logging (default: JSON when piped or `CI`, text on a terminal) |

**Output contract:** firmware console goes to **stdout**, all emulator
diagnostics go to **stderr** as structured `slog` records. Exit codes: 0
success, 1 load error, 2 usage, 3 halt/fault, 4 missing expectation, 5 wall
timeout.

Per-stream debug switches (JSON is the default for a piped run):
`RP2350SIM_SPI_LOG`, `RP2350SIM_DMA_LOG`, `RP2350SIM_LCD_LOG`,
`RP2350SIM_USB_LOG`, `RP2350SIM_ETH_LOG`, `RP2350SIM_W6300_LOG`,
`RP2350SIM_CH9120_LOG`, `RP2350SIM_PIO_LOG`.

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

`-w6300` attaches the W6300 instead, the controller actually fitted to the
RP2350-POE-ETH. Its TinyGo driver lives in `drivers/w6300` (a separate module so
the firmware build pulls only the driver), and `firmware/w6300` exercises a TCP
client, a UDP datagram and a TCP server. `task run:w6300` starts
`tools/ethhost` (echo, UDP sink, server check) and runs the firmware against it.

The non-PoE **RP2350-ETH** uses a CH9120 UART-to-Ethernet module instead:
UART1 on GP21 (TX) / GP20 (RX), CFG0 GP18, RST GP19 and TCPCS GP17. It is a
transparent serial bridge configured with the CH9120 command set; the
`drivers/ch9120` module configures TCP/UDP client/server mode and then
`Read`/`Write` exchange raw socket bytes. `task run:ch9120` connects to the host
echo server, and the UI's Ethernet tab shows its state and events.

### Pico 2 W (virtual Wi-Fi)

The Pico 2 W's Infineon CYW43439 is **not** modelled: its gSPI protocol,
firmware download and Bluetooth stack are out of the emulator's scope. Instead
the `pico2w` board exposes a **virtual Wi-Fi netdev** backed by the host
network stack, reusing the CH9120 serial-module model. Firmware uses
`drivers/ch9120`'s `NetDevice` (a one-socket `netdev`) and calls
`netdev.UseNetdev`, so TinyGo's `net` package works normally:

```go
nd := ch9120.NewNetDevice(dev, base)
netdev.UseNetdev(nd)
conn, _ := net.DialTCP("tcp", nil, &net.TCPAddr{IP: net.IPv4(127,0,0,1), Port: 9000})
```

`task run:wifi` shows `wifi echo: hello wifi`; the UI's Wi-Fi tab shows the
module state. Bluetooth is not emulated; see `.operator/specs/hardware.md`.

The W6300 frame is `instruction, address hi, address lo, dummy, data`; its
register map and status/interrupt registers differ from the W5500, and it has
32 KB of TX and RX buffer per direction. UDP firmware must call the driver's
`Socket`/`Bind`/`Connect`/`Send` directly: TinyGo's `net.DialUDP` loses the
destination address on its second netdev call in this build (TCP is fine).

See `.operator/specs/hardware.md` for the board wiring and the remaining gaps.

## Desktop UI

```sh
cd cmd/simui
go run . --board geek ../../firmware.elf
```

The UI starts with a board picker. The list comes from `cmd/simui/boards.yaml` (or
`RP2350SIM_BOARDS=/path/to/boards.yaml`), and each entry declares its hardware:
an ST7789 LCD on an SPI bus with its CS/DC/RST pins, a W5500/W6300 controller,
a WS2812B data pin, scope channels, generated analog signals, or logic-analyzer
protocols. `go run . --list-boards` prints the ids.

Selecting a board attaches only those devices to the emulated chip and builds
the tabs accordingly:

- **Board** (always): the Pico 2 header diagram and the live GPIO table.
- **LCD**: only when the board has an LCD; shows the framebuffer.
- **Ethernet**: only for W5500/W6300 boards; shows the socket log.
- **LEDs**: only for WS2812B boards; shows the last latched colours.
- **Scope** (always): an oscilloscope with up to 8 pin traces in different
  colours, a 1 MHz sampler and a ~16 ms window. Click header pins to add or
  remove traces. **Zoom −/+** changes the time base from 1× to 64×;
  **Range −/+** selects the full-scale voltage (0.5, 1, 1.65, 2, 3.3 or 5 V);
  **Reset** restores 1× and 3.3 V. The view grows with the window width.
  `analog:` board entries drive a generated waveform (the `pico2-analog` board
  puts a 1 kHz sine on GP26).
- **Logic** (always): a logic analyzer. Board `logic:` blocks declare UART,
  I2C and SPI pins/baud; hardware transfers are tapped from the peripheral
  models (so high-speed buses do not need pin-level sampling), and PIO or
  bit-banged traffic falls back to the pin trace. Decoded UART bytes, I2C
  address/data/ACK phases and SPI bytes are listed under synthesised
  waveforms; USB CDC transfers are listed as well. **Trigger** arms on the next
  UART, I2C, SPI or any protocol event and freezes the capture (and optionally
  pauses the machine); **Rearm** waits for the next event.
- **Console** (always): USB CDC + UART. Click it to give it keyboard focus;
  typed text and Enter/Backspace/Tab/arrows are forwarded to the firmware.

The toolbar shows the selected board, a **Board…** button to return to the
picker, the firmware path field, **Open…** (OS dialog), run/pause and reset.
`--board ID` skips the picker, `--tab board|lcd|eth|ws|scope|logic|console`
chooses the initial tab, and `--width`/`--height` set the window size. For
headless scope snapshots, `--scope-zoom` and `--scope-range` set the initial
time base and voltage range. `--png out.png --run 0.6 firmware.elf` renders a
headless snapshot after advancing virtual time.

## Serial over TCP

A board can expose a hardware UART over TCP:

```yaml
serial:
  - uart: 1          # UART1, default pins GP4/GP5
    # listen: 127.0.0.1:0  (the default: a kernel-chosen free port)
```

The Console tab shows the assigned address (for example
`UART1 tcp 127.0.0.1:39479 · listening`). Connect with `nc 127.0.0.1 39479`;
bytes go into the UART receive FIFO and everything the firmware transmits
comes back on the same connection. The listener is kept across firmware
resets, so the port does not change while the UI is open.

`task ui:serial` runs the `pico2-serial` board with `firmware/serialbridge`,
which echoes UART1. The headless runner has the same feature:
`-serial-tcp 1:127.0.0.1:0` prints the chosen address on stderr.
`tools/serialcheck` is the client used by the smoke test.

## Documentation

The user guide lives in [`docs/`](docs/README.md):

- [`docs/cli.md`](docs/cli.md) — every flag, output contract and exit code
- [`docs/ci.md`](docs/ci.md) — running firmware in CI, input, expectations, artifacts
- [`docs/tinygo.md`](docs/tinygo.md) — install, TinyGo builds, drop-in Taskfile/Makefile targets
- [`docs/ui.md`](docs/ui.md) — board picker, `boards.yaml`, tabs, scope, logic analyzer, serial bridge
- [`docs/boards.md`](docs/boards.md) — supported boards, wiring and limitations
- [`docs/c.md`](docs/c.md) — C firmware with the Pico SDK, examples and current gaps

## Tests

```sh
go test ./...
```

`task ci` is the full gate: `go vet`, the unit tests, the QEMU differential
corpus (skipped when `qemu-system-arm`/`arm-none-eabi-gdb` are missing), every
firmware example, and `tools/smoke.sh`, which asserts on observable behaviour
(GPIO edges, console echo, PIO checks, WS2812 colours, multicore launch, LCD
pixels, W6300 TCP/UDP against `tools/ethhost`) plus a headless UI snapshot
checked with `tools/pngcheck`. `.github/workflows/ci.yml` runs the same target.

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
- Missing peripherals: USB host, PSRAM.
- The default `pico2` build does not launch core 1. With
  `tinygo build -scheduler=cores` the runtime starts core 1 through the SIO
  FIFO; both cores then run tasks (see `firmware/cores` and `task run:cores`).
- Time is virtual and one instruction is one cycle; throughput is about 160M
  cycles/s (faster than the emulated 150 MHz clock), so firmware runs at or
  above real time. `-cpuprofile` writes a Go CPU profile.
- Hardware notes, board wiring and design contracts live in `.operator/specs/`.

## References

- RP2350 datasheet; `tinygo/src/device/rp/rp2350.go` register definitions
- TinyGo `machine` and `runtime` packages for the boot contract
- `qemu-system-arm -M mps2-an505` as the Cortex-M33 instruction oracle
