# rp2350sim

rp2350sim is a Raspberry Pi RP2350 (Pico 2 and compatible boards) emulator for developing, testing, and debugging firmware on a desktop without hardware. It runs unmodified Pico SDK C and `tinygo build -target=pico2` programs, including RTOS firmware such as Zephyr, with a live UI for the emulated board and its peripherals.


Status: runs real programs end to end, package initialisation, the task
scheduler, `time.Sleep`, GPIO, USB CDC `println`/panic output, USB HID keyboard/mouse/gamepad reports, UART, SPI with DMA, PWM, ADC, I2C, PIO (with a WS2812B wire decoder), ST7789 displays, a CST816D touch controller, a QMI8658 IMU, a microSD card model that can be backed by a real host directory, W5500 Ethernet, plus a W6300 driver for the PoE board, the OTP array (fuses, Hamming ECC, page locks, bootrom access), and optional QSPI PSRAM (1-16 MiB on QMI CS1, with configurable access latency).
Stock **Pico SDK C**
firmware boots through the same vector table and runs its runtime, UART stdio, GPIO, alarms and multicore; single-precision FPU is supported. The Cortex-M33 interpreter is checked against QEMU instruction by instruction.

Fully tested on Linux & Mac OSX. Windows binaries are built and the network
models use portable Go net, but the bash smoke suite is not run on Windows.

## Installation

On macOS Apple Silicon or Intel, install both programs with
[Homebrew](https://brew.sh):

```sh
brew install akhenakh/tap/rp2350sim
```

On Arch-based Linux, install from the AUR with `yay` or `paru`:

```sh
yay -S rp2350sim
# or
paru -S rp2350sim
```

Prebuilt binaries are attached to [GitHub Releases](https://github.com/akhenakh/rp2350sim/releases).

### Verifying a release

Release archives and the checksum file carry [build provenance](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
signed by this repository's release workflow. Check a downloaded archive with:

```sh
gh attestation verify rp2350sim_<version>_linux_amd64.tar.gz \
  --repo akhenakh/rp2350sim
```

## Use of AI
This project was created using AI, cross checked against existing implementations, unit tested and smoke tested , CI guards.  
Your best bet is to try and see if it's running your code properly, send bug reports if you want the tool to improve.


## Running firmware

`firmware.elf` can be a TinyGo or Pico SDK C image; both use the same runner.

```sh
rp2350sim -fw firmware.elf -seconds 1 -trace-pins 25
# or, building and running the bundled TinyGo blinky example:
task run:blinky
# or a Pico SDK C example:
task c:hello
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
| `-sd`, `-sd-dir PATH`, `-sd-image PATH` | attach a microSD card or back it with a host directory / raw image |
| `-touch-lcd2`, `-touch x,y`, `-touch-at D` | Touch-LCD-2 devices and a deterministic tap |
| `-sensors` | log touch, IMU, battery, backlight and card state at exit |
| `-serial-tcp 1:127.0.0.1:0` | expose UART1 as a TCP server |
| `-strict-mmio` | halt on the first unmapped peripheral access |
| `-unhandled` | print a summary of unmapped accesses at exit |
| `-trace N`, `-trace-from ADDR` | trace instructions |
| `-trace-last` | keep a ring of recent instructions for diagnostics |
| `-bp ADDR\|SYMBOL`, `-bp2 ADDR\|SYMBOL` | log at an address or ELF symbol |
| `-watch ADDR\|SYMBOL`, `-watch-val V`, `-watch-reg N` | write and register logging |
| `-poke ADDR=VAL` | write memory before starting |
| `-peek ADDR` | print a memory word at exit |
| `-ws2812 GPIO` | decode a WS2812B strip driven on that pin |
| `-otp PATH`, `-otp-dump PATH` | seed / write back the OTP array (JSON) |
| `-psram MiB`, `-psram-latency N`, `-psram-cs GPIO` | attach QSPI PSRAM on QMI CS1 and set its per-access stall |
| `-flash PATH`, `-flash-out PATH` | load / write back the raw 16 MiB flash image |
| `-logic-pins 0,1`, `-logic-out PATH` | capture a logic-analyzer trace (UART/I2C/SPI taps plus pin samples) |
| `-i2c-tcp 0:0x50@127.0.0.1:0` | expose I2C targets as Bus Pirate TCP devices |
| `-spi-tcp 0:17@127.0.0.1:0` | expose an SPI device as a Bus Pirate TCP device |
| `-i2c-preset 0:bme280@0x76` | attach a built-in I2C device (repeatable) |
| `-spi-preset 0:w25q16@17` | attach a built-in SPI device (repeatable) |
| `-list-presets` | print the built-in device registry |
| `-i2c-plugin 0:0x76=dev.lua` | attach a Lua/WASM I2C device file (repeatable) |
| `-spi-plugin 0:17=dev.wasm` | attach a Lua/WASM SPI device file (repeatable) |
| `-uart-plugin 1=dev.lua` | attach a Lua/WASM serial companion (repeatable) |
| `-onewire PIN[:TEMP]` | attach a DS18B20 (repeatable) |
| `-dht PIN[:KIND[:HUM[:TEMP]]]` | attach a DHT11/22 (repeatable) |
| `-ultrasonic TRIG:ECHO[:CM]` | attach an HC-SR04 (repeatable) |
| `-gps UART[:LAT:LON]` | attach an NMEA GPS source |
| `-can CS[:INT[:PEER_ID]]`, `-can-bus 0\|1` | attach an MCP2515 and a CAN peer |
| `-itm` | print Cortex-M33 ITM stimulus writes |
| `-continue-after-halt` | keep PIO/PWM/DMA running after a core halts (firmware that returns from `main`) |
| `-input TEXT` | send bytes to USB CDC and UART0 after boot |
| `-bootsel` | hold BOOTSEL/QSPI CS low at boot and during the run |
| `-usb-hid`, `-hid-out HEX` | log decoded HID reports / send a host-to-device HID output report |
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
`RP2350SIM_CH9120_LOG`, `RP2350SIM_PIO_LOG`, `RP2350SIM_I2C_LOG`,
`RP2350SIM_DCP_LOG`.

## Ethernet

`-eth` attaches a W5500 on SPI0 (SCK GP18, MOSI GP19, MISO GP16, CS GP17),
the breadboard wiring used by the `pico2-w5500` UI board. The emulated hardware
sockets are backed by the host network, so TinyGo's stock
`tinygo.org/x/drivers/w5500` netdev driver works unchanged and can talk to
servers running on this machine. `firmware/w5500` runs a TCP client, a UDP
datagram and a twice-accepted TCP server against `tools/ethhost`
(`task run:w5500`).

TinyGo's `net.Dial("tcp", "host:port")` needs `LookupPort`, which is not
implemented in this TinyGo build; construct the address explicitly. Use a
4-byte IP: `net.IPv4` returns a 16-byte IPv4-mapped address that the stock
W5500 driver rejects:

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
module state. Bluetooth is not emulated; see [docs/boards.md](docs/boards.md).

The W6300 frame is `instruction, address hi, address lo, dummy, data`; its
register map and status/interrupt registers differ from the W5500, and it has
32 KB of TX and RX buffer per direction. UDP firmware must call the driver's
`Socket`/`Bind`/`Connect`/`Send` directly: TinyGo's `net.DialUDP` loses the
destination address on its second netdev call in this build (TCP is fine).

## Desktop UI

```sh
rp2350sim-ui --board geek firmware.elf
```

![Board picker](docs/images/picker.png)

The UI starts with a board picker. The list comes from `cmd/simui/boards.yaml`
(or `RP2350SIM_BOARDS=/path/to/boards.yaml`), and each entry declares its
hardware: an ST7789 LCD with its CS/DC/RST pins, touch and IMU controllers, a
W5500/W6300/CH9120 network controller, virtual Wi-Fi, a microSD directory, a
WS2812B data pin, scope channels, generated analog signals, logic-analyzer
protocols, UART TCP bridges, built-in I2C/SPI presets, scripted Lua/WASM device
files or remote Bus Pirate devices.
`rp2350sim-ui --list-boards` prints the ids.

Selecting a board attaches only those devices to the emulated chip and builds
the tabs accordingly:

- **Board** (always): the Pico 2 header diagram and the live GPIO table. After
  an OTA reset it shows the flash image, boot number and entry point.
- **LCD**: only when the board has an LCD; shows the framebuffer and maps
  clicks/drags to native touch coordinates.
- **Sensors**: touch, IMU, battery, backlight and card state.
- **Ethernet / Wi-Fi**: guest identity, live sockets with the host address to
  connect to, and a device log.
- **LEDs**: only for WS2812B boards; the last latched colours.
- **Scope** (always): up to 8 pin traces, 1 MHz sampling, a ~16 ms window,
  zoom/range controls, generated `analog:` waveforms and a toggleable Hann
  FFT/spectrum with a log frequency axis and peak readout.
- **Logic** (always): UART/I2C/SPI/USB decoding, triggers and **Save trace**.
- **Timed hobbies**: 1-Wire DS18B20, DHT11/22, HC-SR04, NMEA GPS and MCP2515
  CAN are Go device models with cycle-accurate edge timing.
- **OTP**: pages, rows, ECC status and locks.
- **Debug**: breakpoints, cycle stepping, registers, memory, source lines and
  DWARF variables, plus a GDB remote server (`--gdb` / toolbar **GDB**).
- **Bus**: I2C/SPI Bus Pirate TCP terminals, built-in presets and scripted
  Lua/WASM device plugins (with their debug log).
- **Console** (always): USB CDC + UART, click to type into the firmware.

The toolbar shows the selected board, a **Board…** button to return to the
picker, the firmware path field, **Open…** (OS dialog), run/pause and reset.
`--watch` reloads the firmware when the file changes on disk.
`--tab` selects the initial tab, `--paused` loads without executing,
`--logic-trigger`/`--logic-pause` arm the analyzer, and `--png out.png --run 0.6
firmware.elf` renders a headless snapshot. **Cmd+Q** on macOS or **Ctrl+Q**
elsewhere quits. See [docs/ui.md](docs/ui.md) for every tab and
[docs/peripherals.md](docs/peripherals.md) for the CLI/UI peripheral map.

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

## Tasks

The repository ships a [Taskfile](https://taskfile.dev) (`task` lists them).

| task | what it does |
|---|---|
| `task` | build the emulator and the blinky firmware |
| `task build` | build `build/rp2350sim` |
| `task install` | install `rp2350sim` into `$(go env GOPATH)/bin` |
| `task firmware:blinky` | TinyGo blinky ELF for the Pico 2 |
| `task firmware:blinky:uf2` | blinky UF2 for flashing a real board |
| `task firmware:busmaster`, `busdevice` | TinyGo I2C/SPI TCP bridge master and device demos |
| `task run:blinky` | run blinky and trace GP25 for three virtual seconds |
| `task run:lcd` | run the ST7789 example in the emulator with the LCD attached |
| `task run:sdcard` | read the card identity and list a host-backed FAT16 root directory |
| `task run:touchlcd` | run the Touch-LCD-2 example with a deterministic touch |
| `task touchlcd` | run the Touch-LCD-2 example and export its framebuffer |
| `task run:piotest` | run the PIO API checks (FIFOs, autopush/pull, IRQ, side-set) |
| `task run:cores` | build with `-scheduler=cores`, run a task on core 1, trace both pins |
| `task run:echo` | echo injected console input through USB CDC |
| `task run:ws2812` | run the WS2812B example and print the decoded LED colours |
| `task run:hid`, `run:hidgamepad` | log decoded USB HID keyboard/mouse or gamepad reports |
| `task run:w5500` | run the W5500 example (TCP client, UDP, TCP server) against `tools/ethhost` |
| `task run:w6300` | run the W6300 example (TCP client, UDP, TCP server) against `tools/ethhost` |
| `task lcd` | build/run the ST7789 example and export `build/lcd.png` |
| `task ui:blinky` (`ui`) | desktop UI on the `pico2` board with the blinky firmware |
| `task ui:lcd` | desktop UI on the `geek` board with the ST7789 example |
| `task ui:touchlcd` | desktop UI on the `touch-lcd2` board |
| `task ui:ws2812` | desktop UI on the `pico2-ws2812` board |
| `task ui:w6300` | desktop UI on the `poe-eth` board with the W6300 example |
| `task ui:w5500` | desktop UI on the `pico2-w5500` board with the W5500 example |
| `task run:ch9120` | CH9120 UART-to-Ethernet example against the host echo |
| `task ui:ch9120` | desktop UI on the `rp2350-eth` board with the CH9120 example |
| `task run:wifi` | virtual Wi-Fi TCP client against the host echo |
| `task ui:wifi` | desktop UI on the Pico 2 W (virtual Wi-Fi) with the netdev example |
| `task ui:scope` | scope demo with a 1 kHz sine on GP26 (`pico2-analog`) |
| `task ui:scope:fft` | scope with the FFT spectrum view; `ui:scope:fft:png` renders it |
| `task ui:hid` | HID tab with the TinyGo keyboard + mouse firmware |
| `task ui:hid:composite` | HID tab with pico-sdk keyboard/mouse/media/gamepad (BOOTSEL latched) |
| `task ui:hid:png` | headless HID-tab render to `build/ui-hid.png` |
| `task ui:psram` | PSRAM board with the Pico SDK PSRAM demo |
| `task ui:debug` | Debug tab stopped at the C fixture's `debug_marker` |
| `task ui:debug:png` | headless render of the Debug tab to `build/ui-debug.png` |
| `task ui:logic` | logic analyzer with the UART/I2C/SPI demo firmware |
| `task ui:serial` | UART1 bridged over TCP with an echo firmware |
| `task ui:bus` | Bus Pirate board paused: connect terminals, queue ACK/data, then Run |
| `task ui:bus:png` | headless render of the Bus Pirate tab to `build/ui-bus.png` |
| `task ui:presets` | built-in BME280/SSD1306/MPU6050/W25Q16 board |
| `task ui:presets:png` | headless render of the built-in presets tab to `build/ui-presets.png` |
| `task run:presets` | probe the four built-in bus presets and print their IDs |
| `task ui:plugins` | Lua/WASM scripted device examples in the Bus tab |
| `task ui:plugins:png` | headless render of the Scripted plugins card to `build/ui-plugins.png` |
| `task plugins:build` | rebuild the bundled Lua device runtime (wasi-sdk + Lua source) |
| `task ui:ota` | OTA demo in the UI with a persistent flash image and self-update banner |
| `task ui:ota-png` | headless render of the OTA banner to `build/ui-ota.png` |
| `task ui:png` | headless UI render of the LCD tab to `build/ui.png` |
| `task test` / `task test:qemu` | unit tests / QEMU differential corpus |
| `task lint` | go vet, golangci-lint (modernize) and go fix diff in every module |
| `task c:build` | build the Pico SDK C examples (needs `PICO_SDK_PATH`) |
| `task picoexamples:build` | build the pinned pico-examples subset (needs `PICO_SDK_PATH`) |
| `task c:hello`, `c:blinky`, `c:float`, `c:dcp`, `c:multicore` | run the C examples |
| `task c:otp` | run the C OTP demo and dump the programmed array |
| `task c:psram` | run the Pico SDK PSRAM demo with an 8 MiB device |
| `task c:debug` | run the C debugger fixture and stop at `debug_marker` |
| `task debug:test` | run the TinyGo and C DWARF debugger tests |
| `task c:ota` | run the single-slot self-update demo and reboot the new image |
| `task ci` | vet, unit tests, QEMU corpus, firmware smokes and a UI snapshot |
| `task release:check` | validate the GoReleaser configuration |
| `task release:snapshot` | build all release archives under `dist/` (no publish) |
| `task docs:serve` | preview the Geekdoc documentation at <http://localhost:1313> |
| `task docs:build` | build the Geekdoc site into `site/public` |
| `task clean` | remove `build/` |

## Documentation

The user guide lives in [`docs/`](docs/README.md)

The same pages are published as a Geekdoc site at
<https://akhenakh.github.io/rp2350sim/>. Preview it locally with
`task docs:serve`; `site/` contains the Hugo configuration and
`tools/gen_docs_site.py` generates its content from `docs/`.

## Tests

```sh
go test ./...
```

`task ci` is the full gate: `go vet`, the unit tests, the QEMU differential
corpus (skipped when `qemu-system-arm`/`arm-none-eabi-gdb` are missing), every
firmware example, and `tools/smoke.sh`, which asserts on observable behaviour
(GPIO edges, console echo, PIO checks, WS2812 colours, multicore launch, LCD
pixels, W5500 and W6300 TCP/UDP against `tools/ethhost`) plus a headless UI snapshot
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
- Missing peripherals: USB host mode. The RP2350 USB controller is modelled
  as a device, with CDC, MIDI and HID classes enumerated by the emulated host.
- The default `pico2` build does not launch core 1. With
  `tinygo build -scheduler=cores` the runtime starts core 1 through the SIO
  FIFO; both cores then run tasks (see `firmware/cores` and `task run:cores`).
- Time is virtual and one instruction is one cycle. Idle firmware fast-forwards
  to the next microsecond tick, so sleeps cost almost nothing; a compute-bound
  loop runs at about 90M instructions/s, and blinky's 2s program finishes in
  ~0.06s of wall time. `-cpuprofile` writes a Go CPU profile.

## References

- RP2350 datasheet; `tinygo/src/device/rp/rp2350.go` register definitions
- TinyGo `machine` and `runtime` packages for the boot contract
- `qemu-system-arm -M mps2-an505` as the Cortex-M33 instruction oracle

## Releases

Prebuilt Linux, macOS and Windows archives with both `rp2350sim` and
`rp2350sim-ui` are on the
[GitHub Releases page](https://github.com/akhenakh/rp2350sim/releases) (see
[Download](#download)). Tags matching `v*` build and publish them from the
GoReleaser workflow. See [docs/release.md](docs/release.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
